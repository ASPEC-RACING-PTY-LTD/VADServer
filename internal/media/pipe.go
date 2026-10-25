package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// RunOptions tune a decoding run of FFmpeg.
type RunOptions struct {
	// Threads limits FFmpeg's threads (0: FFmpeg decides).
	Threads int
	// HWAccel asks FFmpeg to decode with hardware acceleration ("auto",
	// "cuda", "vaapi", "qsv"; empty decodes on the CPU). FFmpeg falls back
	// to software decoding when the device is not usable.
	HWAccel string
	// Nice lowers the child's scheduling priority (Linux; 0..19).
	Nice int
}

// Pipe runs FFmpeg on in with args ("{input}" stands for the input file)
// and hands its standard output to read. Analysis uses it to decode audio
// and frames as raw streams without temp files. When read returns early
// the process is stopped.
func (t Tools) Pipe(ctx context.Context, in *os.File, args []string, o RunOptions, read func(io.Reader) error) error {
	if t.FFmpeg == "" {
		return ErrUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, input := command(ctx, t.FFmpeg, in)
	cmd.Args = append(cmd.Args, "-hide_banner", "-nostdin", "-loglevel", "error")
	if o.Threads > 0 {
		cmd.Args = append(cmd.Args, "-threads", strconv.Itoa(o.Threads))
	}
	if o.HWAccel != "" {
		cmd.Args = append(cmd.Args, "-hwaccel", o.HWAccel)
	}
	for _, a := range args {
		if a == "{input}" {
			a = input
		}
		cmd.Args = append(cmd.Args, a)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errOut stderrTail
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		return err
	}
	lowerPriority(cmd, o.Nice)
	rerr := read(stdout)
	if rerr != nil {
		cancel()
	}
	_, _ = io.Copy(io.Discard, stdout)
	werr := cmd.Wait()
	switch {
	case rerr != nil:
		return rerr
	case werr != nil && ctx.Err() != nil:
		return ctx.Err()
	case werr != nil:
		return fmt.Errorf("ffmpeg failed: %s", firstNonEmpty(errOut.String(), werr.Error()))
	}
	return nil
}

// lowerPriority renices a started child (best effort).
func lowerPriority(cmd *exec.Cmd, nice int) {
	if nice <= 0 || cmd.Process == nil {
		return
	}
	setNice(cmd.Process.Pid, min(nice, 19))
}

// KeyframeAt reports whether the video has a keyframe within tol seconds
// of at, so a stream copy starting there is frame-accurate.
func (t Tools) KeyframeAt(ctx context.Context, in *os.File, at, tol float64) (bool, error) {
	if t.FFprobe == "" {
		return false, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd, input := command(ctx, t.FFprobe, in)
	from := math.Max(at-3, 0)
	cmd.Args = append(cmd.Args, "-v", "error", "-select_streams", "v:0", "-skip_frame", "nokey", "-read_intervals", fmt.Sprintf("%s%%%s", seconds(from), seconds(at+1)),
		"-show_entries", "frame=best_effort_timestamp_time", "-of", "csv=p=0", input)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false, err
	}
	for _, line := range strings.Split(out.String(), "\n") {
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ",")), 64)
		if err == nil && math.Abs(v-at) <= tol {
			return true, nil
		}
	}
	return false, nil
}
