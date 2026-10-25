package smart

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Worker talks to the local inference worker (a separate container on
// the same host or LAN). Requests carry a shared token; media is sent as
// decoded audio and sampled frames, never as stored files.
type Worker struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// ErrNoWorker: no worker is configured.
var ErrNoWorker = errors.New("the smart processing worker is not configured")

// NewWorker returns nil when url is empty.
func NewWorker(u, token string) *Worker {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return nil
	}
	// Long recordings take a while; the context bounds each call.
	return &Worker{URL: u, Token: token, HTTP: &http.Client{Timeout: 0}}
}

// ModelStatus describes one worker model.
type ModelStatus struct {
	Title       string  `json:"title"`
	Capability  string  `json:"capability"`
	License     string  `json:"license"`
	Source      string  `json:"source"`
	Installed   bool    `json:"installed"`
	Loaded      bool    `json:"loaded"`
	Bytes       int64   `json:"bytes"`
	Estimate    int64   `json:"estimate"`
	Downloading bool    `json:"downloading"`
	Progress    float64 `json:"progress"`
	Error       string  `json:"error,omitempty"`
	Optional    bool    `json:"optional,omitempty"`
}

// WorkerStatus is what the worker reports about itself.
type WorkerStatus struct {
	Version    string                 `json:"version"`
	Device     string                 `json:"device"`
	Threads    int                    `json:"threads"`
	Models     map[string]ModelStatus `json:"models"`
	Busy       int                    `json:"busy"`
	RSS        int64                  `json:"rss"`
	CPUPercent float64                `json:"cpu_percent"`
	MemTotal   int64                  `json:"mem_total"`
	MemAvail   int64                  `json:"mem_available"`
	GPU        string                 `json:"gpu,omitempty"`
}

// Installed lists the usable models.
func (s *WorkerStatus) Installed() map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	for k, m := range s.Models {
		if m.Installed {
			out[k] = true
		}
	}
	return out
}

func (w *Worker) do(ctx context.Context, method, path string, body io.Reader, ctype string, out any) error {
	return w.doSized(ctx, method, path, body, -1, ctype, out)
}

