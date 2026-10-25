package smart

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// pcm renders samples (-1..1) as 16-bit mono at SampleRate.
func pcm(samples []float64) []byte {
	b := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(math.Max(-1, math.Min(1, v))*32767)))
	}
	return b
}

func tone(sec, freq, amp float64) []float64 {
	n := int(sec * SampleRate)
	out := make([]float64, n)
	for i := range out {
		out[i] = amp * math.Sin(2*math.Pi*freq*float64(i)/SampleRate)
	}
	return out
}

func quiet(sec float64) []float64 { return tone(sec, 200, 0.0005) }

func cat(parts ...[]float64) []float64 {
	var out []float64
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func meter(samples []float64) *AudioDSP {
	m := NewAudioMeter()
	// Odd write sizes: the meter must not depend on how the stream is cut.
	b := pcm(samples)
	for len(b) > 0 {
		n := min(len(b), 3333)
		m.Write(b[:n])
		b = b[n:]
	}
	return m.Result()
}

func TestAudioMeterLoudnessOnsetsAndVoiceBand(t *testing.T) {
	a := meter(cat(quiet(3), tone(2, 1000, 0.5), quiet(3)))
	if a.Loudness.Rate != 10 || len(a.Loudness.Values) < 79 || len(a.Loudness.Values) > 81 {
		t.Fatalf("loudness: rate %v, %d values", a.Loudness.Rate, len(a.Loudness.Values))
	}
	loud := a.Loudness.At(4)
	if math.Abs(loud-(-9)) > 1.5 { // RMS of a 0.5 sine is -9 dBFS
		t.Fatalf("loud part %.1f dBFS", loud)
	}
	if q := a.Loudness.At(1); q > -60 {
		t.Fatalf("quiet part %.1f dBFS", q)
	}
	// The strongest onset is at the start of the tone.
	best, at := 0.0, 0.0
	for i, v := range a.Flux.Values {
		if float64(v) > best {
			best, at = float64(v), float64(i)/a.Flux.Rate
		}
	}
	if math.Abs(at-3) > 0.3 {
		t.Fatalf("strongest onset at %.1fs", at)
	}
	if v := a.Voice.At(4); v < 0.8 {
		t.Fatalf("1 kHz tone voice-band share %.2f", v)
	}
	low := meter(cat(quiet(1), tone(2, 80, 0.5)))
	if v := low.Voice.At(2); v > 0.3 {
		t.Fatalf("80 Hz tone voice-band share %.2f", v)
	}
}

func TestFrameMeterSceneAndMotion(t *testing.T) {
	m := &FrameMeter{W: dspW, H: dspH, Rate: dspFrameRate}
	frame := func(v byte, pattern bool) []byte {
		px := make([]byte, dspW*dspH)
		for i := range px {
			px[i] = v
			if pattern && (i/7)%2 == 0 {
				px[i] = 255 - v
			}
		}
		return px
	}
	for i := 0; i < 8; i++ {
		m.Frame(frame(20, false))
	}
	for i := 0; i < 8; i++ {
		m.Frame(frame(200, i%2 == 0))
	}
	v := m.Result()
	if v.Scene.Values[8] < 0.5 {
		t.Fatalf("cut not seen: %v", v.Scene.Values)
	}
	if v.Scene.Values[4] > 0.05 || v.Motion.Values[4] > 0.01 {
		t.Fatalf("static frames scored: scene %v motion %v", v.Scene.Values[4], v.Motion.Values[4])
	}
	if v.Motion.Values[12] < 0.2 {
		t.Fatalf("alternating frames motion %v", v.Motion.Values[12])
	}
	if b := v.Brightness.Values[2]; math.Abs(float64(b)-20.0/255) > 0.02 {
		t.Fatalf("brightness %v", b)
	}
}

func TestVecsAndMatrixRoundTrip(t *testing.T) {
	v := Vecs{Times: []float64{0, 1.5}, Window: 2, Dim: 3, Data: [][]float32{{0.6, 0.8, 0}, {-0.25, 0.5, 1e-3}}}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got Vecs
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for i := range v.Data {
		for k := range v.Data[i] {
			if math.Abs(float64(got.Data[i][k]-v.Data[i][k])) > 1e-3 {
				t.Fatalf("row %d: %v vs %v", i, got.Data[i], v.Data[i])
			}
		}
	}
	if got.Window != 2 || got.Dim != 3 || len(got.Times) != 2 {
		t.Fatalf("vecs = %+v", got)
	}
	if _, err := DecodeVecs([]float64{0}, 0, 4, "AAAA"); err == nil {
		t.Fatal("size mismatch accepted")
	}
	m := Matrix{Hop: 1, Window: 2, Labels: []string{"Laughter", "Music"}, Rows: [][]byte{{Quantize(0.9), 0}, {0, Quantize(1.5)}}}
	b, _ = json.Marshal(m)
	var gm Matrix
	if err := json.Unmarshal(b, &gm); err != nil {
		t.Fatal(err)
	}
	if c := gm.Column("Music"); len(c) != 2 || c[1] != 1 || c[0] != 0 {
		t.Fatalf("music column %v", c)
	}
	if gm.Column("Dog") != nil {
		t.Fatal("unknown column returned")
	}
}

// matrix builds AudioSet-like scores: label -> per-second values.
func matrix(n int, cols map[string]map[int]float64) *Matrix {
	m := &Matrix{Hop: 1, Window: 2}
	for l := range cols {
		m.Labels = append(m.Labels, l)
	}
	for i := 0; i < n; i++ {
		row := make([]byte, len(m.Labels))
		for k, l := range m.Labels {
			row[k] = Quantize(cols[l][i])
		}
		m.Rows = append(m.Rows, row)
	}
	return m
}

func words(start float64, text string) Utterance {
	u := Utterance{Start: start, Text: text}
	t := start
	for _, w := range strings.Fields(text) {
		u.Words = append(u.Words, Word{W: w, S: t, E: t + 0.4, P: 0.9})
		t += 0.5
	}
	u.End = t
	return u
}

// fixture: speech 10-14 and 30-33, laughter 15-16 and 50-51, the phrase
// "clip that" at 31, a loud burst at 40.
func fixture() *Analysis {
	loud := make([]float32, 600)
	for i := range loud {
		loud[i] = -50
		if i >= 400 && i < 420 {
			loud[i] = -8
		}
	}
	return &Analysis{
		Duration: 60, HasAudio: true,
		Audio:  &AudioDSP{Loudness: Series{Rate: 10, Values: loud}, Flux: Series{Rate: 10, Values: make([]float32, 600)}, Voice: Series{Rate: 10, Values: make([]float32, 600)}},
		Speech: []Interval{{Start: 10, End: 14}, {Start: 30, End: 33}},
		Events: matrix(60, map[string]map[int]float64{"Laughter": {15: 0.9, 50: 0.7}, "Music": {}}),
		Transcript: &Transcript{Language: "en", Utterances: []Utterance{
			words(10, "so I was saying the joke goes like this"),
			words(30, "okay clip that right now"),
		}},
	}
}

func evalRule(t *testing.T, n Node) []Event {
	t.Helper()
	f := Filter{Rules: []Rule{{ID: "r", Name: "R", When: n}}, Context: DefaultContext()}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	return Evaluate(&f, &Env{A: fixture()})
}

func spans(evs []Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, strings.TrimRight(strings.TrimRight(jsonNum(e.Start), "0"), ".")+"-"+strings.TrimRight(strings.TrimRight(jsonNum(e.End), "0"), "."))
	}
	return strings.Join(s, ",")
}

