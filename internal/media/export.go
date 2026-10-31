package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Rect is a crop rectangle as fractions (0..1) of the displayed frame.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Freeze holds the frame at At (source seconds) for Duration seconds.
type Freeze struct {
	At       float64 `json:"at"`
	Duration float64 `json:"duration"`
}

// Clip is one selection of a source recording and its adjustments.
type Clip struct {
	Start         float64  `json:"start"` // source seconds
	End           float64  `json:"end"`
	Speed         float64  `json:"speed"` // 1 = normal
	PreservePitch bool     `json:"preserve_pitch"`
	Volume        float64  `json:"volume"` // 1 = unchanged, 0..2
	Muted         bool     `json:"muted"`
	Freezes       []Freeze `json:"freezes,omitempty"`
	Crop          *Rect    `json:"crop,omitempty"`
}

// Quality presets. Copy keeps the source streams untouched (cuts land on
// keyframes); the others re-encode to H.264/AAC MP4 with frame-accurate cuts.
const (
	QualityCopy     = "copy"
	QualityHigh     = "high"
	QualityBalanced = "balanced"
	QualitySmall    = "small"
)

// ValidQuality reports whether q is a known preset.
func ValidQuality(q string) bool {
	return q == QualityCopy || q == QualityHigh || q == QualityBalanced || q == QualitySmall
}

const (
	MinSpeed       = 0.25
	MaxSpeed       = 4.0
	MaxFreeze      = 30.0
	MaxFreezes     = 8
	MinClipSeconds = 0.1
)

// UnmarshalJSON defaults speed and volume to 1 when they are left out.
func (c *Clip) UnmarshalJSON(b []byte) error {
	type plain Clip
	p := plain{Speed: 1, Volume: 1}
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Clip(p)
	return nil
}

// Normalize fills defaults and checks the clip against the source.
func (c *Clip) Normalize(info *Info) error {
	if c.Speed == 0 {
		c.Speed = 1
	}
	if c.Volume < 0 || math.IsNaN(c.Volume) {
		c.Volume = 1
	}
	if c.Volume == 0 {
		c.Muted = true // silent: drop the audio rather than encode silence
	}
	if c.Speed < MinSpeed || c.Speed > MaxSpeed || math.IsNaN(c.Speed) {
		return fmt.Errorf("speed must be between %gx and %gx", MinSpeed, MaxSpeed)
	}
	if c.Volume > 2 {
		return errors.New("volume can be at most 200%")
	}
	if math.IsNaN(c.Start) || math.IsNaN(c.End) || c.Start < 0 || c.End-c.Start < MinClipSeconds {
		return errors.New("the selection is empty")
	}
	if info.Duration > 0 {
		if c.Start >= info.Duration {
			return errors.New("the selection starts after the end of the video")
		}
		c.End = math.Min(c.End, info.Duration)
	}
	if len(c.Freezes) > MaxFreezes {
		return fmt.Errorf("at most %d freeze frames per clip", MaxFreezes)
	}
	for _, f := range c.Freezes {
		if f.Duration <= 0 || f.Duration > MaxFreeze || f.At < c.Start || f.At > c.End {
			return fmt.Errorf("freeze frames must be inside the selection and last up to %g seconds", MaxFreeze)
		}
	}
	if r := c.Crop; r != nil {
		if r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 || r.X+r.W > 1.0001 || r.Y+r.H > 1.0001 {
			return errors.New("the crop must be inside the frame")
		}
		if r.X == 0 && r.Y == 0 && r.W >= 0.9999 && r.H >= 0.9999 {
			c.Crop = nil // the whole frame
		}
	}
	return nil
}

// NeedsEncode reports whether the clip's adjustments require re-encoding.
func (c *Clip) NeedsEncode() bool {
	return c.Speed != 1 || (!c.Muted && c.Volume != 1) || len(c.Freezes) > 0 || c.Crop != nil
}

// OutputDuration is the clip's length after speed and freeze frames.
func (c *Clip) OutputDuration() float64 {
	d := (c.End - c.Start) / c.Speed
	for _, f := range c.Freezes {
		d += f.Duration
	}
	return d
}

// Mode is how a clip is produced.
func Mode(c *Clip, quality string) string {
	if quality == QualityCopy && !c.NeedsEncode() {
		return QualityCopy
	}
	if quality == QualityCopy {
		return QualityHigh // the adjustments need an encode; keep the quality
	}
	return quality
}

// Container picks the output format (FFmpeg muxer) and file extension. A
// copy keeps the source's container where the codecs allow it; encodes
// are MP4.
func Container(mode, sourceName string, info *Info) (format, ext string) {
	if mode != QualityCopy {
		return "mp4", ".mp4"
	}
	switch strings.ToLower(path.Ext(sourceName)) {
	case ".mkv":
		return "matroska", ".mkv"
	case ".webm":
		return "webm", ".webm"
	case ".mov":
		return "mov", ".mov"
	}
	switch info.VideoCodec {
	case "h264", "hevc", "av1", "mpeg4":
		return "mp4", ".mp4"
	}
	return "matroska", ".mkv"
}

