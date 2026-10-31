// Package media runs FFmpeg and FFprobe for video thumbnails, timeline
// frames, stream information and clip exports.
//
// Inputs are open files (stored blobs, possibly extracted copies of
// compressed ones). On Linux the file is handed to the child process as
// fd 3 and read through /dev/fd/3, so nothing else can swap or remove the
// file underneath it; elsewhere (development) its path is used.
package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrUnavailable: FFmpeg or FFprobe is not installed.
var ErrUnavailable = errors.New("video processing is not available on this server (FFmpeg is not installed)")

// Tools locates the FFmpeg binaries.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Detect finds ffmpeg and ffprobe: VAULTDOCK_FFMPEG / VAULTDOCK_FFPROBE,
// then PATH. Missing tools leave the field empty.
func Detect() Tools {
	find := func(env, name string) string {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
			return ""
		}
		p, _ := exec.LookPath(name)
		return p
	}
	return Tools{FFmpeg: find("VAULTDOCK_FFMPEG", "ffmpeg"), FFprobe: find("VAULTDOCK_FFPROBE", "ffprobe")}
}

// Available reports whether both tools were found.
func (t Tools) Available() bool { return t.FFmpeg != "" && t.FFprobe != "" }

// Version returns the first line of "ffmpeg -version".
func (t Tools) Version(ctx context.Context) string {
	if t.FFmpeg == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, t.FFmpeg, "-hide_banner", "-version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}

// Input is what FFmpeg reads: an open local file (File) or a URL it reads
// with HTTP range requests (URL; content relayed from a storage node).
type Input interface {
	attach(cmd *exec.Cmd) string
}

type fileInput struct{ f *os.File }

// File reads an open file (passed to the child as a descriptor on Linux, so
// a path that changes meanwhile does not matter).
func File(f *os.File) Input { return fileInput{f} }

func (i fileInput) attach(cmd *exec.Cmd) string {
	if runtime.GOOS == "linux" {
		cmd.ExtraFiles = []*os.File{i.f}
		return "/dev/fd/3"
	}
	return i.f.Name()
}

// URL reads content over HTTP (seekable with range requests).
type URL string

func (u URL) attach(*exec.Cmd) string { return string(u) }

// command builds a child process reading in. The returned input path goes
// where the command expects the input file.
func command(ctx context.Context, bin string, in Input) (*exec.Cmd, string) {
	cmd := exec.CommandContext(ctx, bin)
	cmd.WaitDelay = 5 * time.Second
	return cmd, in.attach(cmd)
}

// stderrTail keeps the end of a process's error output for messages.
type stderrTail struct{ buf []byte }

func (s *stderrTail) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	if len(s.buf) > 8192 {
		s.buf = s.buf[len(s.buf)-8192:]
	}
	return len(p), nil
}