func jsonNum(v float64) string { b, _ := json.Marshal(math.Round(v*10) / 10); return string(b) + ".0" }

func TestLeafConditions(t *testing.T) {
	if got := spans(evalRule(t, sound("laughter"))); got != "15-17,50-52" {
		t.Fatalf("laughter: %s", got)
	}
	// A higher minimum score drops the weaker laugh.
	n := sound("laughter")
	n.MinScore = 0.8
	if got := spans(evalRule(t, n)); got != "15-17" {
		t.Fatalf("strong laughter: %s", got)
	}
	kw := evalRule(t, Node{Kind: "keyword", Terms: []string{"Clip That!"}})
	if len(kw) != 1 || math.Abs(kw[0].Start-30.5) > 0.01 || !strings.Contains(kw[0].Reasons[0].Text, "clip that") {
		t.Fatalf("keyword: %+v", kw)
	}
	if got := evalRule(t, Node{Kind: "keyword", Terms: []string{"banana"}}); len(got) != 0 {
		t.Fatalf("absent keyword found: %v", got)
	}
	rise := evalRule(t, Node{Kind: "loudness", Mode: "sudden_rise"})
	if len(rise) != 1 || math.Abs(rise[0].Start-39.7) > 0.11 || rise[0].End < 41.9 {
		t.Fatalf("sudden rise: %+v", rise)
	}
	sp := evalRule(t, Node{Kind: "speech", Mode: "active"})
	if spans(sp) != "10-15,30-33" { // words run past the VAD end; VAD wins
		if spans(sp) != "10-14,30-33" {
			t.Fatalf("speech: %s", spans(sp))
		}
	}
}

