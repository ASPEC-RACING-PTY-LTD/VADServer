package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
)

func TestVideoDraftsTrashExpiryAndReuploadBlock(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	dc := e.enrollDevice(admin, "admin", adminPass)
	ctx := context.Background()
	tmp := t.TempDir()
	p, _ := makeFile(t, tmp, "rec.mp4", 64<<10)
	res, err := dc.UploadFile(ctx, p, "/Medal/rec.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := nodeID(t, res.Node)

	// Drafts keep instructions and a reference to the source.
	d := admin.must("POST", "/api/v1/video/drafts", map[string]any{"source_id": id, "data": map[string]any{"clips": []any{map[string]any{"number": 1, "start": 1.5, "end": 4}}}}, 201)
	did := int64(d["id"].(float64))
	if d["name"] != "rec" || d["source_state"] != "ok" || d["source_name"] != "rec.mp4" {
		t.Fatalf("draft = %v", d)
	}
	admin.must("PUT", fmt.Sprintf("/api/v1/video/drafts/%d", did), map[string]any{"name": "Goals", "data": map[string]any{"next": 2}}, 200)
	got := admin.must("GET", fmt.Sprintf("/api/v1/video/drafts/%d", did), nil, 200)
	if got["name"] != "Goals" || got["data"].(map[string]any)["next"].(float64) != 2 {
		t.Fatalf("updated draft = %v", got)
	}
	list := admin.must("GET", fmt.Sprintf("/api/v1/video/drafts?source=%d", id), nil, 200)["items"].([]any)
	if len(list) != 1 {
		t.Fatalf("drafts of the source: %v", list)
	}
	if code, out := admin.req("POST", "/api/v1/video/drafts", map[string]any{"source_id": id, "data": []any{1}}); code != 400 {
		t.Fatalf("non-object draft data accepted: %d %v", code, out)
	}
	// A trashed source is reported, not silently dropped.
	admin.must("DELETE", fmt.Sprintf("/api/v1/files/%d", id), nil, 200)
	if got := admin.must("GET", fmt.Sprintf("/api/v1/video/drafts/%d", did), nil, 200); got["source_state"] != "trashed" {
		t.Fatalf("source state after trash: %v", got["source_state"])
	}
	admin.must("POST", fmt.Sprintf("/api/v1/trash/%d/restore", id), nil, 200)
	admin.must("DELETE", fmt.Sprintf("/api/v1/video/drafts/%d", did), nil, 200)
	admin.must("DELETE", fmt.Sprintf("/api/v1/video/drafts/%d", did), nil, 404)

	// Not a video: no thumbnail or media endpoints.
	pt, _ := makeFile(t, tmp, "notes.txt", 100)
	res, err = dc.UploadFile(ctx, pt, "/notes.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := admin.req("GET", fmt.Sprintf("/api/v1/files/%d/thumbnail", nodeID(t, res.Node)), nil); code != 400 || errCode(out) != "not_video" {
		t.Fatalf("thumbnail of a text file: %d %v", code, out)
	}

	// A trashed item with its own expiry is purged when it passes.
	admin.must("DELETE", fmt.Sprintf("/api/v1/files/%d", id), nil, 200)
	if err := e.st.SetTrashExpiry(ctx, 1, id, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	e.srv.RunMaintenance(ctx, false)
	if items := admin.must("GET", "/api/v1/trash", nil, 200)["items"]; items != nil && len(items.([]any)) != 0 {
		t.Fatalf("expired trash kept: %v", items)
	}

	// A blocked original is not uploaded again by a device; other content is.
	shaOf := func(path string) string {
		h, err := storage.HashFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if err := e.st.BlockReupload(ctx, 1, "/Medal/rec.mp4", shaOf(p), time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	res, err = dc.UploadFile(ctx, p, "/Medal/rec.mp4", nil)
	if err != nil || res.Result != "skipped" {
		t.Fatalf("blocked re-upload: %+v %v", res, err)
	}
	if code, _ := admin.req("GET", "/api/v1/files/resolve?path=/Medal/rec.mp4", nil); code != 404 {
		t.Fatal("blocked original came back")
	}
	p2, _ := makeFile(t, tmp, "rec2.mp4", 1000)
	res, err = dc.UploadFile(ctx, p2, "/Medal/rec.mp4", nil)
	if err != nil || res.Result != "created" {
		t.Fatalf("new content at the blocked path: %+v %v", res, err)
	}
}

func TestVideoExportWithoutFFmpeg(t *testing.T) {
	if media.Detect().Available() {
		t.Skip("ffmpeg installed; covered by TestVideoExportEndToEnd")
	}
	e := newEnv(t, "")
	admin := e.setupAdmin()
	if out := admin.must("GET", "/api/v1/media/status", nil, 200); out["available"] != false {
		t.Fatalf("status = %v", out)
	}
	code, out := admin.req("POST", "/api/v1/video/exports", map[string]any{"source_id": 1, "folder_id": 1, "clips": []any{map[string]any{"number": 1, "clip": map[string]any{"start": 0, "end": 1}}}})
	if code != 501 || errCode(out) != "media_unavailable" {
		t.Fatalf("export without ffmpeg: %d %v", code, out)
	}
}

// TestVideoExportEndToEnd exports two clips from a real recording and
// erases the original (runs where FFmpeg is installed, as in the image).
func TestVideoExportEndToEnd(t *testing.T) {
	tools := media.Detect()
	if !tools.Available() {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	e := newEnv(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.srv.RunMedia(ctx)
	admin := e.setupAdmin()
	dc := e.enrollDevice(admin, "admin", adminPass)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "MedalTVScreenRecording.mp4")
	gen := exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=20",
		"-f", "lavfi", "-i", "sine=frequency=330:duration=20", "-c:v", "libx264", "-g", "30", "-c:a", "aac", "-shortest", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("make recording: %v %s", err, out)
	}
	res, err := dc.UploadFile(ctx, src, "/Medal/MedalTVScreenRecording.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := nodeID(t, res.Node)
	info := admin.must("GET", fmt.Sprintf("/api/v1/files/%d/media", id), nil, 200)["info"].(map[string]any)
	if info["width"].(float64) != 640 || info["duration"].(float64) < 19 {
		t.Fatalf("media info = %v", info)
	}
	// Timeline frame.
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/files/%d/frame?t=3.2&h=72", e.ts.URL, id), nil)
	resp, err := admin.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || len(img) < 100 {
		t.Fatalf("frame: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	root := admin.must("GET", "/api/v1/files", nil, 200)["folder"].(map[string]any)
	admin.must("POST", "/api/v1/files/folder", map[string]any{"parent_id": root["id"], "name": "Clips"}, 201)
	folder := admin.must("GET", "/api/v1/files/resolve?path=/Clips", nil, 200)["node"].(map[string]any)
	job := admin.must("POST", "/api/v1/video/exports", map[string]any{
		"source_id": id, "folder_id": folder["id"], "quality": "high", "erase_original": true,
		"clips": []any{
			map[string]any{"number": 1, "name": "RocketLeague Goal.mp4", "clip": map[string]any{"start": 2, "end": 6}},
			map[string]any{"number": 2, "name": "Insane Save", "clip": map[string]any{"start": 10, "end": 14, "speed": 2, "preserve_pitch": true, "crop": map[string]any{"x": 0.21875, "y": 0, "w": 0.5625, "h": 1}}},
			map[string]any{"number": 3, "name": "Insane Save", "clip": map[string]any{"start": 15, "end": 18, "volume": 1}},
		},
	}, 202)
	jid := int64(job["id"].(float64))
	var final map[string]any
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		final = admin.must("GET", fmt.Sprintf("/api/v1/video/exports/%d", jid), nil, 200)
		if s := final["state"]; s != "queued" && s != "running" {
			break
		}
	}
	if final["state"] != "done" {
		t.Fatalf("export ended %v: %v", final["state"], final["progress"])
	}
	prog := final["progress"].(map[string]any)
	clips := prog["clips"].([]any)
	wantNames := []string{"RocketLeague Goal.mp4", "Insane Save.mp4", "Insane Save (2).mp4"}
	for i, c := range clips {
		cm := c.(map[string]any)
		if cm["state"] != "done" || cm["name"] != wantNames[i] {
			t.Fatalf("clip %d = %v", i, cm)
		}
		admin.must("GET", "/api/v1/files/resolve?path="+url.QueryEscape("/Clips/"+wantNames[i]), nil, 200)
	}
	orig := prog["original"].(map[string]any)
	if orig["state"] != "trashed" || orig["expires_at"].(float64) < float64(time.Now().Add(6*24*time.Hour).UnixMilli()) {
		t.Fatalf("original = %v", orig)
	}
	if code, _ := admin.req("GET", "/api/v1/files/resolve?path=/Medal/MedalTVScreenRecording.mp4", nil); code != 404 {
		t.Fatal("original not trashed")
	}
	// The synced PC that still has the recording does not bring it back.
	res, err = dc.UploadFile(ctx, src, "/Medal/MedalTVScreenRecording.mp4", nil)
	if err != nil || res.Result != "skipped" {
		t.Fatalf("re-upload of the erased original: %+v %v", res, err)
	}
	// Exported clips get thumbnails like any video.
	c1 := admin.must("GET", "/api/v1/files/resolve?path="+url.QueryEscape("/Clips/Insane Save.mp4"), nil, 200)["node"].(map[string]any)
	ok := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		code, _ := admin.req("GET", fmt.Sprintf("/api/v1/files/%d/thumbnail", int64(c1["id"].(float64))), nil)
		if code == 200 {
			ok = true
			break
		}
		if code != 202 {
			t.Fatalf("thumbnail status %d", code)
		}
	}
	if !ok {
		t.Fatal("no thumbnail for the exported clip")
	}
	cinfo := admin.must("GET", fmt.Sprintf("/api/v1/files/%d/media", int64(c1["id"].(float64))), nil, 200)["info"].(map[string]any)
	if d := cinfo["duration"].(float64); d < 1.8 || d > 2.2 || cinfo["width"].(float64) != 360 {
		t.Fatalf("sped-up cropped clip = %v", cinfo)
	}
	// A second export of a missing source is refused clearly.
	if code, out := admin.req("POST", "/api/v1/video/exports", map[string]any{"source_id": id, "folder_id": folder["id"], "clips": []any{map[string]any{"number": 1, "clip": map[string]any{"start": 0, "end": 1}}}}); code != 404 || errCode(out) != "source_missing" {
		t.Fatalf("export of a trashed source: %d %v", code, out)
	}
}
