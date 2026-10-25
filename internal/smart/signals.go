// Package smart is VaultDock's smart video processing engine: it turns
// what detectors measured in a recording (signals) into timestamped
// events, combines them with the conditions of detection filters, and
// proposes segments with context, scores and explanations.
//
// Detectors run once per stored content (user + SHA-256) and their
// signals are cached, so changing a filter only re-evaluates (cheap);
// expensive analysis is repeated only when the content changes. Some
// detectors are measured by the server itself with FFmpeg (loudness,
// motion, scene changes); the others by the optional local inference
// worker (speech, audio events, vision). Nothing leaves the server.
package smart

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
)

// Series is a signal sampled at a fixed rate: Values[i] is at i/Rate s.
type Series struct {
	Rate   float64   `json:"rate"`
	Values []float32 `json:"values"`
}

// At returns the sample at t seconds (nearest).
func (s *Series) At(t float64) float64 {
	if s == nil || len(s.Values) == 0 || s.Rate <= 0 {
		return 0
	}
	i := int(math.Round(t * s.Rate))
	i = min(max(i, 0), len(s.Values)-1)
	return float64(s.Values[i])
}

// Time is the time of sample i.
func (s *Series) Time(i int) float64 { return float64(i) / s.Rate }

// Interval is something detected between Start and End (seconds).
type Interval struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Score float64 `json:"score,omitempty"`
	Label string  `json:"label,omitempty"`
}

// Vecs holds L2-normalised embeddings, one per time, stored compactly
// (float16, base64) in the cache.
type Vecs struct {
	Times []float64 `json:"times"`
	// Window: seconds each row covers from its time (audio windows; 0 for
	// frames, which stand for the time around them).
	Window float64     `json:"window,omitempty"`
	Dim    int         `json:"dim"`
	Data   [][]float32 `json:"-"`
}

type vecsJSON struct {
	Times  []float64 `json:"times"`
	Window float64   `json:"window,omitempty"`
	Dim    int       `json:"dim"`
	F16    string    `json:"f16"`
}

func (v Vecs) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, len(v.Data)*v.Dim*2)
	for _, row := range v.Data {
		for _, f := range row {
			buf = binary.LittleEndian.AppendUint16(buf, f32to16(f))
		}
	}
	return json.Marshal(vecsJSON{Times: v.Times, Window: v.Window, Dim: v.Dim, F16: base64.StdEncoding.EncodeToString(buf)})
}

func (v *Vecs) UnmarshalJSON(b []byte) error {
	var j vecsJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(j.F16)
	if err != nil {
		return err
	}
	v.Times, v.Window, v.Dim, v.Data = j.Times, j.Window, j.Dim, nil
	if j.Dim <= 0 {
		return nil
	}
	if len(raw) != len(j.Times)*j.Dim*2 {
		return errors.New("embedding data does not match its size")
	}
	v.Data = make([][]float32, len(j.Times))
	for i := range v.Data {
		row := make([]float32, j.Dim)
		for k := range row {
			row[k] = f16to32(binary.LittleEndian.Uint16(raw[(i*j.Dim+k)*2:]))
		}
		v.Data[i] = row
	}
	return nil
}

// DecodeVecs reads embeddings sent by the worker (base64 float16 rows).
func DecodeVecs(times []float64, window float64, dim int, f16 string) (Vecs, error) {
	b, _ := json.Marshal(vecsJSON{Times: times, Window: window, Dim: dim, F16: f16})
	var v Vecs
	err := v.UnmarshalJSON(b)
	return v, err
}

// Dot is the dot product (cosine similarity for normalised vectors).
func Dot(a, b []float32) float64 {
	n := min(len(a), len(b))
	var s float64
	for i := 0; i < n; i++ {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// Normalize scales v to unit length in place.
func Normalize(v []float32) {
	var s float64
	for _, f := range v {
		s += float64(f) * float64(f)
	}
	if s <= 0 {
		return
	}
	k := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= k
	}
}

func f32to16(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int((b>>23)&0xff) - 127 + 15
	mant := b & 0x7fffff
	switch {
	case exp <= 0:
		if exp < -10 {
			return sign
		}
		mant |= 0x800000
		return sign | uint16(mant>>uint(14-exp))
	case exp >= 31:
		return sign | 0x7bff // clamp to the largest finite value
	}
	return sign | uint16(exp)<<10 | uint16(mant>>13)
}

func f16to32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1f
	mant := uint32(h & 0x3ff)
	switch {
	case exp == 0:
		if mant == 0 {
			return math.Float32frombits(sign)
		}
		for mant&0x400 == 0 {
			mant <<= 1
			exp--
		}
		exp++
		mant &= 0x3ff
	case exp == 31:
		return math.Float32frombits(sign | 0x7f800000 | mant<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | mant<<13)
}

// Matrix holds per-window class scores (0..1) quantised to bytes.
type Matrix struct {
	Hop    float64  `json:"hop"`    // seconds between rows
	Window float64  `json:"window"` // seconds each row covers
	Labels []string `json:"labels"`
	Rows   [][]byte `json:"-"`
}

type matrixJSON struct {
	Hop    float64  `json:"hop"`
	Window float64  `json:"window"`
	Labels []string `json:"labels"`
	Data   string   `json:"data"`
	N      int      `json:"n"`
}

func (m Matrix) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, len(m.Rows)*len(m.Labels))
	for _, r := range m.Rows {
		buf = append(buf, r...)
	}
	return json.Marshal(matrixJSON{Hop: m.Hop, Window: m.Window, Labels: m.Labels, Data: base64.StdEncoding.EncodeToString(buf), N: len(m.Rows)})
}