func TestCombinedConditions(t *testing.T) {
	speech := Node{Kind: "speech", Mode: "active"}
	laugh := sound("laughter")
	// Speech followed by laughter within 4 s: only the first laugh qualifies.
	then := evalRule(t, op("then", 4, speech, laugh))
	if spans(then) != "10-17" {
		t.Fatalf("then: %s", spans(then))
	}
	if len(then[0].Reasons) < 2 {
		t.Fatalf("then lost reasons: %+v", then[0].Reasons)
	}
	// The reverse order does not match.
	if got := evalRule(t, op("then", 4, laugh, speech)); len(got) != 0 {
		t.Fatalf("reversed sequence matched: %s", spans(got))
	}
	// And: keyword together with speech.
	and := evalRule(t, op("and", 1, Node{Kind: "keyword", Terms: []string{"clip that"}}, speech))
	if len(and) != 1 || and[0].Start != 30 {
		t.Fatalf("and: %s", spans(and))
	}
	// Or: both kinds of moments.
	or := evalRule(t, op("or", 0, laugh, Node{Kind: "loudness", Mode: "sudden_rise"}))
	if len(or) != 3 {
		t.Fatalf("or: %s", spans(or))
	}
	// Without: laughter with no speech nearby (the second laugh).
	without := evalRule(t, op("without", 5, laugh, speech))
	if spans(without) != "50-52" {
		t.Fatalf("without: %s", spans(without))
	}
	// A window that is too short breaks the sequence.
	if got := evalRule(t, op("then", 0.5, speech, laugh)); len(got) != 0 {
		t.Fatalf("then within 0.5s: %s", spans(got))
	}
}

func TestMissingSignalsWarnNotSubstitute(t *testing.T) {
	a := fixture()
	a.Events = nil
	f := Filter{Rules: []Rule{{ID: "r", Name: "Laughter", When: sound("laughter")}}, Context: DefaultContext()}
	env := &Env{A: a}
	if evs := Evaluate(&f, env); len(evs) != 0 {
		t.Fatalf("laughter found without the sound model: %v", evs)
	}
	if len(env.Warnings) != 1 || !strings.Contains(env.Warnings[0], "AST") {
		t.Fatalf("warnings = %v", env.Warnings)
	}
	// Zero-shot conditions need a text embedder.
	f.Rules[0].When = Node{Kind: "visual_description", Text: "a goal celebration"}
	env = &Env{A: a}
	Evaluate(&f, env)
	if len(env.Warnings) == 0 {
		t.Fatal("no warning for unmeasured frames")
	}
}

func TestSuggestContextMergeLengthAndDedupe(t *testing.T) {
	a := fixture()
	a.Transcript = nil
	a.Speech = nil
	c := DefaultContext()
	c.Pre, c.Post, c.MergeGap, c.TrimSilence, c.SnapSpeech = 5, 3, 4, false, false
	evs := []Event{
		{Start: 20, End: 21, Score: 0.9, RuleName: "A", Category: "emotion"},
		{Start: 25, End: 26, Score: 0.6, RuleName: "B", Category: "audio"},
		{Start: 45, End: 46, Score: 0.3, RuleName: "weak"},
		{Start: 1, End: 2, Score: 0.8, RuleName: "early"},
	}
	s := Suggest(a, evs, c)
	if len(s) != 2 {
		t.Fatalf("suggestions: %+v", s)
	}
	if s[0].Start != 0 || s[0].End != 5 {
		t.Fatalf("padding clipped at 0: %+v", s[0])
	}
	m := s[1]
	if m.Start != 15 || m.End != 29 || m.Events != 2 || m.Label != "A + B" || m.Category != "emotion" || m.Score != 0.9 {
		t.Fatalf("merged: %+v", m)
	}
	// Longest segment: the context is shortened around the moments.
	c.MaxSegment = 8
	s = Suggest(a, evs, c)
	for _, x := range s {
		if x.End-x.Start > 8.01 {
			t.Fatalf("too long: %+v", x)
		}
	}
	// Merging off keeps moments apart; dedupe removes the weaker overlap.
	c.MaxSegment, c.Merge, c.Dedupe = 90, false, 0.4
	s = Suggest(a, evs, c)
	if len(s) != 2 || s[1].Label != "A" {
		t.Fatalf("dedupe: %+v", s)
	}
	c.Dedupe = 0
	if s = Suggest(a, evs, c); len(s) != 3 {
		t.Fatalf("no dedupe: %+v", s)
	}
	// Minimum moment length.
	c.MinEvent = 2
	if s = Suggest(a, evs, c); len(s) != 0 {
		t.Fatalf("short moments kept: %+v", s)
	}
}