// crf and the share of the source bitrate an encode may use.
func encodeSettings(quality string) (crf int, capShare float64) {
	switch quality {
	case QualityBalanced:
		return 22, 0.8
	case QualitySmall:
		return 27, 0.5
	default:
		return 18, 1.0
	}
}

// EstimateSize guesses the output size in bytes.
func EstimateSize(c *Clip, quality string, info *Info) int64 {
	mode := Mode(c, quality)
	dur := c.OutputDuration()
	vbr := float64(info.VideoBitrate)
	if vbr <= 0 {
		vbr = 8e6
	}
	abr := float64(info.AudioBitrate)
	if abr <= 0 && info.HasAudio() {
		abr = 160e3
	}
	if c.Muted || !info.HasAudio() {
		abr = 0
	}
	if mode == QualityCopy {
		if !c.Muted {
			abr *= float64(max(1, info.AudioTracks))
		}
		return int64((vbr + abr) / 8 * (c.End - c.Start) * 1.01)
	}
	_, share := encodeSettings(mode)
	area := 1.0
	if c.Crop != nil {
		area = c.Crop.W * c.Crop.H
	}
	v := vbr * share * math.Max(area, 0.15)
	if abr > 0 {
		abr = math.Min(abr, 192e3)
	}
	// Freeze frames compress to almost nothing.
	moving := (c.End - c.Start) / c.Speed
	return int64(((v+abr)*moving + abr*(dur-moving)) / 8 * 1.02)
}

func even(v float64) int {
	n := int(math.Floor(v/2)) * 2
	return max(n, 2)
}