// String returns the last meaningful lines.
func (s *stderrTail) String() string {
	lines := strings.Split(strings.TrimSpace(string(s.buf)), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}

// Info describes a video file.
type Info struct {
	Duration     float64 `json:"duration"` // seconds
	Width        int     `json:"width"`    // as displayed (rotation applied)
	Height       int     `json:"height"`
	FPS          float64 `json:"fps"`
	VideoCodec   string  `json:"video_codec"`
	AudioCodec   string  `json:"audio_codec,omitempty"`
	AudioTracks  int     `json:"audio_tracks"`
	SampleRate   int     `json:"sample_rate,omitempty"`
	Bitrate      int64   `json:"bitrate"`       // whole file, bits/s
	VideoBitrate int64   `json:"video_bitrate"` // bits/s, estimated when the container does not say
	AudioBitrate int64   `json:"audio_bitrate"`
	Format       string  `json:"format"`
	Size         int64   `json:"size"`
}

// HasAudio reports whether the file has an audio track.
func (i *Info) HasAudio() bool { return i.AudioTracks > 0 }

type probeOut struct {
	Streams []struct {
		CodecType    string            `json:"codec_type"`
		CodecName    string            `json:"codec_name"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		AvgFrameRate string            `json:"avg_frame_rate"`
		RFrameRate   string            `json:"r_frame_rate"`
		SampleRate   string            `json:"sample_rate"`
		BitRate      string            `json:"bit_rate"`
		Tags         map[string]string `json:"tags"`
		SideData     []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
		Disposition map[string]int `json:"disposition"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
		Size       string `json:"size"`
	} `json:"format"`
}

func rate(s string) float64 {
	a, b, ok := strings.Cut(s, "/")
	n, _ := strconv.ParseFloat(a, 64)
	if !ok {
		return n
	}
	d, _ := strconv.ParseFloat(b, 64)
	if d == 0 {
		return 0
	}
	return n / d
}

// Probe reads stream information.
func (t Tools) Probe(ctx context.Context, in Input) (*Info, error) {
	if t.FFprobe == "" {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd, input := command(ctx, t.FFprobe, in)
	cmd.Args = append(cmd.Args, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", input)
	var out bytes.Buffer
	var errOut stderrTail
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("reading the video took too long")
		}
		return nil, fmt.Errorf("not a readable video: %s", firstNonEmpty(errOut.String(), err.Error()))
	}
	return parseProbe(out.Bytes())
}

func parseProbe(b []byte) (*Info, error) {
	var p probeOut
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("unreadable stream information: %w", err)
	}
	info := &Info{Format: p.Format.FormatName}
	info.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	info.Bitrate, _ = strconv.ParseInt(p.Format.BitRate, 10, 64)
	info.Size, _ = strconv.ParseInt(p.Format.Size, 10, 64)
	video := false
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			if video || s.Disposition["attached_pic"] == 1 {
				continue // cover art and further video tracks
			}
			video = true
			info.VideoCodec = s.CodecName
			info.Width, info.Height = s.Width, s.Height
			info.FPS = rate(s.AvgFrameRate)
			if info.FPS <= 0 || info.FPS > 1000 {
				info.FPS = rate(s.RFrameRate)
			}
			info.VideoBitrate, _ = strconv.ParseInt(s.BitRate, 10, 64)
			rot := 0.0
			if v, ok := s.Tags["rotate"]; ok {
				rot, _ = strconv.ParseFloat(v, 64)
			}
			for _, sd := range s.SideData {
				if sd.Rotation != 0 {
					rot = sd.Rotation
				}
			}
			if r := int(math.Abs(math.Round(rot))) % 180; r == 90 {
				info.Width, info.Height = info.Height, info.Width
			}
		case "audio":
			info.AudioTracks++
			if info.AudioTracks == 1 {
				info.AudioCodec = s.CodecName
				info.SampleRate, _ = strconv.Atoi(s.SampleRate)
				info.AudioBitrate, _ = strconv.ParseInt(s.BitRate, 10, 64)
			}
		}
	}
	if !video || info.Width <= 0 || info.Height <= 0 {
		return nil, errors.New("the file has no video track")
	}
	if info.FPS <= 0 || info.FPS > 1000 {
		info.FPS = 30
	}
	if info.Bitrate <= 0 && info.Duration > 0 && info.Size > 0 {
		info.Bitrate = int64(float64(info.Size*8) / info.Duration)
	}
	if info.VideoBitrate <= 0 && info.Bitrate > 0 {
		info.VideoBitrate = max(info.Bitrate-info.AudioBitrate*int64(max(1, info.AudioTracks)), info.Bitrate/2)
	}
	return info, nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// seconds formats a time for FFmpeg arguments.
func seconds(v float64) string { return strconv.FormatFloat(math.Max(0, v), 'f', 3, 64) }

// Frame returns one JPEG frame at t seconds, height pixels high (0 keeps
// the size; width follows the aspect ratio).
func (t Tools) Frame(ctx context.Context, in Input, at float64, height int, quality int) ([]byte, error) {
	if t.FFmpeg == "" {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd, input := command(ctx, t.FFmpeg, in)
	// -ss before -i seeks to the nearest keyframe and decodes up to the exact
	// time, so long recordings are not read from the start.
	cmd.Args = append(cmd.Args, "-hide_banner", "-nostdin", "-loglevel", "error", "-ss", seconds(at), "-i", input, "-frames:v", "1", "-an", "-sn", "-dn")
	if height > 0 {
		cmd.Args = append(cmd.Args, "-vf", fmt.Sprintf("scale=-2:%d:flags=bilinear", height))
	}
	cmd.Args = append(cmd.Args, "-q:v", strconv.Itoa(quality), "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	var out bytes.Buffer
	var errOut stderrTail
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("extracting the frame took too long")
		}
		return nil, fmt.Errorf("no frame: %s", firstNonEmpty(errOut.String(), err.Error()))
	}
	if out.Len() == 0 {
		return nil, errors.New("no frame at that time")
	}
	return out.Bytes(), nil
}

// Progress receives the output time written so far, in seconds.
type Progress func(outSeconds float64)

// Run runs FFmpeg with args (built by ExportArgs, input placeholder
// already replaced) and reports progress.
func (t Tools) run(ctx context.Context, cmd *exec.Cmd, progress Progress) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errOut stderrTail
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && k == "out_time_us" && progress != nil {
			if us, err := strconv.ParseInt(v, 10, 64); err == nil && us >= 0 {
				progress(float64(us) / 1e6)
			}
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg failed: %s", firstNonEmpty(errOut.String(), err.Error()))
	}
	return nil
}