func (m *Matrix) UnmarshalJSON(b []byte) error {
	var j matrixJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(j.Data)
	if err != nil {
		return err
	}
	m.Hop, m.Window, m.Labels, m.Rows = j.Hop, j.Window, j.Labels, nil
	w := len(j.Labels)
	if w == 0 || len(raw) != j.N*w {
		if j.N == 0 {
			return nil
		}
		return errors.New("score data does not match its size")
	}
	for i := 0; i < j.N; i++ {
		m.Rows = append(m.Rows, raw[i*w:(i+1)*w])
	}
	return nil
}

// Column returns the scores (0..1) of one label over time, or nil.
func (m *Matrix) Column(label string) []float64 {
	if m == nil {
		return nil
	}
	for k, l := range m.Labels {
		if l == label {
			out := make([]float64, len(m.Rows))
			for i, r := range m.Rows {
				out[i] = float64(r[k]) / 255
			}
			return out
		}
	}
	return nil
}

// Quantize turns a 0..1 score into a byte.
func Quantize(v float64) byte { return byte(math.Round(math.Min(math.Max(v, 0), 1) * 255)) }

// Word is one transcribed word.
type Word struct {
	W string  `json:"w"`
	S float64 `json:"s"`
	E float64 `json:"e"`
	P float64 `json:"p,omitempty"`
}

// Utterance is one transcribed stretch of speech.
type Utterance struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Words   []Word  `json:"words,omitempty"`
	Speaker int     `json:"speaker"` // 1-based anonymous speaker number, 0 unknown
	// NoSpeech is Whisper's estimate that this is not speech.
	NoSpeech float64 `json:"no_speech,omitempty"`
}

// Transcript is a recording's timestamped speech.
type Transcript struct {
	Language   string      `json:"language"`
	Model      string      `json:"model"`
	Utterances []Utterance `json:"utterances"`
	// Embeddings: one sentence embedding per utterance (semantic search).
	Embeddings *Vecs `json:"embeddings,omitempty"`
	// Speakers: anonymous voice embeddings per utterance (clustered into
	// Speaker numbers); not kept after clustering.
	Speakers int `json:"speakers"`
}

// FaceFrame is the face analysis of one frame.
type FaceFrame struct {
	T     float64      `json:"t"`
	Count int          `json:"count"`
	Boxes [][4]float32 `json:"boxes,omitempty"` // x, y, w, h as fractions of the frame
	// Expr: zero-shot expression scores of the largest face (labels in
	// FaceTrack.ExprLabels).
	Expr []float32 `json:"expr,omitempty"`
}

// FaceTrack is the face analysis of a recording.
type FaceTrack struct {
	ExprLabels []string    `json:"expr_labels"`
	Frames     []FaceFrame `json:"frames"`
}

// OCRWord is a recognised word with its box (fractions of the frame).
type OCRWord struct {
	Text string     `json:"text"`
	Box  [4]float32 `json:"box"`
	Conf float32    `json:"conf"`
}

// OCRFrame is the on-screen text of one frame.
type OCRFrame struct {
	T     float64   `json:"t"`
	Words []OCRWord `json:"words"`
}

// AudioDSP is what the server measures in the audio itself.
type AudioDSP struct {
	// Loudness: RMS level in dBFS, 10 per second.
	Loudness Series `json:"loudness"`
	// Flux: spectral onset strength (0..1), 10 per second.
	Flux Series `json:"flux"`
	// Voice: share of energy in the speech band (0..1), 10 per second;
	// a rough speech indicator used when no speech model is installed.
	Voice Series `json:"voice"`
}

// VideoDSP is what the server measures in the frames itself.
type VideoDSP struct {
	Motion     Series `json:"motion"`     // mean frame difference 0..1
	Scene      Series `json:"scene"`      // histogram change 0..1
	Brightness Series `json:"brightness"` // mean luma 0..1
}

// Emotions are speech emotion scores per speech stretch.
type Emotions struct {
	Labels []string  `json:"labels"`
	Spans  []EmoSpan `json:"spans"`
}

// EmoSpan holds the scores of one speech stretch.
type EmoSpan struct {
	Start  float64   `json:"start"`
	End    float64   `json:"end"`
	Scores []float32 `json:"scores"`
}

// Analysis is everything known about one recording. Tracks that were not
// measured are nil.
type Analysis struct {
	Duration   float64
	HasAudio   bool
	Audio      *AudioDSP
	Video      *VideoDSP
	Speech     []Interval // voice activity
	Transcript *Transcript
	Events     *Matrix // AudioSet audio event scores
	AudioEmb   *Vecs   // CLAP audio embeddings
	Emotion    *Emotions
	Frames     *Vecs // CLIP frame embeddings
	Faces      *FaceTrack
	OCR        []OCRFrame
}