func TestSuggestSnapsToSentencesAndTrimsSilence(t *testing.T) {
	a := fixture()
	c := DefaultContext()
	c.Pre, c.Post, c.MergeGap, c.TrimSilence = 2, 1, 0, false
	// Laughter at 15-17 with 2 s before: starts at 13, inside the sentence
	// 10-14.5; the start moves to the sentence start.
	s := Suggest(a, []Event{{Start: 15, End: 17, Score: 0.9, RuleName: "L"}}, c)
	if len(s) != 1 || s[0].Start != 9.8 {
		t.Fatalf("snap: %+v", s)
	}
	// Silent padding is trimmed down to a second around the moment.
	c.SnapSpeech, c.TrimSilence, c.Pre, c.Post = false, true, 10, 10
	s = Suggest(a, []Event{{Start: 40, End: 42, Score: 0.9, RuleName: "Loud"}}, c)
	if len(s) != 1 || s[0].Start < 38.5 || s[0].End > 43.6 {
		t.Fatalf("trim: %+v", s)
	}
}

func TestInterpretDescriptions(t *testing.T) {
	all := Availability{FFmpeg: true, Models: map[string]bool{"silero-vad": true, "whisper": true, "ast": true, "clap": true, "ser": true, "clip": true, "yunet": true, "tesseract": true, "minilm": true}}
	p := Interpret("someone tells a joke and the other person laughs", all)
	r := p.Filter.Rules[0].When
	if r.Op != "then" || len(r.Children) < 2 {
		t.Fatalf("joke plan: %+v", r)
	}
	if p.Status != "supported" || !p.Filter.Rules[1].Disabled {
		t.Fatalf("status %s rules %+v", p.Status, p.Filter.Rules)
	}
	p = Interpret(`when I say "clip that"`, all)
	if k := p.Filter.Rules[0].When; k.Kind != "keyword" || k.Terms[0] != "clip that" {
		t.Fatalf("keyword plan: %+v", k)
	}
	p = Interpret("clutch plays", all)
	if p.Status != "partial" || len(p.Unsupported) == 0 {
		t.Fatalf("clutch: %s %v", p.Status, p.Unsupported)
	}
	p = Interpret("laughing after the scoreboard changes", all)
	w := p.Filter.Rules[0].When
	if w.Op != "then" || w.Children[0].Kind != "score_change" || w.Children[1].Kind != "audio_event" {
		t.Fatalf("after: %+v", w)
	}
	p = Interpret("talking about cars", all)
	if w := p.Filter.Rules[0].When; w.Kind != "dialogue" || !strings.Contains(w.Text, "cars") {
		t.Fatalf("topic: %+v", w)
	}
	// Nothing recognised: approximate meaning search, said so.
	p = Interpret("purple elephants", all)
	if p.Filter.Rules[0].When.Op != "or" || len(p.Notes) == 0 {
		t.Fatalf("semantic: %+v %v", p.Filter.Rules[0].When, p.Notes)
	}
	// Without models: unavailable, with the models named.
	p = Interpret("laughter", Availability{FFmpeg: true})
	if p.Status != "unavailable" || len(p.Components) == 0 || p.Components[0].Available || p.Components[0].Missing[0] != "ast" {
		t.Fatalf("no models: %s %+v", p.Status, p.Components)
	}
	// Only the server's own measurements: loudness works.
	p = Interpret("sudden loud moments", Availability{FFmpeg: true})
	if p.Status != "supported" || p.Filter.Rules[0].When.Mode != "sudden_rise" {
		t.Fatalf("loudness: %s %+v", p.Status, p.Filter.Rules[0].When)
	}
}

