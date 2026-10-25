package smart

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
)

// Settings tune analysis (administrator settings).
type Settings struct {
	Threads      int     // FFmpeg threads per decode (0: FFmpeg decides)
	HWAccel      string  // FFmpeg decoding acceleration ("", auto, cuda, vaapi, qsv)
	Nice         int     // child process priority (0..19)
	FrameRate    float64 // frames per second sent to vision models
	FrameWidth   int     // their width in pixels
	OCREvery     float64 // seconds between frames read with OCR
	ChunkSeconds float64 // audio sent to the worker per request
	Language     string  // speech language ("" detects it)
}

func (s Settings) withDefaults() Settings {
	if s.FrameRate <= 0 {
		s.FrameRate = 1
	}
	if s.FrameWidth <= 0 {
		s.FrameWidth = 640
	}
	if s.OCREvery <= 0 {
		s.OCREvery = 2
	}
	if s.ChunkSeconds <= 0 {
		s.ChunkSeconds = 600
	}
	return s
}

func (s Settings) run() media.RunOptions {
	return media.RunOptions{Threads: s.Threads, HWAccel: s.HWAccel, Nice: s.Nice}
}

// Analyzer measures recordings and caches what it measured:
//
//	<dir>/<user>/<sha[0:2]>/<sha>/<detector>.v<version>[.<params>].json.gz
//	                              <...>.partial.json.gz   checkpoint of a long run
//
// Files are written atomically, private to the server (0640 in 0750
// directories). The worker gets decoded audio and frames, never paths.
type Analyzer struct {
	Tools  media.Tools
	Worker func() *Worker // current worker (settings may change)
	Dir    string         // cache
	Temp   string         // decoded audio while a recording is analysed
	Opts   func() Settings

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Source is a recording to analyse.
type Source struct {
	UserID int64
	SHA256 string
	Info   *media.Info
	Open   func() (*os.File, error)
}

func (a *Analyzer) settings() Settings {
	if a.Opts == nil {
		return Settings{}.withDefaults()
	}
	return a.Opts().withDefaults()
}

func (a *Analyzer) dir(userID int64, sha string) string {
	return filepath.Join(a.Dir, strconv.FormatInt(userID, 10), sha[:2], sha)
}

// paramKey: settings that change what a detector measures.
func paramKey(d string, s Settings) string {
	switch d {
	case DFrames, DFaces:
		return fmt.Sprintf("f%gw%d", s.FrameRate, s.FrameWidth)
	case DOCR:
		return fmt.Sprintf("o%gw%d", s.OCREvery, s.FrameWidth)
	case DTranscript, DEmotion:
		if s.Language != "" {
			return "l" + s.Language
		}
	}
	return ""
}

func (a *Analyzer) file(src Source, d string, s Settings) string {
	name := fmt.Sprintf("%s.v%d", d, DetectorVersion[d])
	if k := paramKey(d, s); k != "" {
		name += "." + k
	}
	return filepath.Join(a.dir(src.UserID, src.SHA256), name+".json.gz")
}

// Has reports whether a detector's signals are cached.
func (a *Analyzer) Has(src Source, d string) bool {
	_, err := os.Stat(a.file(src, d, a.settings()))
	return err == nil
}

func (a *Analyzer) lock(key string) func() {
	a.mu.Lock()
	if a.locks == nil {
		a.locks = map[string]*sync.Mutex{}
	}
	l, ok := a.locks[key]
	if !ok {
		l = &sync.Mutex{}
		a.locks[key] = l
	}
	a.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func writeGz(p string, v any) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp := p + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	err = json.NewEncoder(zw).Encode(v)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func readGz(p string, v any) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return err
	}
	defer zr.Close()
	return json.NewDecoder(zr).Decode(v)
}

// Progress reports a stage and its share done (0..1).
type Progress func(stage string, done float64)

