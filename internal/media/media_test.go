package media

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const probeJSON = `{
 "streams": [
  {"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"60/1","r_frame_rate":"60/1","bit_rate":"12000000"},
  {"codec_type":"audio","codec_name":"aac","sample_rate":"48000","bit_rate":"192000"},
  {"codec_type":"audio","codec_name":"aac","sample_rate":"48000","bit_rate":"128000"},
  {"codec_type":"video","codec_name":"mjpeg","width":300,"height":300,"disposition":{"attached_pic":1}}
 ],
 "format": {"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"600.5","bit_rate":"12500000","size":"938281250"}
}`

func TestParseProbe(t *testing.T) {
	i, err := parseProbe([]byte(probeJSON))
	if err != nil {
		t.Fatal(err)
	}
	if i.Width != 1920 || i.Height != 1080 || i.FPS != 60 || i.VideoCodec != "h264" || i.AudioTracks != 2 || i.SampleRate != 48000 || i.Duration != 600.5 || i.VideoBitrate != 12000000 {
		t.Fatalf("info = %+v", i)
	}
	// A phone video recorded upright: the displayed size is rotated.
	rot := `{"streams":[{"codec_type":"video","codec_name":"hevc","width":1920,"height":1080,"avg_frame_rate":"30000/1001","side_data_list":[{"rotation":-90}]}],"format":{"duration":"10","size":"1000000"}}`
	i, err = parseProbe([]byte(rot))
	if err != nil {
		t.Fatal(err)
	}
	if i.Width != 1080 || i.Height != 1920 || i.HasAudio() || i.Bitrate != 800000 {
		t.Fatalf("rotated info = %+v", i)
	}
	if _, err := parseProbe([]byte(`{"streams":[{"codec_type":"audio","codec_name":"mp3"}],"format":{}}`)); err == nil {
		t.Fatal("an audio-only file must not be a video")
	}
}

func info1080() *Info {
	return &Info{Duration: 600, Width: 1920, Height: 1080, FPS: 60, VideoCodec: "h264", AudioTracks: 1, SampleRate: 48000, VideoBitrate: 12e6, AudioBitrate: 192e3}
}

