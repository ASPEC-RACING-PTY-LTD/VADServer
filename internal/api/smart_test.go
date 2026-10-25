package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
)

// fakeWorker stands in for the inference worker: voice activity and a
// transcript in which "clip that" is said at 15 s.
func fakeWorker(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			json.NewEncoder(w).Encode(map[string]any{"version": "test", "device": "cpu", "models": map[string]any{
				"silero-vad": map[string]any{"installed": true}, "whisper": map[string]any{"installed": true}, "ast": map[string]any{"installed": false},
			}})
		case "/v1/audio":
			if !strings.Contains(r.URL.Query().Get("tasks"), "transcript") {
				http.Error(w, `{"error":"unexpected tasks"}`, 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"vad": []any{map[string]any{"start": 14.5, "end": 16.5, "score": 1}},
				"transcript": map[string]any{"language": "en", "model": "fake", "utterances": []any{map[string]any{
					"start": 14.5, "end": 16.5, "text": "okay clip that",
					"words": []any{map[string]any{"w": "okay", "s": 14.5, "e": 14.9, "p": 0.9}, map[string]any{"w": "clip", "s": 15, "e": 15.3, "p": 0.9}, map[string]any{"w": "that", "s": 15.3, "e": 15.6, "p": 0.9}},
				}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSmartProcessingEndToEnd: a synthetic recording is analysed (server
// loudness + worker transcript), reviewed, rendered beside the original
// with its dates, and the original is cleaned up after verification.
func TestSmartProcessingEndToEnd(t *testing.T) {
	tools := media.Detect()
	if !tools.Available() {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	wk := fakeWorker(t)
	e := newEnv(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.srv.RunMedia(ctx)
	go e.srv.RunSmart(ctx)
	admin := e.setupAdmin()
	dc := e.enrollDevice(admin, "admin", adminPass)

	set := admin.must("GET", "/api/v1/admin/smart/settings", nil, 200)["settings"].(map[string]any)
	set["worker_url"] = wk.URL
	admin.must("PUT", "/api/v1/admin/smart/settings", set, 200)
	st := admin.must("GET", "/api/v1/smart/status?fresh=1", nil, 200)
	if st["worker"].(map[string]any)["reachable"] != true {
		t.Fatalf("worker status: %v", st["worker"])
	}

	// 20 s: quiet, a loud burst at 8-10 s.
	tmp := t.TempDir()
	src := filepath.Join(tmp, "OmeTV.mp4")
	gen := exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=20",
		"-f", "lavfi", "-i", "aevalsrc=if(between(t\\,8\\,10)\\,0.8\\,0.002)*sin(2*PI*440*t):s=48000:d=20", "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-shortest", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("make recording: %v %s", err, out)
	}
	res, err := dc.UploadFile(ctx, src, "/Rec/OmeTV.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := nodeID(t, res.Node)
	orig := admin.must("GET", fmt.Sprintf("/api/v1/files/%d", id), nil, 200)

	// Unknown presets and traversal in folder names are refused.
	if code, _ := admin.req("POST", "/api/v1/smart/queues", map[string]any{"name": "x", "preset_ids": []string{"builtin:nope"}, "file_ids": []int64{id}}); code != 404 {
		t.Fatalf("unknown preset: %d", code)
	}
	if code, _ := admin.req("POST", "/api/v1/smart/queues", map[string]any{"name": "x", "preset_ids": []string{"builtin:loud"}, "file_ids": []int64{id}, "output": map[string]any{"destination": "subfolder", "subfolder": "../escape"}}); code != 400 {
		t.Fatalf("traversal subfolder: %d", code)
	}

	filter := map[string]any{"rules": []any{
		map[string]any{"name": "Loud", "category": "audio", "when": map[string]any{"kind": "loudness", "mode": "sudden_rise"}},
		map[string]any{"name": "Clip that", "category": "speech", "when": map[string]any{"kind": "keyword", "terms": []string{"clip that"}}},
	}, "context": map[string]any{"threshold": 0.3, "min_event": 0.1, "max_segment": 30, "pre": 1, "post": 1, "merge": false, "merge_gap": 0, "trim_silence": false, "snap_speech": false, "dedupe": 0.6, "max_suggestions": 10}}
	q := admin.must("POST", "/api/v1/smart/queues", map[string]any{"name": "OmeTV", "filter": filter, "folder_ids": []int64{nodeParent(t, admin, id)}, "recursive": true}, 201)
	qid := int64(q["id"].(float64))

	waitItem := func(want ...string) map[string]any {
		var last map[string]any
		for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
			items := admin.must("GET", fmt.Sprintf("/api/v1/smart/queues/%d/items", qid), nil, 200)["items"].([]any)
			if len(items) != 1 {
				t.Fatalf("items: %v", items)
			}
			it := items[0].(map[string]any)
			last = it
			for _, w := range want {
				if it["state"] == w {
					return it
				}
			}
			if it["state"] == "failed" {
				t.Fatalf("item failed: %v", it["error"])
			}
		}
		t.Fatalf("item never reached %v: %v; queue %v", want, last, admin.must("GET", fmt.Sprintf("/api/v1/smart/queues/%d", qid), nil, 200)["state"])
		return nil
	}
	it := waitItem("ready")
	iid := int64(it["id"].(float64))
	detail := admin.must("GET", fmt.Sprintf("/api/v1/smart/items/%d", iid), nil, 200)
	segs := detail["segments"].([]any)
	if len(segs) != 2 {
		t.Fatalf("segments: %v", segs)
	}
	loud, kw := segs[0].(map[string]any), segs[1].(map[string]any)
	if s := loud["start"].(float64); s < 6 || s > 8 {
		t.Fatalf("loud segment: %v", loud)
	}
	if s := kw["start"].(float64); s < 13.5 || s > 14.5 || kw["label"] != "Clip that" {
		t.Fatalf("keyword segment: %v", kw)
	}
	if r := kw["reasons"].([]any); len(r) == 0 || !strings.Contains(r[0].(map[string]any)["text"].(string), "clip that") {
		t.Fatalf("reasons: %v", kw["reasons"])
	}
	if tr := admin.must("GET", fmt.Sprintf("/api/v1/smart/items/%d/transcript", iid), nil, 200); tr["available"] != true {
		t.Fatalf("transcript: %v", tr)
	}
	if hits := admin.must("GET", "/api/v1/smart/search?q=clip+that", nil, 200)["hits"].([]any); len(hits) != 1 {
		t.Fatalf("search: %v", hits)
	}

	// Another user sees none of it.
	other := e.createUser(admin, "bob", 0)
	if code, _ := other.req("GET", fmt.Sprintf("/api/v1/smart/queues/%d", qid), nil); code != 404 {
		t.Fatalf("other user reads the queue: %d", code)
	}
	if code, _ := other.req("PATCH", fmt.Sprintf("/api/v1/smart/segments/%d", int64(kw["id"].(float64))), map[string]any{"state": "approved"}); code != 404 {
		t.Fatalf("other user edits a segment: %d", code)
	}
	if out := other.must("POST", "/api/v1/smart/cleanup", map[string]any{"item_ids": []int64{iid}}, 200); out["removed"].(float64) != 0 {
		t.Fatalf("other user cleaned up: %v", out)
	}

	// Review: reject the loud moment, trim the keyword one, finish.
	admin.must("PATCH", fmt.Sprintf("/api/v1/smart/segments/%d", int64(loud["id"].(float64))), map[string]any{"state": "rejected"}, 200)
	admin.must("PATCH", fmt.Sprintf("/api/v1/smart/segments/%d", int64(kw["id"].(float64))), map[string]any{"start": 13.0, "end": 17.0, "state": "approved", "name": "Clip"}, 200)
	if code, _ := admin.req("PATCH", fmt.Sprintf("/api/v1/smart/segments/%d", int64(kw["id"].(float64))), map[string]any{"end": 99.0}); code != 400 {
		t.Fatalf("segment past the end: %d", code)
	}
	admin.must("POST", fmt.Sprintf("/api/v1/smart/items/%d/actions/review", iid), map[string]any{}, 200)
	waitItem("done")

	detail = admin.must("GET", fmt.Sprintf("/api/v1/smart/items/%d", iid), nil, 200)
	outs := detail["outputs"].([]any)
	if len(outs) != 1 || outs[0].(map[string]any)["state"] != "done" {
		t.Fatalf("outputs: %v", outs)
	}
	o := outs[0].(map[string]any)
	if !strings.HasPrefix(o["path"].(string), "/Rec/OmeTV - Clip") {
		t.Fatalf("clip path: %v", o["path"])
	}
	clip := admin.must("GET", fmt.Sprintf("/api/v1/files/%d", int64(o["node_id"].(float64))), nil, 200)
	if clip["modified_at"] != orig["modified_at"] {
		t.Fatalf("clip date %v, original %v", clip["modified_at"], orig["modified_at"])
	}
	info := admin.must("GET", fmt.Sprintf("/api/v1/files/%d/media", int64(o["node_id"].(float64))), nil, 200)["info"].(map[string]any)
	if d := info["duration"].(float64); d < 3.5 || d > 6 {
		t.Fatalf("clip length %.2f", d)
	}

	// Reviewing again renders nothing new (same boundaries).
	admin.must("POST", fmt.Sprintf("/api/v1/smart/items/%d/actions/reopen", iid), nil, 200)
	admin.must("POST", fmt.Sprintf("/api/v1/smart/items/%d/actions/review", iid), map[string]any{}, 200)
	waitItem("done")
	if outs := admin.must("GET", fmt.Sprintf("/api/v1/smart/items/%d", iid), nil, 200)["outputs"].([]any); len(outs) != 1 {
		t.Fatalf("duplicate clips: %v", outs)
	}

	// Cleanup: eligible, then removed after verification.
	cl := admin.must("GET", fmt.Sprintf("/api/v1/smart/cleanup?queue_id=%d", qid), nil, 200)
	if cl["items"].([]any)[0].(map[string]any)["status"] != "eligible" {
		t.Fatalf("cleanup list: %v", cl)
	}
	stats := admin.must("GET", "/api/v1/smart/stats", nil, 200)["total"].(map[string]any)
	if stats["estimated_bytes"].(float64) <= 0 || stats["generated_count"].(float64) != 1 {
		t.Fatalf("stats before cleanup: %v", stats)
	}
	done := admin.must("POST", "/api/v1/smart/cleanup", map[string]any{"item_ids": []int64{iid}, "mode": "delete"}, 200)
	if done["removed"].(float64) != 1 || done["freed"].(float64) <= 0 {
		t.Fatalf("cleanup: %v", done)
	}
	if code, _ := admin.req("GET", fmt.Sprintf("/api/v1/files/%d", id), nil); code != 404 {
		t.Fatalf("original still there: %d", code)
	}
	admin.must("GET", fmt.Sprintf("/api/v1/files/%d", int64(o["node_id"].(float64))), nil, 200) // the clip stays
	stats = admin.must("GET", "/api/v1/smart/stats", nil, 200)["total"].(map[string]any)
	if stats["freed_bytes"].(float64) <= 0 || stats["deleted_count"].(float64) != 1 || stats["estimated_bytes"].(float64) != 0 {
		t.Fatalf("stats after cleanup: %v", stats)
	}
	// Doing it again is a no-op.
	if again := admin.must("POST", "/api/v1/smart/cleanup", map[string]any{"item_ids": []int64{iid}}, 200); again["removed"].(float64) != 0 {
		t.Fatalf("second cleanup: %v", again)
	}

	// Queue actions: a finished queue can be archived and reopened; the
	// duplicate (paused, nothing done yet) is resumed, paused and cancelled.
	admin.must("POST", fmt.Sprintf("/api/v1/smart/queues/%d/actions/archive", qid), nil, 200)
	if code, _ := admin.req("POST", fmt.Sprintf("/api/v1/smart/queues/%d/items", qid), map[string]any{"file_ids": []int64{}}); code != 409 {
		t.Fatalf("added to an archived queue: %d", code)
	}
	admin.must("POST", fmt.Sprintf("/api/v1/smart/queues/%d/actions/reopen", qid), nil, 200)
	dup := admin.must("POST", fmt.Sprintf("/api/v1/smart/queues/%d/actions/duplicate", qid), nil, 201)
	if dup["state"] != "paused" {
		t.Fatalf("duplicate: %v", dup)
	}
	did := int64(dup["id"].(float64))
	for _, a := range []string{"resume", "pause", "cancel", "reopen", "pause"} {
		admin.must("POST", fmt.Sprintf("/api/v1/smart/queues/%d/actions/%s", did, a), nil, 200)
	}
	admin.must("DELETE", fmt.Sprintf("/api/v1/smart/queues/%d", did), nil, 204)
	admin.must("GET", fmt.Sprintf("/api/v1/files/%d", int64(o["node_id"].(float64))), nil, 200) // deleting a queue keeps files
}

func nodeParent(t *testing.T, w *web, id int64) int64 {
	n := w.must("GET", fmt.Sprintf("/api/v1/files/%d", id), nil, 200)
	if n["node"] != nil {
		n = n["node"].(map[string]any)
	}
	return int64(n["parent_id"].(float64))
}