// Ensure measures the detectors that are not cached yet. It returns the
// detectors that could not be measured with why; err is a failure of the
// whole recording (unreadable, cancelled).
func (a *Analyzer) Ensure(ctx context.Context, src Source, detectors []string, progress Progress) (map[string]error, error) {
	if progress == nil {
		progress = func(string, float64) {}
	}
	s := a.settings()
	unlock := a.lock(fmt.Sprintf("%d/%s", src.UserID, src.SHA256))
	defer unlock()
	var need []string
	for _, d := range detectors {
		if _, err := os.Stat(a.file(src, d, s)); err != nil {
			need = append(need, d)
		}
	}
	failed := map[string]error{}
	if len(need) == 0 {
		return failed, nil
	}
	var w *Worker
	if a.Worker != nil {
		w = a.Worker()
	}
	var audio, workerAudio, vision []string
	for _, d := range need {
		switch {
		case WorkerDetectors[d] && w == nil:
			failed[d] = ErrNoWorker
		case d == DAudioDSP:
			audio = append(audio, d)
		case AudioDetectors[d]:
			workerAudio = append(workerAudio, d)
		case d == DVideoDSP:
			vision = append(vision, d)
		default:
			vision = append(vision, d)
		}
	}
	if !src.Info.HasAudio() {
		for _, d := range append(audio, workerAudio...) {
			// No audio track: measured as empty, not as failed.
			if err := writeGz(a.file(src, d, s), emptyFor(d)); err != nil {
				return failed, err
			}
		}
		audio, workerAudio = nil, nil
	}
	if len(audio)+len(workerAudio) > 0 {
		if err := a.audio(ctx, src, s, w, len(audio) > 0, workerAudio, failed, progress); err != nil {
			return failed, err
		}
	}
	for _, d := range vision {
		if d == DVideoDSP {
			progress("motion", 0)
			f, err := src.Open()
			if err != nil {
				return failed, err
			}
			vd, err := MeasureVideo(ctx, a.Tools, f, s.run())
			f.Close()
			if err != nil {
				if ctx.Err() != nil {
					return failed, ctx.Err()
				}
				return failed, fmt.Errorf("measuring frames: %w", err)
			}
			if err := writeGz(a.file(src, d, s), vd); err != nil {
				return failed, err
			}
		}
	}
	var wv []string
	for _, d := range vision {
		if d != DVideoDSP {
			wv = append(wv, d)
		}
	}
	if len(wv) > 0 {
		if err := a.frames(ctx, src, s, w, wv, failed, progress); err != nil {
			return failed, err
		}
	}
	return failed, nil
}

func emptyFor(d string) any {
	switch d {
	case DAudioDSP:
		return &AudioDSP{}
	case DVAD:
		return []Interval{}
	case DTranscript:
		return &Transcript{}
	case DAudioEvents:
		return &Matrix{}
	case DAudioCLAP:
		return &Vecs{}
	case DEmotion:
		return &Emotions{}
	}
	return struct{}{}
}

var workerAudioTask = map[string]string{DVAD: "vad", DTranscript: "transcript", DAudioEvents: "events", DAudioCLAP: "clap", DEmotion: "emotion"}

// audioPartial is the checkpoint of the worker's audio analysis.
type audioPartial struct {
	Tasks    []string    `json:"tasks"`
	Done     float64     `json:"done"` // seconds analysed
	VAD      []Interval  `json:"vad"`
	Utt      []Utterance `json:"utt"`
	UttEmb   *Vecs       `json:"utt_emb,omitempty"`
	SpkEmb   *Vecs       `json:"spk_emb,omitempty"`
	Language string      `json:"language"`
	Model    string      `json:"model"`
	Events   *Matrix     `json:"events,omitempty"`
	CLAP     *Vecs       `json:"clap,omitempty"`
	Emotion  *Emotions   `json:"emotion,omitempty"`
	Failed   []string    `json:"failed,omitempty"`
}

