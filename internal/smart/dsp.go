package smart

import (
	"encoding/binary"
	"io"
	"math"
	"math/cmplx"
)

// Audio and frame measurements the server makes itself (no models).

const (
	// SampleRate of the decoded analysis audio (mono, s16le).
	SampleRate = 16000
	// dspRate: measurements per second.
	dspRate  = 10
	dspHop   = SampleRate / dspRate
	fftSize  = 1024
	silentDB = -90
)

// AudioMeter measures loudness, onsets and the speech-band share of a
// mono 16 kHz signal written to it in any pieces.
type AudioMeter struct {
	buf      []float64
	window   []float64
	prevMag  []float64
	loud     []float32
	flux     []float32
	voice    []float32
	leftover []byte
}

func NewAudioMeter() *AudioMeter {
	w := make([]float64, fftSize)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1)) // Hann
	}
	return &AudioMeter{window: w}
}

// Write takes raw s16le samples.
func (m *AudioMeter) Write(p []byte) (int, error) {
	n := len(p)
	if len(m.leftover) > 0 {
		p = append(m.leftover, p...)
		m.leftover = nil
	}
	for len(p) >= 2 {
		m.buf = append(m.buf, float64(int16(binary.LittleEndian.Uint16(p)))/32768)
		p = p[2:]
	}
	if len(p) == 1 {
		m.leftover = []byte{p[0]}
	}
	for len(m.buf) >= dspHop {
		m.frame(m.buf[:dspHop])
		m.buf = m.buf[dspHop:]
	}
	return n, nil
}

// ReadFrom consumes r to the end.
func (m *AudioMeter) ReadFrom(r io.Reader) (int64, error) {
	b := make([]byte, 64<<10)
	var total int64
	for {
		n, err := r.Read(b)
		if n > 0 {
			_, _ = m.Write(b[:n])
			total += int64(n)
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func (m *AudioMeter) frame(x []float64) {
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(len(x)))
	db := float64(silentDB)
	if rms > 1e-9 {
		db = math.Max(20*math.Log10(rms), silentDB)
	}
	m.loud = append(m.loud, float32(db))
	// Spectrum of the hop's first fftSize samples.
	seg := make([]complex128, fftSize)
	for i := 0; i < fftSize && i < len(x); i++ {
		seg[i] = complex(x[i]*m.window[i], 0)
	}
	fft(seg)
	half := fftSize / 2
	mag := make([]float64, half)
	var total, band float64
	binHz := float64(SampleRate) / fftSize
	for i := 1; i < half; i++ {
		a := cmplx.Abs(seg[i])
		mag[i] = math.Log1p(a * 10)
		e := a * a
		total += e
		if f := float64(i) * binHz; f >= 300 && f <= 3400 {
			band += e
		}
	}
	var flux float64
	if m.prevMag != nil {
		for i := range mag {
			if d := mag[i] - m.prevMag[i]; d > 0 {
				flux += d
			}
		}
		flux /= float64(half)
	}
	m.prevMag = mag
	m.flux = append(m.flux, float32(flux))
	share := 0.0
	if total > 1e-12 {
		share = band / total
	}
	m.voice = append(m.voice, float32(share))
}

// Result returns the measurements. Flux is scaled to 0..1 by its 99th
// percentile so recordings of any level compare.
func (m *AudioMeter) Result() *AudioDSP {
	if len(m.buf) > dspHop/4 {
		pad := make([]float64, dspHop)
		copy(pad, m.buf)
		m.frame(pad)
		m.buf = nil
	}
	flux := append([]float32(nil), m.flux...)
	if p := percentile32(flux, 0.99); p > 0 {
		for i := range flux {
			flux[i] = float32(math.Min(float64(flux[i])/p, 1))
		}
	}
	return &AudioDSP{
		Loudness: Series{Rate: dspRate, Values: m.loud},
		Flux:     Series{Rate: dspRate, Values: flux},
		Voice:    Series{Rate: dspRate, Values: m.voice},
	}
}

// fft is an in-place radix-2 FFT (len(a) must be a power of two).
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			k := complex(1, 0)
			for i := 0; i < size/2; i++ {
				u, v := a[start+i], a[start+i+size/2]*k
				a[start+i], a[start+i+size/2] = u+v, u-v
				k *= w
			}
		}
	}
}