// atempo builds a pitch-preserving tempo chain (each atempo stage 0.5..2).
func atempo(speed float64) []string {
	var out []string
	for speed > 2 {
		out = append(out, "atempo=2")
		speed /= 2
	}
	for speed < 0.5 {
		out = append(out, "atempo=0.5")
		speed /= 0.5
	}
	if math.Abs(speed-1) > 1e-6 {
		out = append(out, "atempo="+strconv.FormatFloat(speed, 'f', -1, 64))
	}
	return out
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

// ExportArgs builds the FFmpeg arguments for a clip. "{input}" stands for
// the input file; output goes to out in the given muxer format.
func ExportArgs(c *Clip, mode string, info *Info, format, out string) []string {
	dur := c.End - c.Start
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-progress", "pipe:1", "-nostats"}
	if mode == QualityCopy {
		// Input seeking with stream copy starts at the keyframe before Start.
		args = append(args, "-ss", seconds(c.Start), "-t", seconds(dur), "-i", "{input}", "-map", "0:v:0")
		if !c.Muted {
			args = append(args, "-map", "0:a?")
		}
		args = append(args, "-c", "copy", "-avoid_negative_ts", "make_zero", "-map_metadata", "0", "-map_chapters", "-1")
		if format == "mp4" || format == "mov" {
			args = append(args, "-movflags", "+faststart")
		}
		return append(args, "-f", format, out)
	}
	// Accurate seek: decoding starts at the keyframe before Start and frames
	// before Start are dropped. Times below are relative to Start.
	args = append(args, "-ss", seconds(c.Start), "-t", seconds(dur), "-i", "{input}")
	audio := info.HasAudio() && !c.Muted
	// Cut points at freeze frames.
	freezes := append([]Freeze(nil), c.Freezes...)
	sort.Slice(freezes, func(i, j int) bool { return freezes[i].At < freezes[j].At })
	type seg struct{ a, b, hold float64 }
	var segs []seg
	from := 0.0
	for _, f := range freezes {
		at := math.Min(math.Max(f.At-c.Start, 0), dur)
		segs = append(segs, seg{from, at, f.Duration})
		from = at
	}
	segs = append(segs, seg{from, dur, 0})
	// Drop empty source pieces that carry no freeze.
	kept := segs[:0]
	for _, s := range segs {
		if s.b-s.a > 0.001 || s.hold > 0 {
			kept = append(kept, s)
		}
	}
	segs = kept
	var g []string
	vin := "[0:v:0]"
	pre := []string{}
	if c.Crop != nil {
		w, h := even(c.Crop.W*float64(info.Width)), even(c.Crop.H*float64(info.Height))
		x, y := int(c.Crop.X*float64(info.Width)), int(c.Crop.Y*float64(info.Height))
		x, y = min(max(x, 0), info.Width-w), min(max(y, 0), info.Height-h)
		pre = append(pre, fmt.Sprintf("crop=%d:%d:%d:%d", w, h, x, y))
	}
	pre = append(pre, "setsar=1")
	n := len(segs)
	if n == 1 {
		g = append(g, vin+strings.Join(pre, ",")+"[vs0]")
	} else {
		labels := ""
		for i := range segs {
			labels += fmt.Sprintf("[vs%d]", i)
		}
		g = append(g, vin+strings.Join(pre, ",")+fmt.Sprintf(",split=%d", n)+labels)
	}
	if audio {
		if n == 1 {
			g = append(g, "[0:a:0]anull[as0]")
		} else {
			labels := ""
			for i := range segs {
				labels += fmt.Sprintf("[as%d]", i)
			}
			g = append(g, fmt.Sprintf("[0:a:0]asplit=%d", n)+labels)
		}
	}
	concat := ""
	for i, s := range segs {
		v := []string{fmt.Sprintf("trim=start=%s:end=%s", num(s.a), num(s.b)), "setpts=PTS-STARTPTS"}
		if c.Speed != 1 {
			v = append(v, "setpts=PTS/"+num(c.Speed))
			if c.Speed > 1 {
				v = append(v, "fps="+num(info.FPS))
			}
		}
		if s.hold > 0 {
			v = append(v, "tpad=stop_mode=clone:stop_duration="+num(s.hold))
		}
		g = append(g, fmt.Sprintf("[vs%d]%s[v%d]", i, strings.Join(v, ","), i))
		concat += fmt.Sprintf("[v%d]", i)
		if audio {
			a := []string{fmt.Sprintf("atrim=start=%s:end=%s", num(s.a), num(s.b)), "asetpts=PTS-STARTPTS"}
			if c.Speed != 1 {
				if c.PreservePitch {
					a = append(a, atempo(c.Speed)...)
				} else {
					sr := info.SampleRate
					if sr <= 0 {
						sr = 48000
					}
					a = append(a, fmt.Sprintf("asetrate=%s,aresample=%d", num(float64(sr)*c.Speed), sr))
				}
			}
			if c.Volume != 1 {
				a = append(a, "volume="+num(c.Volume))
			}
			if s.hold > 0 {
				a = append(a, "apad=pad_dur="+num(s.hold))
			}
			g = append(g, fmt.Sprintf("[as%d]%s[a%d]", i, strings.Join(a, ","), i))
			concat += fmt.Sprintf("[a%d]", i)
		}
	}
	if audio {
		g = append(g, fmt.Sprintf("%sconcat=n=%d:v=1:a=1[vout][aout]", concat, n))
	} else {
		g = append(g, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[vout]", concat, n))
	}
	args = append(args, "-filter_complex", strings.Join(g, ";"), "-map", "[vout]")
	crf, share := encodeSettings(mode)
	args = append(args, "-c:v", "libx264", "-preset", "medium", "-crf", strconv.Itoa(crf), "-pix_fmt", "yuv420p")
	// Never more than the source's bitrate (scaled to the cropped area), so
	// a clip does not grow beyond its share of the original.
	if info.VideoBitrate > 0 {
		area := 1.0
		if c.Crop != nil {
			area = math.Max(c.Crop.W*c.Crop.H, 0.15)
		}
		capRate := int64(float64(info.VideoBitrate) * share * area * math.Max(1, math.Min(c.Speed, 2)))
		capRate = max(capRate, 500_000)
		args = append(args, "-maxrate", strconv.FormatInt(capRate, 10), "-bufsize", strconv.FormatInt(capRate*2, 10))
	}
	if audio {
		abr := info.AudioBitrate
		if abr <= 0 || abr > 192_000 {
			abr = 192_000
		}
		abr = max(abr, 96_000)
		args = append(args, "-map", "[aout]", "-c:a", "aac", "-b:a", strconv.FormatInt(abr, 10))
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-map_metadata", "0", "-map_chapters", "-1", "-movflags", "+faststart", "-f", format, out)
	return args
}

// Export runs the clip export from in to out (a new file).
func (t Tools) Export(ctx context.Context, in Input, args []string, progress Progress) error {
	if t.FFmpeg == "" {
		return ErrUnavailable
	}
	cmd, input := command(ctx, t.FFmpeg, in)
	for _, a := range args {
		if a == "{input}" {
			a = input
		}
		cmd.Args = append(cmd.Args, a)
	}
	return t.run(ctx, cmd, progress)
}

// Verify probes an exported file and checks it against the expected
// duration (seconds).
func (t Tools) Verify(ctx context.Context, file string, want float64, wantAudio bool) (*Info, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := t.Probe(ctx, File(f))
	if err != nil {
		return nil, fmt.Errorf("the exported file is not playable: %w", err)
	}
	if info.Duration <= 0 {
		return nil, errors.New("the exported file has no length")
	}
	// Stream copies start at a keyframe, so they may run a little longer.
	if tol := math.Max(1.5, want*0.05); math.Abs(info.Duration-want) > tol+10 || info.Duration < want-tol {
		return nil, fmt.Errorf("the exported file is %.1fs long, expected %.1fs", info.Duration, want)
	}
	if wantAudio && !info.HasAudio() {
		return nil, errors.New("the exported file lost its audio")
	}
	return info, nil
}