func (a *Analyzer) audio(ctx context.Context, src Source, s Settings, w *Worker, dsp bool, tasks []string, failed map[string]error, progress Progress) error {
	if err := os.MkdirAll(a.Temp, 0o700); err != nil {
		return err
	}
	pcm, err := os.CreateTemp(a.Temp, "sa-*.pcm")
	if err != nil {
		return err
	}
	defer func() { pcm.Close(); os.Remove(pcm.Name()) }()
	progress("decoding audio", 0)
	f, err := src.Open()
	if err != nil {
		return err
	}
	var meter *AudioMeter
	var sink io.Writer = pcm
	if dsp {
		meter = NewAudioMeter()
		sink = io.MultiWriter(pcm, meter)
	}
	bw := bufio.NewWriterSize(sink, 1<<20)
	err = DecodeAudio(ctx, a.Tools, f, s.run(), bw)
	f.Close()
	if err == nil {
		err = bw.Flush()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("decoding audio: %w", err)
	}
	if dsp {
		if err := writeGz(a.file(src, DAudioDSP, s), meter.Result()); err != nil {
			return err
		}
	}
	if len(tasks) == 0 {
		return nil
	}
	fi, err := pcm.Stat()
	if err != nil {
		return err
	}
	total := float64(fi.Size()) / 2 / SampleRate
	var wt []string
	for _, d := range tasks {
		wt = append(wt, workerAudioTask[d])
	}
	sort.Strings(wt)
	if contains(wt, "transcript") {
		wt = append(wt, "speakers", "embed")
	}
	partPath := filepath.Join(a.dir(src.UserID, src.SHA256), "audio.v1."+strings.Join(wt, "+")+paramKey(DTranscript, s)+".partial.json.gz")
	var part audioPartial
	if readGz(partPath, &part) != nil || strings.Join(part.Tasks, ",") != strings.Join(wt, ",") {
		part = audioPartial{Tasks: wt}
	}
	chunk := s.ChunkSeconds
	// Sound event rows are aligned to whole seconds across chunks.
	chunk = math.Max(math.Round(chunk), 30)
	for part.Done < total-0.05 {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress("listening", part.Done/total)
		from := part.Done
		to := math.Min(from+chunk, total)
		off := int64(from*SampleRate) * 2
		n := int64((to-from)*SampleRate) * 2
		body := io.MultiReader(strings.NewReader(string(WAVHeader(int(n)))), io.NewSectionReader(pcm, off, n))
		res, err := w.Audio(ctx, body, wt, s.Language)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			for _, d := range tasks {
				failed[d] = err
			}
			return nil // what was measured stays in the checkpoint
		}
		if err := part.add(res, from); err != nil {
			for _, d := range tasks {
				failed[d] = err
			}
			return nil
		}
		part.Done = to
		if to < total-0.05 {
			if err := writeGz(partPath, &part); err != nil {
				return err
			}
		}
	}
	progress("listening", 1)
	// Finished: one file per detector.
	for _, d := range tasks {
		var v any
		switch d {
		case DVAD:
			v = nonNilIntervals(part.VAD)
		case DTranscript:
			tr := &Transcript{Language: part.Language, Model: part.Model, Utterances: part.Utt}
			if tr.Utterances == nil {
				tr.Utterances = []Utterance{}
			}
			if part.UttEmb != nil && len(part.UttEmb.Data) == len(part.Utt) {
				tr.Embeddings = part.UttEmb
			}
			if part.SpkEmb != nil && len(part.SpkEmb.Data) == len(part.Utt) {
				tr.Speakers = ClusterSpeakers(tr.Utterances, part.SpkEmb.Data)
			}
			v = tr
		case DAudioEvents:
			if part.Events == nil {
				part.Events = &Matrix{}
			}
			v = part.Events
		case DAudioCLAP:
			if part.CLAP == nil {
				part.CLAP = &Vecs{}
			}
			v = part.CLAP
		case DEmotion:
			if part.Emotion == nil {
				part.Emotion = &Emotions{}
			}
			v = part.Emotion
		}
		if err := writeGz(a.file(src, d, s), v); err != nil {
			return err
		}
	}
	_ = os.Remove(partPath)
	return nil
}

func nonNilIntervals(v []Interval) []Interval {
	if v == nil {
		return []Interval{}
	}
	return v
}

func appendVecs(dst **Vecs, v *Vecs, off float64) {
	if v == nil {
		return
	}
	if *dst == nil {
		*dst = &Vecs{Dim: v.Dim, Window: v.Window, Times: []float64{}, Data: [][]float32{}}
	}
	for i := range v.Data {
		t := off
		if i < len(v.Times) {
			t += v.Times[i]
		}
		(*dst).Times = append((*dst).Times, t)
		(*dst).Data = append((*dst).Data, v.Data[i])
	}
	if (*dst).Dim == 0 {
		(*dst).Dim = v.Dim
	}
}