func percentile32(v []float32, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := make([]float64, len(v))
	for i, f := range v {
		s[i] = float64(f)
	}
	return percentile(s, p)
}

// FrameMeter measures motion, scene changes and brightness of small gray
// frames (W×H bytes each) at Rate frames per second.
type FrameMeter struct {
	W, H   int
	Rate   float64
	prev   []byte
	prevH  []float64
	motion []float32
	scene  []float32
	bright []float32
}

const histBins = 32

// Frame takes one frame.
func (m *FrameMeter) Frame(pix []byte) {
	n := m.W * m.H
	if len(pix) < n || n == 0 {
		return
	}
	pix = pix[:n]
	hist := make([]float64, histBins)
	var sum, diff float64
	for i, p := range pix {
		sum += float64(p)
		hist[int(p)*histBins/256]++
		if m.prev != nil {
			diff += math.Abs(float64(p) - float64(m.prev[i]))
		}
	}
	for i := range hist {
		hist[i] /= float64(n)
	}
	var sc float64
	if m.prevH != nil {
		for i := range hist {
			sc += math.Abs(hist[i] - m.prevH[i])
		}
		sc /= 2
	}
	m.motion = append(m.motion, float32(diff/float64(n)/255))
	m.scene = append(m.scene, float32(sc))
	m.bright = append(m.bright, float32(sum/float64(n)/255))
	if m.prev == nil {
		m.prev = make([]byte, n)
	}
	copy(m.prev, pix)
	m.prevH = hist
}

// ReadFrom consumes raw gray frames from r.
func (m *FrameMeter) ReadFrom(r io.Reader) (int64, error) {
	n := m.W * m.H
	buf := make([]byte, n)
	var total int64
	for {
		k, err := io.ReadFull(r, buf)
		total += int64(k)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
		m.Frame(buf)
	}
}

func (m *FrameMeter) Result() *VideoDSP {
	return &VideoDSP{
		Motion:     Series{Rate: m.Rate, Values: m.motion},
		Scene:      Series{Rate: m.Rate, Values: m.scene},
		Brightness: Series{Rate: m.Rate, Values: m.bright},
	}
}

// EnergySpeech estimates speech stretches from the measurements alone
// (no model): loud enough above the recording's noise floor with most of
// its energy in the speech band. Music and noise also pass; it is the
// fallback when the speech model is not installed.
func EnergySpeech(a *AudioDSP) []Interval {
	if a == nil || len(a.Loudness.Values) == 0 {
		return nil
	}
	floor := noiseFloor(a)
	var out []Interval
	start := -1
	run := 0
	for i, l := range a.Loudness.Values {
		voiced := float64(l) > floor+12 && a.Voice.At(a.Loudness.Time(i)) > 0.55
		if voiced {
			if start < 0 {
				start = i
			}
			run = 0
		} else if start >= 0 {
			run++
			if run > 3 { // 0.4 s pause ends a stretch
				end := i - run + 1
				if float64(end-start)/dspRate >= 0.3 {
					out = append(out, Interval{Start: float64(start) / dspRate, End: float64(end) / dspRate, Score: 0.5, Label: "speech"})
				}
				start, run = -1, 0
			}
		}
	}
	if start >= 0 {
		end := len(a.Loudness.Values) - run
		if float64(end-start)/dspRate >= 0.3 {
			out = append(out, Interval{Start: float64(start) / dspRate, End: float64(end) / dspRate, Score: 0.5, Label: "speech"})
		}
	}
	return out
}

// noiseFloor is the 10th percentile loudness, ignoring digital silence.
func noiseFloor(a *AudioDSP) float64 {
	var v []float64
	for _, l := range a.Loudness.Values {
		if l > silentDB+1 {
			v = append(v, float64(l))
		}
	}
	if len(v) == 0 {
		return silentDB
	}
	return percentile(v, 0.10)
}