// doSized sends a body of a known size (size >= 0) with its length, so the
// worker never receives a streamed (chunked) body it cannot size.
func (w *Worker) doSized(ctx context.Context, method, path string, body io.Reader, size int64, ctype string, out any) error {
	if w == nil {
		return ErrNoWorker
	}
	req, err := http.NewRequestWithContext(ctx, method, w.URL+path, body)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	if w.Token != "" {
		req.Header.Set("Authorization", "Bearer "+w.Token)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := w.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the smart processing worker is not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return &WorkerError{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(resp.Body)
	return dec.Decode(out)
}

// WorkerError is an error answer of the worker.
type WorkerError struct {
	Status  int
	Message string
}

func (e *WorkerError) Error() string {
	return fmt.Sprintf("smart processing worker: %s (%d)", e.Message, e.Status)
}

// Status asks the worker for its state.
func (w *Worker) Status(ctx context.Context) (*WorkerStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var s WorkerStatus
	if err := w.do(ctx, "GET", "/v1/status", nil, "", &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Install starts downloading models (explicit setup by an administrator).
func (w *Worker) Install(ctx context.Context, models []string) error {
	b, _ := json.Marshal(map[string]any{"models": models})
	return w.do(ctx, "POST", "/v1/models/install", bytes.NewReader(b), "application/json", nil)
}

// Remove deletes downloaded models.
func (w *Worker) Remove(ctx context.Context, models []string) error {
	b, _ := json.Marshal(map[string]any{"models": models})
	return w.do(ctx, "POST", "/v1/models/remove", bytes.NewReader(b), "application/json", nil)
}

// AudioResult is the worker's analysis of one audio chunk (times relative
// to the chunk).
type AudioResult struct {
	VAD        []Interval `json:"vad"`
	Transcript *struct {
		Language   string      `json:"language"`
		Model      string      `json:"model"`
		Utterances []Utterance `json:"utterances"`
		Embeddings *wireVecs   `json:"embeddings"`
		Speakers   *wireVecs   `json:"speakers"`
	} `json:"transcript"`
	Events  *Matrix   `json:"events"`
	CLAP    *wireVecs `json:"clap"`
	Emotion *Emotions `json:"emotion"`
}

type wireVecs struct {
	Times  []float64 `json:"times"`
	Window float64   `json:"window"`
	Dim    int       `json:"dim"`
	F16    string    `json:"f16"`
}

func (v *wireVecs) decode() (*Vecs, error) {
	if v == nil {
		return nil, nil
	}
	x, err := DecodeVecs(v.Times, v.Window, v.Dim, v.F16)
	if err != nil {
		return nil, err
	}
	return &x, nil
}

// Audio analyses a WAV chunk (16 kHz mono) of size bytes.
func (w *Worker) Audio(ctx context.Context, wav io.Reader, size int64, tasks []string, language string) (*AudioResult, error) {
	q := url.Values{"tasks": {strings.Join(tasks, ",")}}
	if language != "" {
		q.Set("language", language)
	}
	var out AudioResult
	if err := w.doSized(ctx, "POST", "/v1/audio?"+q.Encode(), wav, size, "audio/wav", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FrameResult is the worker's analysis of a batch of frames.
type FrameResult struct {
	CLIP  *wireVecs  `json:"clip"`
	Faces *FaceTrack `json:"faces"`
	OCR   []OCRFrame `json:"ocr"`
}

// FrameBatch encodes frames for Frames: time (float64), length (uint32),
// JPEG, repeated.
type FrameBatch struct {
	buf bytes.Buffer
	N   int
}

func (b *FrameBatch) Add(t float64, jpeg []byte) {
	var h [12]byte
	binary.LittleEndian.PutUint64(h[:8], math.Float64bits(t))
	binary.LittleEndian.PutUint32(h[8:], uint32(len(jpeg)))
	b.buf.Write(h[:])
	b.buf.Write(jpeg)
	b.N++
}

func (b *FrameBatch) Len() int { return b.buf.Len() }

// Frames analyses a batch of frames. ocrEvery: seconds between frames
// read with OCR.
func (w *Worker) Frames(ctx context.Context, b *FrameBatch, tasks []string, ocrEvery float64) (*FrameResult, error) {
	q := url.Values{"tasks": {strings.Join(tasks, ",")}, "ocr_every": {fmt.Sprint(ocrEvery)}}
	var out FrameResult
	if err := w.do(ctx, "POST", "/v1/frames?"+q.Encode(), bytes.NewReader(b.buf.Bytes()), "application/octet-stream", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Text embeds texts with a model; it also returns the model's logit scale.
func (w *Worker) Text(ctx context.Context, model string, texts []string) ([][]float32, float64, error) {
	b, _ := json.Marshal(map[string]any{"model": model, "texts": texts})
	var out struct {
		wireVecs
		Scale float64 `json:"scale"`
	}
	if err := w.do(ctx, "POST", "/v1/text", bytes.NewReader(b), "application/json", &out); err != nil {
		return nil, 0, err
	}
	out.Times = make([]float64, len(texts))
	v, err := out.decode()
	if err != nil {
		return nil, 0, err
	}
	return v.Data, out.Scale, nil
}

// Plan asks the worker's local language model to express a description
// as a filter. ErrNoPlanner when no language model is installed.
func (w *Worker) Plan(ctx context.Context, desc string, guide string) (*Filter, string, []string, error) {
	b, _ := json.Marshal(map[string]any{"description": desc, "guide": guide})
	var out struct {
		Model  string   `json:"model"`
		Filter Filter   `json:"filter"`
		Notes  []string `json:"notes"`
	}
	if err := w.do(ctx, "POST", "/v1/plan", bytes.NewReader(b), "application/json", &out); err != nil {
		var we *WorkerError
		if errors.As(err, &we) && we.Status == 501 {
			return nil, "", nil, ErrNoPlanner
		}
		return nil, "", nil, err
	}
	return &out.Filter, out.Model, out.Notes, nil
}

// ErrNoPlanner: the worker has no language model for interpreting
// descriptions.
var ErrNoPlanner = errors.New("no local language model is installed")

// TextCache embeds queries through the worker and remembers them.
type TextCache struct {
	W      *Worker
	Ctx    context.Context
	mu     sync.Mutex
	vecs   map[string][]float32
	scales map[string]float64
}

func NewTextCache(w *Worker) *TextCache {
	return &TextCache{W: w, Ctx: context.Background(), vecs: map[string][]float32{}, scales: map[string]float64{}}
}

func (c *TextCache) Text(model string, texts []string) ([][]float32, error) {
	c.mu.Lock()
	out := make([][]float32, len(texts))
	var missing []string
	for i, t := range texts {
		if v, ok := c.vecs[model+"\x00"+t]; ok {
			out[i] = v
		} else {
			missing = append(missing, t)
		}
	}
	c.mu.Unlock()
	if len(missing) > 0 {
		ctx, cancel := context.WithTimeout(c.Ctx, 2*time.Minute)
		defer cancel()
		vs, scale, err := c.W.Text(ctx, model, missing)
		if err != nil {
			return nil, err
		}
		if len(vs) != len(missing) {
			return nil, errors.New("the worker returned the wrong number of embeddings")
		}
		c.mu.Lock()
		if len(c.vecs) > 4096 {
			c.vecs = map[string][]float32{}
		}
		for i, t := range missing {
			c.vecs[model+"\x00"+t] = vs[i]
		}
		if scale > 0 {
			c.scales[model] = scale
		}
		for i, t := range texts {
			out[i] = c.vecs[model+"\x00"+t]
		}
		c.mu.Unlock()
	}
	return out, nil
}

func (c *TextCache) Scale(model string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scales[model]
}