func (p *audioPartial) add(r *AudioResult, off float64) error {
	for _, v := range r.VAD {
		p.VAD = append(p.VAD, Interval{Start: v.Start + off, End: v.End + off, Score: v.Score, Label: "speech"})
	}
	if t := r.Transcript; t != nil {
		if p.Language == "" {
			p.Language = t.Language
		}
		p.Model = t.Model
		emb, err := t.Embeddings.decode()
		if err != nil {
			return err
		}
		spk, err := t.Speakers.decode()
		if err != nil {
			return err
		}
		for i, u := range t.Utterances {
			u.Start += off
			u.End += off
			for k := range u.Words {
				u.Words[k].S += off
				u.Words[k].E += off
			}
			p.Utt = append(p.Utt, u)
			// Keep embedding rows in step with utterances (empty when missing).
			if emb != nil {
				row := []float32{}
				if i < len(emb.Data) {
					row = emb.Data[i]
				}
				appendVecs(&p.UttEmb, &Vecs{Dim: emb.Dim, Times: []float64{u.Start - off}, Data: [][]float32{row}}, off)
			}
			if spk != nil {
				row := []float32{}
				if i < len(spk.Data) {
					row = spk.Data[i]
				}
				appendVecs(&p.SpkEmb, &Vecs{Dim: spk.Dim, Times: []float64{u.Start - off}, Data: [][]float32{row}}, off)
			}
		}
	}
	if m := r.Events; m != nil && len(m.Rows) > 0 {
		if p.Events == nil {
			p.Events = &Matrix{Hop: m.Hop, Window: m.Window, Labels: m.Labels}
		}
		// Rows stay aligned to time: pad or cut to the chunk start.
		want := int(math.Round(off / p.Events.Hop))
		for len(p.Events.Rows) < want {
			p.Events.Rows = append(p.Events.Rows, make([]byte, len(p.Events.Labels)))
		}
		p.Events.Rows = p.Events.Rows[:want]
		p.Events.Rows = append(p.Events.Rows, m.Rows...)
	}
	if r.CLAP != nil {
		v, err := r.CLAP.decode()
		if err != nil {
			return err
		}
		appendVecs(&p.CLAP, v, off)
	}
	if e := r.Emotion; e != nil {
		if p.Emotion == nil {
			p.Emotion = &Emotions{Labels: e.Labels}
		}
		for _, sp := range e.Spans {
			sp.Start += off
			sp.End += off
			p.Emotion.Spans = append(p.Emotion.Spans, sp)
		}
	}
	return nil
}

// framePartial is the checkpoint of the worker's frame analysis.
type framePartial struct {
	Tasks []string   `json:"tasks"`
	Done  float64    `json:"done"`
	CLIP  *Vecs      `json:"clip,omitempty"`
	Faces *FaceTrack `json:"faces,omitempty"`
	OCR   []OCRFrame `json:"ocr,omitempty"`
}

var workerFrameTask = map[string]string{DFrames: "clip", DFaces: "faces", DOCR: "ocr"}

// frameSpan: seconds of frames per worker request (and checkpoint).
const frameSpan = 120

func (a *Analyzer) frames(ctx context.Context, src Source, s Settings, w *Worker, ds []string, failed map[string]error, progress Progress) error {
	var wt []string
	for _, d := range ds {
		wt = append(wt, workerFrameTask[d])
	}
	sort.Strings(wt)
	key := fmt.Sprintf("frames.v1.%s.f%gw%do%g.partial.json.gz", strings.Join(wt, "+"), s.FrameRate, s.FrameWidth, s.OCREvery)
	partPath := filepath.Join(a.dir(src.UserID, src.SHA256), key)
	var part framePartial
	if readGz(partPath, &part) != nil || strings.Join(part.Tasks, ",") != strings.Join(wt, ",") {
		part = framePartial{Tasks: wt}
	}
	total := src.Info.Duration
	if total <= 0 {
		total = 1
	}
	// OCR runs on every k-th frame.
	ocrEvery := math.Max(s.OCREvery, 1/s.FrameRate)
	for part.Done < total-0.01 {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress("watching", part.Done/total)
		from := part.Done
		to := math.Min(from+frameSpan, total)
		f, err := src.Open()
		if err != nil {
			return err
		}
		var batch FrameBatch
		err = ExtractFrames(ctx, a.Tools, f, s.run(), from, to, s.FrameRate, s.FrameWidth, func(at float64, jpeg []byte) error {
			batch.Add(at, jpeg)
			return nil
		})
		f.Close()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("decoding frames: %w", err)
		}
		if batch.N > 0 {
			res, err := w.Frames(ctx, &batch, wt, ocrEvery)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				for _, d := range ds {
					failed[d] = err
				}
				return nil
			}
			if res.CLIP != nil {
				v, err := res.CLIP.decode()
				if err != nil {
					return err
				}
				appendVecs(&part.CLIP, v, 0) // times are absolute
			}
			if res.Faces != nil {
				if part.Faces == nil {
					part.Faces = &FaceTrack{ExprLabels: res.Faces.ExprLabels, Frames: []FaceFrame{}}
				}
				part.Faces.Frames = append(part.Faces.Frames, res.Faces.Frames...)
			}
			part.OCR = append(part.OCR, res.OCR...)
		}
		part.Done = to
		if to < total-0.01 {
			if err := writeGz(partPath, &part); err != nil {
				return err
			}
		}
	}
	progress("watching", 1)
	for _, d := range ds {
		var v any
		switch d {
		case DFrames:
			if part.CLIP == nil {
				part.CLIP = &Vecs{}
			}
			v = part.CLIP
		case DFaces:
			if part.Faces == nil {
				part.Faces = &FaceTrack{Frames: []FaceFrame{}}
			}
			v = part.Faces
		case DOCR:
			if part.OCR == nil {
				part.OCR = []OCRFrame{}
			}
			v = part.OCR
		}
		if err := writeGz(a.file(src, d, s), v); err != nil {
			return err
		}
	}
	_ = os.Remove(partPath)
	return nil
}

