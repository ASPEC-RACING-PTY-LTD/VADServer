package smart

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
)

// Decoding for analysis, with FFmpeg reading the stored file.

func secs(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// DecodeAudio writes the first audio track as 16 kHz mono s16le to w.
func DecodeAudio(ctx context.Context, t media.Tools, in media.Input, o media.RunOptions, w io.Writer) error {
	args := []string{"-i", "{input}", "-map", "0:a:0", "-vn", "-sn", "-dn", "-ac", "1", "-ar", strconv.Itoa(SampleRate), "-f", "s16le", "-c:a", "pcm_s16le", "pipe:1"}
	return t.Pipe(ctx, in, args, o, func(r io.Reader) error {
		_, err := io.Copy(w, r)
		return err
	})
}

// Small gray frames for motion and scene measurements.
const (
	dspW, dspH   = 64, 36
	dspFrameRate = 4
)

// MeasureVideo decodes small gray frames and measures them.
func MeasureVideo(ctx context.Context, t media.Tools, in media.Input, o media.RunOptions) (*VideoDSP, error) {
	m := &FrameMeter{W: dspW, H: dspH, Rate: dspFrameRate}
	args := []string{"-i", "{input}", "-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", fmt.Sprintf("fps=%d,scale=%d:%d:flags=area,format=gray", dspFrameRate, dspW, dspH), "-f", "rawvideo", "pipe:1"}
	err := t.Pipe(ctx, in, args, o, func(r io.Reader) error {
		_, err := m.ReadFrom(bufio.NewReaderSize(r, 1<<16))
		return err
	})
	if err != nil {
		return nil, err
	}
	return m.Result(), nil
}

// ExtractFrames decodes frames from..to (seconds) at fps, width pixels
// wide, as JPEG, and hands each to fn with its time.
func ExtractFrames(ctx context.Context, t media.Tools, in media.Input, o media.RunOptions, from, to, fps float64, width int, fn func(at float64, jpeg []byte) error) error {
	args := []string{"-ss", secs(from), "-t", secs(to - from), "-i", "{input}", "-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", fmt.Sprintf("fps=%s,scale='min(%d,iw)':-2:flags=bilinear", strconv.FormatFloat(fps, 'f', -1, 64), width),
		"-f", "image2pipe", "-c:v", "mjpeg", "-q:v", "4", "pipe:1"}
	i := 0
	return t.Pipe(ctx, in, args, o, func(r io.Reader) error {
		return splitJPEG(bufio.NewReaderSize(r, 1<<18), func(b []byte) error {
			at := from + float64(i)/fps
			i++
			return fn(at, b)
		})
	})
}

// splitJPEG cuts a stream of concatenated JPEG images at their end
// markers (FFmpeg's encoder writes no embedded thumbnails, and entropy
// data never holds an unstuffed 0xFFD9).
func splitJPEG(r *bufio.Reader, fn func([]byte) error) error {
	var cur bytes.Buffer
	prevFF := false
	for {
		b, err := r.ReadByte()
		if err == io.EOF {
			if cur.Len() > 0 {
				return errors.New("truncated frame")
			}
			return nil
		}
		if err != nil {
			return err
		}
		cur.WriteByte(b)
		if prevFF && b == 0xD9 {
			img := append([]byte(nil), cur.Bytes()...)
			cur.Reset()
			if err := fn(img); err != nil {
				return err
			}
			prevFF = false
			continue
		}
		prevFF = b == 0xFF
		if cur.Len() > 16<<20 {
			return errors.New("frame too large")
		}
	}
}

// WAVHeader is the header of a 16 kHz mono s16le WAV with n data bytes.
func WAVHeader(n int) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+n))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 1) // mono
	binary.LittleEndian.PutUint32(h[24:], SampleRate)
	binary.LittleEndian.PutUint32(h[28:], SampleRate*2)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(n))
	return h
}