func TestNormalizeAndMode(t *testing.T) {
	info := info1080()
	c := Clip{Start: 15, End: 32, Volume: 1}
	if err := c.Normalize(info); err != nil {
		t.Fatal(err)
	}
	if c.Speed != 1 || c.NeedsEncode() || Mode(&c, QualityCopy) != QualityCopy || Mode(&c, QualitySmall) != QualitySmall {
		t.Fatalf("plain trim: %+v", c)
	}
	c = Clip{Start: 590, End: 700, Speed: 2, Volume: 1, Crop: &Rect{0, 0, 1, 1}}
	if err := c.Normalize(info); err != nil {
		t.Fatal(err)
	}
	if c.End != 600 || c.Crop != nil || Mode(&c, QualityCopy) != QualityHigh || c.OutputDuration() != 5 {
		t.Fatalf("clamped/sped clip: %+v", c)
	}
	for _, bad := range []Clip{
		{Start: 10, End: 10},
		{Start: 700, End: 710},
		{Start: 1, End: 5, Speed: 8},
		{Start: 1, End: 5, Freezes: []Freeze{{At: 9, Duration: 1}}},
		{Start: 1, End: 5, Crop: &Rect{0.5, 0, 0.6, 1}},
	} {
		if err := bad.Normalize(info); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	var parsed Clip
	if err := json.Unmarshal([]byte(`{"start":1,"end":2}`), &parsed); err != nil || parsed.Speed != 1 || parsed.Volume != 1 {
		t.Fatalf("defaults from JSON: %+v %v", parsed, err)
	}
	// A muted clip at another volume is still a stream copy.
	c = Clip{Start: 1, End: 5, Muted: true, Volume: 0.3}
	_ = c.Normalize(info)
	if c.NeedsEncode() {
		t.Fatal("muting alone must not need an encode")
	}
}

func TestContainer(t *testing.T) {
	info := info1080()
	if f, e := Container(QualityCopy, "rec.mkv", info); f != "matroska" || e != ".mkv" {
		t.Fatal(f, e)
	}
	if f, e := Container(QualityCopy, "rec.mp4", info); f != "mp4" || e != ".mp4" {
		t.Fatal(f, e)
	}
	if f, e := Container(QualityHigh, "rec.mkv", info); f != "mp4" || e != ".mp4" {
		t.Fatal(f, e)
	}
}

func TestExportArgs(t *testing.T) {
	info := info1080()
	c := Clip{Start: 15, End: 32, Volume: 1}
	_ = c.Normalize(info)
	a := strings.Join(ExportArgs(&c, QualityCopy, info, "mp4", "out.part"), " ")
	for _, want := range []string{"-ss 15.000 -t 17.000 -i {input}", "-c copy", "-map 0:a?", "-movflags +faststart", "-f mp4 out.part"} {
		if !strings.Contains(a, want) {
			t.Fatalf("copy args %q lack %q", a, want)
		}
	}
	c = Clip{Start: 100, End: 110, Speed: 1.5, PreservePitch: true, Volume: 0.5,
		Freezes: []Freeze{{At: 105, Duration: 2}}, Crop: &Rect{0.25, 0, 0.5, 1}}
	if err := c.Normalize(info); err != nil {
		t.Fatal(err)
	}
	a = strings.Join(ExportArgs(&c, Mode(&c, QualityCopy), info, "mp4", "out.part"), " ")
	for _, want := range []string{
		"crop=960:1080:480:0", "split=2", "asplit=2", "trim=start=0.000000:end=5.000000", "trim=start=5.000000:end=10.000000",
		"tpad=stop_mode=clone:stop_duration=2.000000", "apad=pad_dur=2.000000", "setpts=PTS/1.500000", "atempo=1.5",
		"volume=0.500000", "concat=n=2:v=1:a=1[vout][aout]", "-c:v libx264", "-crf 18", "-maxrate", "-c:a aac",
	} {
		if !strings.Contains(a, want) {
			t.Fatalf("encode args %q lack %q", a, want)
		}
	}
	// Without pitch preservation the sample rate carries the speed.
	c = Clip{Start: 1, End: 3, Speed: 0.5, Volume: 1}
	_ = c.Normalize(info)
	a = strings.Join(ExportArgs(&c, QualityBalanced, info, "mp4", "o"), " ")
	if !strings.Contains(a, "asetrate=24000.000000,aresample=48000") || !strings.Contains(a, "-crf 22") {
		t.Fatalf("slow clip args %q", a)
	}
	// A muted encode has no audio graph.
	c = Clip{Start: 1, End: 3, Muted: true, Crop: &Rect{0, 0, 0.5, 0.5}}
	_ = c.Normalize(info)
	a = strings.Join(ExportArgs(&c, QualityHigh, info, "mp4", "o"), " ")
	if strings.Contains(a, "[0:a") || !strings.Contains(a, "-an") || !strings.Contains(a, "concat=n=1:v=1:a=0") {
		t.Fatalf("muted args %q", a)
	}
}

func TestAtempoChain(t *testing.T) {
	if got := strings.Join(atempo(4), ","); got != "atempo=2,atempo=2" {
		t.Fatal(got)
	}
	if got := strings.Join(atempo(0.25), ","); got != "atempo=0.5,atempo=0.5" {
		t.Fatal(got)
	}
	if len(atempo(1)) != 0 {
		t.Fatal("no tempo change at 1x")
	}
}

func TestEstimateSize(t *testing.T) {
	info := info1080()
	c := Clip{Start: 0, End: 60, Volume: 1}
	_ = c.Normalize(info)
	copyEst := EstimateSize(&c, QualityCopy, info)
	if copyEst < 85e6 || copyEst > 95e6 { // ~12.2 Mbit/s for a minute
		t.Fatalf("copy estimate %d", copyEst)
	}
	small := EstimateSize(&c, QualitySmall, info)
	if small >= copyEst {
		t.Fatalf("small (%d) must be under copy (%d)", small, copyEst)
	}
}

func TestPosterTime(t *testing.T) {
	if PosterTime(1) != 0 || PosterTime(20) != 2 || PosterTime(3600) != 8 {
		t.Fatal(PosterTime(1), PosterTime(20), PosterTime(3600))
	}
}

func TestCacheSweep(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	keep := strings.Repeat("a", 64)
	gone := strings.Repeat("b", 64)
	for _, sha := range []string{keep, gone} {
		if err := c.Put(c.PosterPath(7, sha), []byte("jpg")); err != nil {
			t.Fatal(err)
		}
	}
	old := c.FramePath(7, keep, 72, 1000)
	fresh := c.FramePath(7, keep, 72, 2000)
	_ = c.Put(old, []byte("f"))
	_ = c.Put(fresh, []byte("f"))
	long := time.Now().Add(-FrameIdle - time.Hour)
	_ = os.Chtimes(old, long, long)
	n := c.Sweep(context.Background(), func(_ int64, sha string) bool { return sha == keep })
	if n != 2 {
		t.Fatalf("removed %d, want 2 (one content, one frame)", n)
	}
	if _, err := os.Stat(c.PosterPath(7, gone)); err == nil {
		t.Fatal("cache of removed content kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("recent frame removed")
	}
	c.PutFailed(7, keep, "broken")
	if r, ok := c.Failed(7, keep); !ok || r != "broken" {
		t.Fatal("failure not recorded")
	}
}

// TestRealFFmpeg runs the tools end to end when FFmpeg is installed
// (always in the server image; skipped elsewhere).
func TestRealFFmpeg(t *testing.T) {
	tools := Detect()
	if !tools.Available() {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	gen := exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=12", "-c:v", "libx264", "-g", "60", "-c:a", "aac", "-shortest", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("make test video: %v %s", err, out)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx := context.Background()
	info, err := tools.Probe(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 640 || info.Height != 360 || !info.HasAudio() || info.Duration < 11.5 {
		t.Fatalf("probe = %+v", info)
	}
	img, err := tools.Frame(ctx, f, 5, 72, 6)
	if err != nil || len(img) < 100 || img[0] != 0xFF || img[1] != 0xD8 {
		t.Fatalf("frame: %v (%d bytes)", err, len(img))
	}
	cases := []struct {
		name    string
		clip    Clip
		quality string
	}{
		{"copy", Clip{Start: 2, End: 6, Volume: 1}, QualityCopy},
		{"accurate", Clip{Start: 2.5, End: 5.5, Volume: 1}, QualityHigh},
		{"edited", Clip{Start: 1, End: 7, Speed: 2, PreservePitch: true, Volume: 0.5, Freezes: []Freeze{{At: 3, Duration: 1}}, Crop: &Rect{0.2, 0.1, 0.5, 0.8}}, QualityBalanced},
		{"muted slow", Clip{Start: 1, End: 2, Speed: 0.5, Muted: true}, QualitySmall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.clip
			if err := c.Normalize(info); err != nil {
				t.Fatal(err)
			}
			mode := Mode(&c, tc.quality)
			format, _ := Container(mode, "src.mp4", info)
			out := filepath.Join(dir, tc.name+".part")
			var last float64
			if err := tools.Export(ctx, f, ExportArgs(&c, mode, info, format, out), func(s float64) { last = s }); err != nil {
				t.Fatal(err)
			}
			want := c.OutputDuration()
			if mode == QualityCopy {
				want = c.End - c.Start
			}
			got, err := tools.Verify(ctx, out, want, !c.Muted)
			if err != nil {
				t.Fatal(err)
			}
			if mode != QualityCopy && (got.Duration < want-0.2 || got.Duration > want+0.2) {
				t.Fatalf("duration %.2f, want %.2f", got.Duration, want)
			}
			if c.Crop != nil && (got.Width != 320 || got.Height != 288) {
				t.Fatalf("cropped to %dx%d", got.Width, got.Height)
			}
			if c.Muted && got.HasAudio() {
				t.Fatal("muted clip has audio")
			}
			if last <= 0 {
				t.Fatal("no progress reported")
			}
		})
	}
	// A damaged file fails cleanly.
	bad := filepath.Join(dir, "bad.mp4")
	_ = os.WriteFile(bad, []byte("not a video at all"), 0o644)
	bf, _ := os.Open(bad)
	defer bf.Close()
	if _, err := tools.Probe(ctx, bf); err == nil {
		t.Fatal("damaged file probed as a video")
	}
}