// Load reads the cached signals of the given detectors (missing ones
// stay nil).
func (a *Analyzer) Load(src Source, detectors []string) (*Analysis, error) {
	s := a.settings()
	an := &Analysis{Duration: src.Info.Duration, HasAudio: src.Info.HasAudio()}
	for _, d := range detectors {
		p := a.file(src, d, s)
		var err error
		switch d {
		case DAudioDSP:
			var v AudioDSP
			if err = readGz(p, &v); err == nil && len(v.Loudness.Values) > 0 {
				an.Audio = &v
			}
		case DVideoDSP:
			var v VideoDSP
			if err = readGz(p, &v); err == nil {
				an.Video = &v
			}
		case DVAD:
			var v []Interval
			if err = readGz(p, &v); err == nil {
				an.Speech = nonNilIntervals(v)
			}
		case DTranscript:
			var v Transcript
			if err = readGz(p, &v); err == nil {
				an.Transcript = &v
			}
		case DAudioEvents:
			var v Matrix
			if err = readGz(p, &v); err == nil {
				an.Events = &v
			}
		case DAudioCLAP:
			var v Vecs
			if err = readGz(p, &v); err == nil {
				an.AudioEmb = &v
			}
		case DEmotion:
			var v Emotions
			if err = readGz(p, &v); err == nil {
				an.Emotion = &v
			}
		case DFrames:
			var v Vecs
			if err = readGz(p, &v); err == nil {
				an.Frames = &v
			}
		case DFaces:
			var v FaceTrack
			if err = readGz(p, &v); err == nil {
				an.Faces = &v
			}
		case DOCR:
			var v []OCRFrame
			if err = readGz(p, &v); err == nil {
				if v == nil {
					v = []OCRFrame{}
				}
				an.OCR = v
			}
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
	}
	return an, nil
}

// Cached lists the detectors measured for a recording.
func (a *Analyzer) Cached(src Source) []string {
	s := a.settings()
	var out []string
	for _, d := range AllDetectors {
		if _, err := os.Stat(a.file(src, d, s)); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// Forget removes everything measured for a recording.
func (a *Analyzer) Forget(userID int64, sha string) error {
	return os.RemoveAll(a.dir(userID, sha))
}

// Sweep removes cached signals of content that is no longer stored and
// leftovers of interrupted writes; the temp directory is emptied (call
// it when nothing is being analysed, e.g. at start).
func (a *Analyzer) Sweep(ctx context.Context, exists func(userID int64, sha string) bool, clearTemp bool) (removed int) {
	if clearTemp {
		if es, err := os.ReadDir(a.Temp); err == nil {
			for _, e := range es {
				if os.RemoveAll(filepath.Join(a.Temp, e.Name())) == nil {
					removed++
				}
			}
		}
	}
	users, _ := os.ReadDir(a.Dir)
	for _, u := range users {
		uid, err := strconv.ParseInt(u.Name(), 10, 64)
		if err != nil || !u.IsDir() {
			continue
		}
		prefixes, _ := os.ReadDir(filepath.Join(a.Dir, u.Name()))
		for _, p := range prefixes {
			entries, _ := os.ReadDir(filepath.Join(a.Dir, u.Name(), p.Name()))
			for _, e := range entries {
				if ctx.Err() != nil {
					return removed
				}
				d := filepath.Join(a.Dir, u.Name(), p.Name(), e.Name())
				if len(e.Name()) != 64 || !exists(uid, e.Name()) {
					if os.RemoveAll(d) == nil {
						removed++
					}
					continue
				}
				files, _ := os.ReadDir(d)
				for _, f := range files {
					if strings.Contains(f.Name(), ".tmp-") {
						if fi, err := f.Info(); err == nil && time.Since(fi.ModTime()) > time.Hour {
							_ = os.Remove(filepath.Join(d, f.Name()))
						}
					}
				}
			}
		}
	}
	return removed
}