func TestFinishPlanRejectsInvalidModelOutput(t *testing.T) {
	avail := Availability{FFmpeg: true}
	if _, ok := FinishPlan("x", "m", Filter{}, nil, avail); ok {
		t.Fatal("empty filter accepted")
	}
	bad := Filter{Rules: []Rule{{Name: "x", When: Node{Kind: "telepathy"}}}, Context: DefaultContext()}
	if _, ok := FinishPlan("x", "m", bad, nil, avail); ok {
		t.Fatal("unknown kind accepted")
	}
	good := Filter{Rules: []Rule{{Name: "Loud", When: Node{Kind: "loudness", Mode: "loud"}}}, Context: DefaultContext()}
	p, ok := FinishPlan("loud bits", "qwen", good, []string{"n"}, avail)
	if !ok || p.Interpreter != "model:qwen" || p.Status != "supported" {
		t.Fatalf("plan %+v", p)
	}
}

func TestFilterValidation(t *testing.T) {
	deep := Node{Kind: "speech", Mode: "active"}
	for i := 0; i < 8; i++ {
		deep = op("and", 1, deep)
	}
	for name, f := range map[string]Filter{
		"too deep":       {Rules: []Rule{{Name: "x", When: deep}}, Context: DefaultContext()},
		"bad threshold":  {Rules: []Rule{{Name: "x", When: Node{Kind: "speech", Mode: "active"}}}, Context: Context{Threshold: 2}},
		"bad mode":       {Rules: []Rule{{Name: "x", When: Node{Kind: "loudness", Mode: "deafening"}}}, Context: DefaultContext()},
		"bad window":     {Rules: []Rule{{Name: "x", When: Node{Op: "then", Window: -1, Children: []Node{{Kind: "question"}}}}}, Context: DefaultContext()},
		"empty keywords": {Rules: []Rule{{Name: "x", When: Node{Kind: "keyword"}}}, Context: DefaultContext()},
	} {
		if err := f.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Every built-in preset is valid and only uses known kinds.
	for _, p := range Builtins() {
		f := p.Filter
		if err := f.Validate(); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		f.Rules = append(f.Rules[:0:0], f.Rules...)
		for _, r := range f.Rules {
			r.When.Leaves(func(n *Node) {
				if _, ok := kinds[n.Kind]; !ok {
					t.Errorf("%s uses unknown kind %q", p.ID, n.Kind)
				}
			})
		}
	}
}

func TestPresetsNeedRealDetectors(t *testing.T) {
	none := Availability{FFmpeg: true}
	for _, p := range Builtins() {
		if p.ID == "builtin:custom" {
			continue
		}
		f := p.Filter
		cs := Check(&f, none)
		if len(cs) == 0 {
			t.Errorf("%s has no conditions", p.ID)
		}
		for _, c := range cs {
			if len(c.Detectors) == 0 {
				t.Errorf("%s/%s names no detector", p.ID, c.Rule)
			}
		}
	}
	// Loud reactions still finds loudness jumps without any model.
	p, _ := Builtin("builtin:loud")
	f := p.Filter
	ok := false
	for _, c := range Check(&f, none) {
		ok = ok || (c.Available && c.Kind == "loudness")
	}
	if !ok {
		t.Fatal("loudness condition unavailable with FFmpeg alone")
	}
}

func TestClusterSpeakers(t *testing.T) {
	a := []float32{1, 0, 0}
	a2 := []float32{0.98, 0.2, 0}
	b := []float32{0, 1, 0}
	b2 := []float32{0.1, 0.99, 0}
	for _, v := range [][]float32{a2, b2} {
		Normalize(v)
	}
	u := make([]Utterance, 4)
	for i := range u {
		u[i] = Utterance{Start: float64(i * 3), End: float64(i*3) + 2.5}
	}
	n := ClusterSpeakers(u, [][]float32{a, b, a2, b2})
	if n != 2 || u[0].Speaker != u[2].Speaker || u[1].Speaker != u[3].Speaker || u[0].Speaker == u[1].Speaker {
		t.Fatalf("speakers %d: %d %d %d %d", n, u[0].Speaker, u[1].Speaker, u[2].Speaker, u[3].Speaker)
	}
}
