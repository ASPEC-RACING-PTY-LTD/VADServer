package smart

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Reason explains one detection behind a moment.
type Reason struct {
	Kind     string  `json:"kind"`
	Detector string  `json:"detector,omitempty"`
	Text     string  `json:"text"`
	Score    float64 `json:"score"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
}

// Event is a detected moment.
type Event struct {
	Start    float64  `json:"start"`
	End      float64  `json:"end"`
	Score    float64  `json:"score"`
	Rule     string   `json:"rule,omitempty"`
	RuleName string   `json:"rule_name,omitempty"`
	Category string   `json:"category,omitempty"`
	Reasons  []Reason `json:"reasons"`
}

// Embedder embeds text queries for the zero-shot detectors (worker).
type Embedder interface {
	// Text returns L2-normalised embeddings of texts for a model ("clip",
	// "clap", "minilm").
	Text(model string, texts []string) ([][]float32, error)
	// Scale is the model's logit scale for zero-shot probabilities.
	Scale(model string) float64
}

// Env is what a filter is evaluated against.
type Env struct {
	A     *Analysis
	Embed Embedder
	// Warnings collects problems (missing signals, failed queries) once each.
	Warnings []string
	// Leaves collects every detector moment (for the review timeline) when
	// KeepLeaves is set.
	KeepLeaves bool
	Leaves     []Event
	warned     map[string]bool
}

func (e *Env) warn(format string, a ...any) {
	m := fmt.Sprintf(format, a...)
	if e.warned == nil {
		e.warned = map[string]bool{}
	}
	if !e.warned[m] {
		e.warned[m] = true
		e.Warnings = append(e.Warnings, m)
	}
}

// maxLeafEvents bounds one condition's moments in one recording.
const maxLeafEvents = 2000

// Evaluate runs every enabled rule and returns the moments found, each
// labelled with its rule.
func Evaluate(f *Filter, env *Env) []Event {
	var out []Event
	for i := range f.Rules {
		r := &f.Rules[i]
		if r.Disabled {
			continue
		}
		for _, ev := range env.node(&r.When) {
			ev.Rule, ev.RuleName, ev.Category = r.ID, r.Name, r.Category
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

func (e *Env) node(n *Node) []Event {
	if n.Op == "" {
		evs := e.leaf(n)
		k := kinds[n.Kind]
		minScore := n.MinScore
		if minScore == 0 {
			minScore = k.DefaultScore
		}
		kept := evs[:0]
		for _, ev := range evs {
			if ev.Score >= minScore && ev.End-ev.Start >= n.MinDuration {
				kept = append(kept, ev)
			}
		}
		if len(kept) > maxLeafEvents {
			sort.Slice(kept, func(i, j int) bool { return kept[i].Score > kept[j].Score })
			kept = kept[:maxLeafEvents]
			sort.Slice(kept, func(i, j int) bool { return kept[i].Start < kept[j].Start })
		}
		if e.KeepLeaves && len(e.Leaves) < 20000 {
			for _, ev := range kept {
				ev.Category = k.Category
				e.Leaves = append(e.Leaves, ev)
			}
		}
		return kept
	}
	kids := make([][]Event, len(n.Children))
	for i := range n.Children {
		kids[i] = e.node(&n.Children[i])
	}
	switch n.Op {
	case "or":
		var all []Event
		for _, k := range kids {
			all = append(all, k...)
		}
		return mergeEvents(all, 0)
	case "and":
		return andEvents(kids, n.Window)
	case "then":
		return thenEvents(kids, n.Window)
	case "without":
		return withoutEvents(kids, n.Window)
	}
	return nil
}

// gap between two intervals (0 when they overlap).
func gap(a, b Event) float64 {
	return math.Max(0, math.Max(a.Start, b.Start)-math.Min(a.End, b.End))
}

func joinEvents(evs ...Event) Event {
	out := Event{Start: math.Inf(1), End: math.Inf(-1)}
	prod := 1.0
	for _, ev := range evs {
		out.Start = math.Min(out.Start, ev.Start)
		out.End = math.Max(out.End, ev.End)
		prod *= math.Max(ev.Score, 1e-6)
		out.Reasons = append(out.Reasons, ev.Reasons...)
	}
	out.Score = math.Pow(prod, 1/float64(len(evs)))
	return out
}

// mergeEvents joins moments closer than g seconds (max score, all reasons).
func mergeEvents(evs []Event, g float64) []Event {
	if len(evs) == 0 {
		return nil
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].Start < evs[j].Start })
	out := []Event{evs[0]}
	for _, ev := range evs[1:] {
		cur := &out[len(out)-1]
		if ev.Start <= cur.End+g {
			cur.End = math.Max(cur.End, ev.End)
			cur.Score = math.Max(cur.Score, ev.Score)
			if len(cur.Reasons) < 24 {
				cur.Reasons = append(cur.Reasons, ev.Reasons...)
			}
			continue
		}
		out = append(out, ev)
	}
	return out
}

func andEvents(kids [][]Event, window float64) []Event {
	var out []Event
	for _, a := range kids[0] {
		parts := []Event{a}
		ok := true
		for _, other := range kids[1:] {
			best, found := Event{}, false
			for _, b := range other {
				if gap(a, b) <= window && (!found || b.Score > best.Score) {
					best, found = b, true
				}
			}
			if !found {
				ok = false
				break
			}
			parts = append(parts, best)
		}
		if ok {
			out = append(out, joinEvents(parts...))
		}
	}
	return mergeEvents(out, 0)
}

func thenEvents(kids [][]Event, window float64) []Event {
	var out []Event
	for _, a := range kids[0] {
		chain := []Event{a}
		prev := a
		ok := true
		for _, next := range kids[1:] {
			best, found := Event{}, false
			for _, b := range next {
				// b follows prev: starts after prev starts, ends after it, and
				// no later than window seconds after prev ended.
				if b.Start >= prev.Start && b.End > prev.End-0.01 && b.Start-prev.End <= window {
					if !found || b.Start < best.Start || (b.Start == best.Start && b.Score > best.Score) {
						best, found = b, true
					}
				}
			}
			if !found {
				ok = false
				break
			}
			chain = append(chain, best)
			prev = best
		}
		if ok {
			out = append(out, joinEvents(chain...))
		}
	}
	return mergeEvents(out, 0)
}

func withoutEvents(kids [][]Event, window float64) []Event {
	var out []Event
outer:
	for _, a := range kids[0] {
		for _, other := range kids[1:] {
			for _, b := range other {
				if gap(a, b) <= window {
					continue outer
				}
			}
		}
		out = append(out, a)
	}
	return out
}

// --- detector conditions ---

func (e *Env) leaf(n *Node) []Event {
	a := e.A
	switch n.Kind {
	case "audio_event":
		return e.audioEvent(n)
	case "audio_description":
		if a.AudioEmb == nil {
			e.warn("Described sounds were not measured (CLAP model)")
			return nil
		}
		return e.zeroShot(n, "clap", a.AudioEmb, []string{"background noise", "people talking", "silence", "music playing"}, "sound like")
	case "loudness":
		return e.loudness(n)
	case "music_change":
		return e.musicChange(n)
	case "speech":
		return e.speech(n)
	case "question":
		return e.questions()
	case "keyword":
		return e.keywords(n)
	case "profanity":
		return e.profanity()
	case "repeated_phrase":
		return e.repeats(n)
	case "dialogue":
		return e.dialogue(n)
	case "topic_shift":
		return e.topicShift()
	case "emotion":
		return e.emotion(n)
	case "visual_description":
		if a.Frames == nil {
			e.warn("Described scenes were not measured (CLIP model)")
			return nil
		}
		return e.zeroShot(n, "clip", a.Frames, []string{"a video frame", "a screenshot", "a photo of a person", "a photo of a room"}, "looks like")
	case "faces":
		return e.faces(n)
	case "expression":
		return e.expression(n)
	case "motion":
		return e.motion(n)
	case "scene_change":
		return e.sceneChange()
	case "onscreen_text":
		return e.onscreenText(n)
	case "score_change":
		return e.scoreChange(n)
	case "killfeed":
		return e.killfeed(n)
	}
	return nil
}

func ev(kind, det string, s, end, score float64, text string) Event {
	return Event{Start: s, End: end, Score: score, Reasons: []Reason{{Kind: kind, Detector: det, Text: text, Score: math.Round(score*100) / 100, Start: s, End: end}}}
}

// runs turns per-row scores into moments: rows scoring at least thr, rows
// closer than maxGap joined; row i covers span(i).
func runs(scores []float64, thr, maxGap float64, span func(i int) (float64, float64), mk func(s, e, score float64, best int) Event) []Event {
	var out []Event
	start, best := -1, -1
	var s0, e0 float64
	flush := func() {
		if start >= 0 {
			out = append(out, mk(s0, e0, scores[best], best))
		}
		start, best = -1, -1
	}
	for i, v := range scores {
		if v < thr {
			continue
		}
		s, en := span(i)
		if start >= 0 && s > e0+maxGap {
			flush()
		}
		if start < 0 {
			start, best, s0 = i, i, s
		}
		if v > scores[best] {
			best = i
		}
		e0 = math.Max(e0, en)
	}
	flush()
	return out
}

func (e *Env) audioEvent(n *Node) []Event {
	m := e.A.Events
	if m == nil {
		e.warn("Sound events were not measured (AST model)")
		return nil
	}
	cls := soundClasses(n.Labels)
	var cols [][]float64
	var names []string
	for _, c := range cls {
		if col := m.Column(c); col != nil {
			cols, names = append(cols, col), append(names, c)
		}
	}
	if len(cols) == 0 {
		e.warn("The sound model has no class %q", strings.Join(n.Labels, ", "))
		return nil
	}
	comb := make([]float64, len(m.Rows))
	which := make([]int, len(m.Rows))
	for i := range comb {
		for k, col := range cols {
			if col[i] > comb[i] {
				comb[i], which[i] = col[i], k
			}
		}
	}
	thr := n.MinScore
	if thr == 0 {
		thr = kinds["audio_event"].DefaultScore
	}
	return runs(comb, thr, m.Hop, func(i int) (float64, float64) {
		return float64(i) * m.Hop, float64(i)*m.Hop + m.Window
	}, func(s, en, score float64, best int) Event {
		return ev("audio_event", DAudioEvents, s, en, score, fmt.Sprintf("%s heard (%.0f%%)", names[which[best]], score*100))
	})
}

// zeroShot scores each embedding row against the description with the
// neutral prompts as alternatives (softmax, as zero-shot classification).
func (e *Env) zeroShot(n *Node, model string, v *Vecs, neutral []string, verb string) []Event {
	if e.Embed == nil {
		e.warn("The smart processing worker is not reachable for text queries")
		return nil
	}
	texts := append([]string{n.Text}, neutral...)
	embs, err := e.Embed.Text(model, texts)
	if err != nil || len(embs) != len(texts) {
		e.warn("Text query failed (%s): %v", model, err)
		return nil
	}
	q := embs[0]
	var alts [][]float32
	for _, a := range embs[1:] {
		if Dot(q, a) < 0.9 { // a neutral prompt that means the same is no alternative
			alts = append(alts, a)
		}
	}
	scale := e.Embed.Scale(model)
	if scale <= 0 {
		scale = 100
	}
	scores := make([]float64, len(v.Data))
	for i, row := range v.Data {
		sq := Dot(q, row) * scale
		den := 1.0
		for _, a := range alts {
			den += math.Exp(Dot(a, row)*scale - sq)
		}
		scores[i] = 1 / den
	}
	// Light smoothing: neighbouring frames or windows agree.
	sm := make([]float64, len(scores))
	for i := range scores {
		sum, k := scores[i]*2, 2.0
		if i > 0 {
			sum, k = sum+scores[i-1], k+1
		}
		if i+1 < len(scores) {
			sum, k = sum+scores[i+1], k+1
		}
		sm[i] = sum / k
	}
	hop := v.hop()
	win := v.Window
	if win <= 0 {
		win = hop
	}
	thr := n.MinScore
	if thr == 0 {
		thr = kinds[n.Kind].DefaultScore
	}
	det := DFrames
	if model == "clap" {
		det = DAudioCLAP
	}
	return runs(sm, thr, hop, func(i int) (float64, float64) {
		t := v.Times[i]
		if model == "clip" {
			return t - hop/2, t + hop/2
		}
		return t, t + win
	}, func(s, en, score float64, best int) Event {
		return ev(n.Kind, det, math.Max(s, 0), en, score, fmt.Sprintf("%s %q (%.0f%%)", verb, n.Text, score*100))
	})
}

func (v *Vecs) hop() float64 {
	if len(v.Times) >= 2 {
		return math.Max(v.Times[1]-v.Times[0], 0.05)
	}
	return 1
}

func (e *Env) loudness(n *Node) []Event {
	a := e.A.Audio
	if a == nil || len(a.Loudness.Values) == 0 {
		if !e.A.HasAudio {
			e.warn("The recording has no audio")
		} else {
			e.warn("Loudness was not measured")
		}
		return nil
	}
	L := floats(a.Loudness.Values)
	rate := a.Loudness.Rate
	floor := noiseFloor(a)
	span := func(i int) (float64, float64) { return float64(i) / rate, float64(i+1) / rate }
	minDur := n.MinDuration
	switch n.Mode {
	case "loud":
		thr := n.DB
		if thr == 0 {
			thr = math.Max(floor+20, percentile(L, 0.9))
		}
		sc := make([]float64, len(L))
		for i, l := range L {
			if l >= thr {
				sc[i] = clamp01(0.5 + (l-thr)/20)
			}
		}
		return runs(sc, 0.01, 0.5, span, func(s, en, score float64, best int) Event {
			return ev("loudness", DAudioDSP, s, en, score, fmt.Sprintf("loud: %.0f dBFS (%.0f dB above the background)", L[best], L[best]-floor))
		})
	case "quiet", "silence":
		thr := n.DB
		if thr == 0 {
			if n.Mode == "silence" {
				thr = math.Max(-60, floor+3)
			} else {
				thr = floor + 6
			}
		}
		minDur = math.Max(minDur, map[string]float64{"quiet": 2, "silence": 1.5}[n.Mode])
		sc := make([]float64, len(L))
		for i, l := range L {
			if l <= thr {
				sc[i] = 1
			}
		}
		evs := runs(sc, 0.5, 0.15, span, func(s, en, _ float64, _ int) Event {
			score := clamp01(0.55 + (en-s)/30)
			return ev("loudness", DAudioDSP, s, en, score, fmt.Sprintf("%s for %.1f s", n.Mode, en-s))
		})
		kept := evs[:0]
		for _, x := range evs {
			if x.End-x.Start >= minDur {
				kept = append(kept, x)
			}
		}
		return kept
	case "sudden_rise", "sudden_drop":
		r := n.DB
		if r == 0 {
			r = 15
		}
		look := int(1.5 * rate)
		var out []Event
		for i := look; i < len(L); i++ {
			ref := L[i-1]
			for j := i - look; j < i; j++ {
				if n.Mode == "sudden_rise" {
					ref = math.Min(ref, L[j])
				} else {
					ref = math.Max(ref, L[j])
				}
			}
			t := float64(i) / rate
			if n.Mode == "sudden_rise" && L[i]-ref >= r && L[i] >= floor+15 {
				end := i
				for end+1 < len(L) && L[end+1] >= L[i]-10 && float64(end-i)/rate < 5 {
					end++
				}
				out = append(out, ev("loudness", DAudioDSP, t-0.3, float64(end+1)/rate, clamp01((L[i]-ref)/30+0.2), fmt.Sprintf("sudden jump of %.0f dB", L[i]-ref)))
				i = end + look
			}
			if n.Mode == "sudden_drop" && ref-L[i] >= r {
				out = append(out, ev("loudness", DAudioDSP, t-1, t+1, clamp01((ref-L[i])/30+0.2), fmt.Sprintf("sudden drop of %.0f dB", ref-L[i])))
				i += look
			}
		}
		return out
	}
	return nil
}

func (e *Env) musicChange(n *Node) []Event {
	m := e.A.Events
	if m == nil {
		e.warn("Sound events were not measured (AST model)")
		return nil
	}
	col := m.Column("Music")
	if col == nil {
		return nil
	}
	sm := medianFilter(col, 3)
	var out []Event
	on := false
	since := 0
	for i, v := range sm {
		t := float64(i) * m.Hop
		switch {
		case !on && v >= 0.5:
			if float64(i-since)*m.Hop >= 3 && i > 0 && n.Mode == "start" {
				out = append(out, ev("music_change", DAudioEvents, t-1.5, t+1.5, v, "music starts"))
			}
			on, since = true, i
		case on && v <= 0.2:
			if float64(i-since)*m.Hop >= 3 && n.Mode == "stop" {
				out = append(out, ev("music_change", DAudioEvents, t-1.5, t+1.5, clamp01(1-v), "music stops"))
			}
			on, since = false, i
		}
	}
	return out
}

func medianFilter(v []float64, w int) []float64 {
	out := make([]float64, len(v))
	h := w / 2
	for i := range v {
		lo, hi := max(0, i-h), min(len(v), i+h+1)
		win := append([]float64(nil), v[lo:hi]...)
		sort.Float64s(win)
		out[i] = win[len(win)/2]
	}
	return out
}

// speechSpans are the speech stretches from the best source measured.
func (e *Env) speechSpans() ([]Interval, string) {
	a := e.A
	if a.Speech != nil {
		return mergeIntervals(a.Speech, 0.6), DVAD
	}
	if a.Transcript != nil {
		var out []Interval
		for _, u := range a.Transcript.Utterances {
			if u.NoSpeech < 0.8 {
				out = append(out, Interval{Start: u.Start, End: u.End, Score: 0.7})
			}
		}
		return mergeIntervals(out, 0.6), DTranscript
	}
	if a.Audio != nil {
		return EnergySpeech(a.Audio), DAudioDSP
	}
	return nil, ""
}

func mergeIntervals(in []Interval, g float64) []Interval {
	if len(in) == 0 {
		return nil
	}
	s := append([]Interval(nil), in...)
	sort.Slice(s, func(i, j int) bool { return s[i].Start < s[j].Start })
	out := []Interval{s[0]}
	for _, x := range s[1:] {
		cur := &out[len(out)-1]
		if x.Start <= cur.End+g {
			cur.End = math.Max(cur.End, x.End)
			cur.Score = math.Max(cur.Score, x.Score)
			continue
		}
		out = append(out, x)
	}
	return out
}

func (e *Env) speech(n *Node) []Event {
	if n.Mode == "overlap" || n.Mode == "speaker_change" {
		return e.turns(n)
	}
	sp, det := e.speechSpans()
	if det == "" {
		e.warn("Speech was not measured")
		return nil
	}
	if det == DAudioDSP {
		e.warn("Speech is estimated from loudness (install the speech model for accurate voice activity)")
	}
	base := 0.75
	if det == DAudioDSP {
		base = 0.5
	}
	var out []Event
	switch n.Mode {
	case "active":
		for _, s := range mergeIntervals(sp, 1.5) {
			out = append(out, ev("speech", det, s.Start, s.End, base, fmt.Sprintf("speech for %.0f s", s.End-s.Start)))
		}
	case "start", "end":
		for i, s := range sp {
			if n.Mode == "start" && i > 0 && s.Start-sp[i-1].End >= 3 {
				out = append(out, ev("speech", det, s.Start, s.Start+2, base, fmt.Sprintf("speech starts after %.0f s", s.Start-sp[i-1].End)))
			}
			if n.Mode == "end" && i+1 < len(sp) && sp[i+1].Start-s.End >= 3 {
				out = append(out, ev("speech", det, math.Max(s.End-2, s.Start), s.End, base, fmt.Sprintf("speech stops for %.0f s", sp[i+1].Start-s.End)))
			}
			if n.Mode == "end" && i+1 == len(sp) && e.A.Duration-s.End >= 3 {
				out = append(out, ev("speech", det, math.Max(s.End-2, s.Start), s.End, base, "speech ends"))
			}
		}
	case "awkward_silence":
		minGap := n.MinDuration
		if minGap == 0 {
			minGap = 3
		}
		var music []float64
		var hop float64
		if m := e.A.Events; m != nil {
			music, hop = m.Column("Music"), m.Hop
		}
		var floor float64
		if e.A.Audio != nil {
			floor = noiseFloor(e.A.Audio)
		}
		for i := 1; i < len(sp); i++ {
			g0, g1 := sp[i-1].End, sp[i].Start
			if g1-g0 < minGap || g1-g0 > 60 {
				continue
			}
			if speechWithin(sp, g0-30, g0) < 3 || speechWithin(sp, g1, g1+30) < 2 {
				continue // not a conversation on both sides
			}
			if a := e.A.Audio; a != nil {
				if meanSeries(&a.Loudness, g0, g1) > floor+12 {
					continue // something else is loud
				}
			}
			if music != nil && meanCol(music, hop, g0, g1) > 0.3 {
				continue
			}
			score := clamp01(0.45 + (g1-g0-minGap)/10)
			out = append(out, ev("speech", det, g0, g1, score, fmt.Sprintf("%.1f s of silence in a conversation", g1-g0)))
		}
	}
	return out
}

func speechWithin(sp []Interval, a, b float64) float64 {
	var t float64
	for _, s := range sp {
		t += math.Max(0, math.Min(s.End, b)-math.Max(s.Start, a))
	}
	return t
}

func meanSeries(s *Series, a, b float64) float64 {
	i0, i1 := int(a*s.Rate), int(math.Ceil(b*s.Rate))
	i0, i1 = max(i0, 0), min(i1, len(s.Values))
	if i1 <= i0 {
		return s.At(a)
	}
	var sum float64
	for _, v := range s.Values[i0:i1] {
		sum += float64(v)
	}
	return sum / float64(i1-i0)
}

func meanCol(col []float64, hop, a, b float64) float64 {
	i0, i1 := int(a/hop), int(math.Ceil(b/hop))
	i0, i1 = max(i0, 0), min(i1, len(col))
	if i1 <= i0 {
		return 0
	}
	var sum float64
	for _, v := range col[i0:i1] {
		sum += v
	}
	return sum / float64(i1-i0)
}

func (e *Env) transcript() *Transcript {
	if e.A.Transcript == nil {
		e.warn("Speech was not transcribed (Whisper model)")
		return nil
	}
	return e.A.Transcript
}

func (e *Env) turns(n *Node) []Event {
	tr := e.transcript()
	if tr == nil {
		return nil
	}
	if tr.Speakers < 2 {
		if tr.Speakers == 0 {
			e.warn("Speakers were not told apart (voice embedding model)")
		}
		return nil
	}
	u := tr.Utterances
	var out []Event
	if n.Mode == "speaker_change" {
		for i := 1; i < len(u); i++ {
			if u[i].Speaker > 0 && u[i-1].Speaker > 0 && u[i].Speaker != u[i-1].Speaker && u[i].Start-u[i-1].End < 5 {
				out = append(out, ev("speech", DTranscript, u[i].Start-1, u[i].Start+2, 0.6, fmt.Sprintf("speaker %d replies to speaker %d", u[i].Speaker, u[i-1].Speaker)))
			}
		}
		return out
	}
	// overlap: several quick turn changes or interruptions within 10 s.
	var switches []float64
	for i := 1; i < len(u); i++ {
		if u[i].Speaker > 0 && u[i-1].Speaker > 0 && u[i].Speaker != u[i-1].Speaker && u[i].Start < u[i-1].End+0.4 {
			switches = append(switches, u[i].Start)
		}
	}
	for i := 0; i < len(switches); {
		j := i
		for j+1 < len(switches) && switches[j+1]-switches[i] <= 10 {
			j++
		}
		if c := j - i + 1; c >= 3 {
			out = append(out, ev("speech", DTranscript, switches[i]-1, switches[j]+2, clamp01(0.3+0.12*float64(c)), fmt.Sprintf("%d interruptions or quick exchanges", c)))
		}
		i = j + 1
	}
	return out
}

var interrogatives = map[string]bool{"what": true, "why": true, "how": true, "who": true, "where": true, "when": true, "which": true, "whose": true,
	"do": true, "does": true, "did": true, "are": true, "is": true, "can": true, "could": true, "would": true, "will": true, "should": true, "have": true, "has": true, "were": true, "was": true}

func (e *Env) questions() []Event {
	tr := e.transcript()
	if tr == nil {
		return nil
	}
	var out []Event
	for _, u := range tr.Utterances {
		txt := strings.TrimSpace(u.Text)
		ws := tokens(txt)
		if len(ws) < 3 {
			continue
		}
		switch {
		case strings.HasSuffix(txt, "?"):
			out = append(out, ev("question", DTranscript, u.Start, u.End, 0.8, fmt.Sprintf("question: %q", clipText(txt))))
		case interrogatives[ws[0]]:
			out = append(out, ev("question", DTranscript, u.Start, u.End, 0.55, fmt.Sprintf("likely question: %q", clipText(txt))))
		}
	}
	return out
}

func clipText(s string) string {
	if r := []rune(s); len(r) > 90 {
		return string(r[:87]) + "…"
	}
	return s
}

type timedWord struct {
	w    string
	s, e float64
	p    float64
}

// words returns every transcribed word with its time.
func (t *Transcript) words() []timedWord {
	var out []timedWord
	for _, u := range t.Utterances {
		if len(u.Words) > 0 {
			for _, w := range u.Words {
				if n := normWord(w.W); n != "" {
					out = append(out, timedWord{n, w.S, w.E, w.P})
				}
			}
			continue
		}
		ws := tokens(u.Text)
		if len(ws) == 0 {
			continue
		}
		step := (u.End - u.Start) / float64(len(ws))
		for i, w := range ws {
			out = append(out, timedWord{w, u.Start + float64(i)*step, u.Start + float64(i+1)*step, 0.8})
		}
	}
	return out
}

func wordMatch(term, w string) bool {
	if strings.HasSuffix(term, "*") {
		return strings.HasPrefix(w, strings.TrimSuffix(term, "*"))
	}
	return term == w
}

func (e *Env) keywords(n *Node) []Event {
	tr := e.transcript()
	if tr == nil {
		return nil
	}
	ws := tr.words()
	terms := cleanTerms(n.Terms)
	type hit struct {
		term int
		s, e float64
		p    float64
	}
	var hits []hit
	for ti, t := range terms {
		for i := 0; i+len(t) <= len(ws); i++ {
			ok := true
			for k := range t {
				if !wordMatch(t[k], ws[i+k].w) {
					ok = false
					break
				}
			}
			if ok {
				p := 0.0
				for k := range t {
					p += ws[i+k].p
				}
				hits = append(hits, hit{ti, ws[i].s, ws[i+len(t)-1].e, p / float64(len(t))})
			}
		}
	}
	score := func(p float64) float64 {
		if p <= 0 {
			return 0.85
		}
		return clamp01(0.5 + 0.45*p)
	}
	var out []Event
	if n.Match == "all" && len(terms) > 1 {
		win := n.Window
		if win == 0 {
			win = 20
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].s < hits[j].s })
		for i, h := range hits {
			seen := map[int]bool{}
			end, sum := h.e, 0.0
			for _, g := range hits[i:] {
				if g.s-h.s > win {
					break
				}
				if !seen[g.term] {
					seen[g.term] = true
					end, sum = math.Max(end, g.e), sum+score(g.p)
				}
			}
			if len(seen) == len(terms) {
				out = append(out, ev("keyword", DTranscript, h.s, end, sum/float64(len(terms)), "said: "+strings.Join(n.Terms, ", ")))
			}
		}
		return mergeEvents(out, 0)
	}
	for _, h := range hits {
		out = append(out, ev("keyword", DTranscript, h.s, h.e, score(h.p), fmt.Sprintf("said %q", strings.Join(terms[h.term], " "))))
	}
	return mergeEvents(out, 1)
}

func (e *Env) profanity() []Event {
	tr := e.transcript()
	if tr == nil {
		return nil
	}
	var out []Event
	for _, w := range tr.words() {
		if profane(w.w) {
			out = append(out, ev("profanity", DTranscript, w.s-0.3, w.e+0.3, 0.85, "profanity"))
		}
	}
	return mergeEvents(out, 2)
}

var stopWords = map[string]bool{"a": true, "the": true, "i": true, "you": true, "it": true, "to": true, "and": true, "of": true, "is": true, "that": true,
	"like": true, "um": true, "uh": true, "yeah": true, "oh": true, "so": true, "in": true, "me": true, "my": true, "we": true, "he": true, "she": true, "what": true, "no": true, "yes": true, "okay": true, "ok": true}

func (e *Env) repeats(n *Node) []Event {
	tr := e.transcript()
	if tr == nil {
		return nil
	}
	size := 3
	if n.Count >= 2 && n.Count <= 8 {
		size = n.Count
	}
	ws := tr.words()
	pos := map[string][]int{}
	for i := 0; i+size <= len(ws); i++ {
		parts := make([]string, size)
		content := false
		for k := range parts {
			parts[k] = ws[i+k].w
			if !stopWords[parts[k]] {
				content = true
			}
		}
		if content {
			g := strings.Join(parts, " ")
			pos[g] = append(pos[g], i)
		}
	}
	var out []Event
	for g, idx := range pos {
		for k := 1; k < len(idx); k++ {
			if idx[k]-idx[k-1] < size {
				continue // overlapping n-grams of one utterance
			}
			if ws[idx[k]].s-ws[idx[k-1]].s <= 60 {
				out = append(out, ev("repeated_phrase", DTranscript, ws[idx[k]].s, ws[idx[k]+size-1].e, clamp01(0.55+0.1*float64(k)), fmt.Sprintf("%q said again", g)))
			}
		}
	}
	return mergeEvents(out, 1)
}

func (e *Env) dialogue(n *Node) []Event {
	tr := e.transcript()
	if tr == nil {
		return nil
	}
	if tr.Embeddings == nil || len(tr.Embeddings.Data) != len(tr.Utterances) {
		e.warn("Sentence embeddings were not computed (MiniLM model)")
		return nil
	}
	if e.Embed == nil {
		e.warn("The smart processing worker is not reachable for text queries")
		return nil
	}
	q, err := e.Embed.Text("minilm", []string{n.Text})
	if err != nil || len(q) != 1 {
		e.warn("Text query failed (minilm): %v", err)
		return nil
	}
	sims := make([]float64, len(tr.Utterances))
	for i, row := range tr.Embeddings.Data {
		sims[i] = Dot(q[0], row)
	}
	var out []Event
	for i, u := range tr.Utterances {
		s := sims[i]
		// A sentence in a matching stretch of conversation counts too.
		if i > 0 && i+1 < len(sims) {
			s = math.Max(s, (sims[i-1]+sims[i]+sims[i+1])/3)
		}
		score := clamp01((s - 0.15) / 0.45)
		out = append(out, ev("dialogue", DTranscript, u.Start, u.End, score, fmt.Sprintf("about %q: %q", n.Text, clipText(strings.TrimSpace(u.Text)))))
	}
	thr := n.MinScore
	if thr == 0 {
		thr = kinds["dialogue"].DefaultScore
	}
	kept := out[:0]
	for _, x := range out {
		if x.Score >= thr {
			kept = append(kept, x)
		}
	}
	return mergeEvents(kept, 3)
}

func (e *Env) topicShift() []Event {
	tr := e.transcript()
	if tr == nil {
		return nil
	}
	if tr.Embeddings == nil || len(tr.Embeddings.Data) != len(tr.Utterances) {
		e.warn("Sentence embeddings were not computed (MiniLM model)")
		return nil
	}
	u, d := tr.Utterances, tr.Embeddings.Data
	mean := func(a, b int) []float32 {
		v := make([]float32, tr.Embeddings.Dim)
		for _, row := range d[a:b] {
			for k := range v {
				v[k] += row[k]
			}
		}
		Normalize(v)
		return v
	}
	const w = 3
	cos := make([]float64, len(u))
	for i := range cos {
		cos[i] = 1
	}
	for i := w; i+w <= len(u); i++ {
		if u[i-1].End-u[i-w].Start < 4 || u[i+w-1].End-u[i].Start < 4 {
			continue
		}
		cos[i] = Dot(mean(i-w, i), mean(i, i+w))
	}
	var out []Event
	for i := w; i+w <= len(u); i++ {
		if cos[i] < cos[i-1] && cos[i] <= cos[min(i+1, len(cos)-1)] {
			if score := clamp01((0.55 - cos[i]) / 0.4); score > 0 {
				out = append(out, ev("topic_shift", DTranscript, math.Max(u[i].Start-2, 0), u[i].End, score, fmt.Sprintf("conversation turns to: %q", clipText(strings.TrimSpace(u[i].Text)))))
			}
		}
	}
	return out
}

func (e *Env) emotion(n *Node) []Event {
	em := e.A.Emotion
	if em == nil {
		e.warn("Speech emotion was not measured (emotion model)")
		return nil
	}
	k := -1
	for i, l := range em.Labels {
		if l == n.Label {
			k = i
		}
	}
	if k < 0 {
		return nil
	}
	var out []Event
	for _, s := range em.Spans {
		if k < len(s.Scores) {
			v := float64(s.Scores[k])
			out = append(out, ev("emotion", DEmotion, s.Start, s.End, v, fmt.Sprintf("voice sounds %s (%.0f%%, uncertain)", n.Label, v*100)))
		}
	}
	return mergeEvents(out, 1)
}

func (e *Env) faceFrames() []FaceFrame {
	if e.A.Faces == nil {
		e.warn("Faces were not detected (face model)")
		return nil
	}
	return e.A.Faces.Frames
}

func frameStep(fr []FaceFrame) float64 {
	if len(fr) >= 2 {
		return math.Max(fr[1].T-fr[0].T, 0.1)
	}
	return 1
}

func (e *Env) faces(n *Node) []Event {
	fr := e.faceFrames()
	if len(fr) == 0 {
		return nil
	}
	dt := frameStep(fr)
	cnt := make([]float64, len(fr))
	for i, f := range fr {
		cnt[i] = float64(f.Count)
	}
	c := medianFilter(cnt, 3)
	span := func(i int) (float64, float64) { return fr[i].T - dt/2, fr[i].T + dt/2 }
	cond := func(f func(v float64) bool, label string, minDur float64) []Event {
		sc := make([]float64, len(c))
		for i, v := range c {
			if f(v) {
				sc[i] = 1
			}
		}
		evs := runs(sc, 0.5, dt*1.5, span, func(s, en, _ float64, _ int) Event {
			return ev("faces", DFaces, s, en, 0.7, label)
		})
		kept := evs[:0]
		for _, x := range evs {
			if x.End-x.Start >= minDur {
				kept = append(kept, x)
			}
		}
		return kept
	}
	switch n.Mode {
	case "present":
		return cond(func(v float64) bool { return v >= 1 }, "a face is visible", 0)
	case "absent":
		return cond(func(v float64) bool { return v == 0 }, "nobody in frame", math.Max(n.MinDuration, 2))
	case "multiple":
		return cond(func(v float64) bool { return v >= 2 }, "several people in frame", 0)
	}
	// enter, leave, count_change: a count that held for 2 frames changes and
	// holds for 2 frames.
	var out []Event
	for i := 2; i+1 < len(c); i++ {
		before, after := c[i-1], c[i]
		if before == after || c[i-2] != before || c[i+1] != after {
			continue
		}
		t := fr[i].T
		switch {
		case after > before && (n.Mode == "enter" || n.Mode == "count_change"):
			out = append(out, ev("faces", DFaces, t-2, t+3, 0.7, fmt.Sprintf("someone appears (%v → %v faces)", before, after)))
		case after < before && (n.Mode == "leave" || n.Mode == "count_change"):
			out = append(out, ev("faces", DFaces, t-3, t+2, 0.7, fmt.Sprintf("someone leaves the frame (%v → %v faces)", before, after)))
		}
	}
	return out
}

func (e *Env) expression(n *Node) []Event {
	fr := e.faceFrames()
	if len(fr) == 0 {
		return nil
	}
	k := -1
	for i, l := range e.A.Faces.ExprLabels {
		if l == n.Label {
			k = i
		}
	}
	if k < 0 {
		e.warn("Expressions were not read (CLIP model)")
		return nil
	}
	dt := frameStep(fr)
	sc := make([]float64, len(fr))
	for i, f := range fr {
		if f.Count > 0 && k < len(f.Expr) {
			sc[i] = float64(f.Expr[k])
		}
	}
	sm := make([]float64, len(sc))
	for i := range sc {
		lo, hi := max(0, i-1), min(len(sc), i+2)
		var s float64
		for _, v := range sc[lo:hi] {
			s += v
		}
		sm[i] = s / float64(hi-lo)
	}
	thr := n.MinScore
	if thr == 0 {
		thr = kinds["expression"].DefaultScore
	}
	return runs(sm, thr, dt*1.5, func(i int) (float64, float64) { return fr[i].T - dt/2, fr[i].T + dt/2 }, func(s, en, score float64, _ int) Event {
		return ev("expression", DFaces, s, en, score, fmt.Sprintf("face looks %s (%.0f%%, uncertain)", n.Label, score*100))
	})
}

func (e *Env) motion(n *Node) []Event {
	v := e.A.Video
	if v == nil || len(v.Motion.Values) == 0 {
		e.warn("Movement was not measured")
		return nil
	}
	M := floats(v.Motion.Values)
	rate := v.Motion.Rate
	med, p90, p95, p99 := percentile(M, 0.5), percentile(M, 0.9), percentile(M, 0.95), percentile(M, 0.99)
	span := func(i int) (float64, float64) { return float64(i) / rate, float64(i+1) / rate }
	switch n.Mode {
	case "sudden":
		var out []Event
		look := int(2 * rate)
		for i := look; i < len(M); i++ {
			var prev float64
			for _, x := range M[i-look : i] {
				prev += x
			}
			prev /= float64(look)
			if M[i] >= math.Max(math.Max(3*med, p95), 0.04) && M[i]-prev >= math.Max(2*med, 0.03) {
				t := float64(i) / rate
				out = append(out, ev("motion", DVideoDSP, t-0.5, t+1.5, clamp01(0.4+M[i]/(p99+1e-6)*0.6), "sudden movement"))
				i += look
			}
		}
		return out
	case "high":
		thr := math.Max(p90, 2*med)
		sc := make([]float64, len(M))
		for i, x := range M {
			if x >= thr && x > 0.01 {
				sc[i] = clamp01(0.5 + (x-thr)/(p99-thr+1e-6)*0.5)
			}
		}
		evs := runs(sc, 0.01, 0.5, span, func(s, en, score float64, _ int) Event {
			return ev("motion", DVideoDSP, s, en, score, "lots of movement")
		})
		kept := evs[:0]
		for _, x := range evs {
			if x.End-x.Start >= math.Max(n.MinDuration, 1) {
				kept = append(kept, x)
			}
		}
		return kept
	case "still":
		thr := math.Max(0.25*med, 0.002)
		sc := make([]float64, len(M))
		for i, x := range M {
			if x <= thr {
				sc[i] = 1
			}
		}
		evs := runs(sc, 0.5, 0.3, span, func(s, en, _ float64, _ int) Event {
			return ev("motion", DVideoDSP, s, en, 0.6, fmt.Sprintf("still picture for %.0f s", en-s))
		})
		kept := evs[:0]
		for _, x := range evs {
			if x.End-x.Start >= math.Max(n.MinDuration, 3) {
				kept = append(kept, x)
			}
		}
		return kept
	}
	return nil
}

func (e *Env) sceneChange() []Event {
	v := e.A.Video
	if v == nil || len(v.Scene.Values) == 0 {
		e.warn("Scene changes were not measured")
		return nil
	}
	var out []Event
	rate := v.Scene.Rate
	for i, s := range v.Scene.Values {
		if score := clamp01(float64(s) * 1.6); score >= 0.2 {
			t := float64(i) / rate
			out = append(out, ev("scene_change", DVideoDSP, math.Max(t-0.25, 0), t+0.25, score, fmt.Sprintf("scene change (%.0f%% of the picture)", float64(s)*100)))
		}
	}
	// One cut per second: the strongest.
	var kept []Event
	for _, x := range out {
		if n := len(kept); n > 0 && x.Start-kept[n-1].Start < 1 {
			if x.Score > kept[n-1].Score {
				kept[n-1] = x
			}
			continue
		}
		kept = append(kept, x)
	}
	return kept
}

func ocrStep(fr []OCRFrame) float64 {
	if len(fr) >= 2 {
		return math.Max(fr[1].T-fr[0].T, 0.1)
	}
	return 2
}

func (e *Env) ocrFrames() []OCRFrame {
	if e.A.OCR == nil {
		e.warn("On-screen text was not read (OCR)")
		return nil
	}
	return e.A.OCR
}

// regionWords returns the words of a frame inside r.
func regionWords(f OCRFrame, r *Rect, minConf float32) []OCRWord {
	var out []OCRWord
	for _, w := range f.Words {
		if w.Conf >= minConf && r.contains(w.Box) {
			out = append(out, w)
		}
	}
	return out
}

func (e *Env) onscreenText(n *Node) []Event {
	fr := e.ocrFrames()
	if len(fr) == 0 {
		return nil
	}
	dt := ocrStep(fr)
	var out []Event
	if n.Text != "" {
		var alts [][]string
		for _, a := range strings.Split(n.Text, "|") {
			if t := tokens(a); len(t) > 0 {
				alts = append(alts, t)
			}
		}
		for _, f := range fr {
			ws := regionWords(f, n.Region, 45)
			toks := make([]string, 0, len(ws))
			var conf float64
			for _, w := range ws {
				if t := normWord(w.Text); t != "" {
					toks = append(toks, t)
					conf += float64(w.Conf)
				}
			}
			for _, alt := range alts {
				if seqMatch(toks, alt) {
					c := 70.0
					if len(toks) > 0 {
						c = conf / float64(len(toks))
					}
					out = append(out, ev("onscreen_text", DOCR, f.T-dt/2, f.T+dt/2, clamp01(0.55+0.4*c/100), fmt.Sprintf("on screen: %q", strings.Join(alt, " "))))
					break
				}
			}
		}
		return mergeEvents(out, dt*1.5)
	}
	var prev [2]map[string]bool
	for _, f := range fr {
		cur := map[string]bool{}
		var fresh []string
		for _, w := range regionWords(f, n.Region, 60) {
			t := normWord(w.Text)
			if len([]rune(t)) < 3 {
				continue
			}
			cur[t] = true
			if !prev[0][t] && !prev[1][t] {
				fresh = append(fresh, t)
			}
		}
		if len(fresh) >= 3 && prev[0] != nil {
			out = append(out, ev("onscreen_text", DOCR, f.T-dt/2, f.T+dt, clamp01(0.5+float64(len(fresh))/20), "text appears: "+clipText(strings.Join(fresh, " "))))
		}
		prev[1], prev[0] = prev[0], cur
	}
	return out
}

// seqMatch finds want as consecutive tokens, tolerating one OCR error in
// words of five letters or more.
func seqMatch(have, want []string) bool {
	for i := 0; i+len(want) <= len(have); i++ {
		ok := true
		for k, w := range want {
			h := have[i+k]
			if h != w && !(len([]rune(w)) >= 5 && editDistance(h, w) <= 1) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// ocrDigits reads a number OCR may have garbled (O for 0, l for 1).
func ocrDigits(s string) (int, bool) {
	s = strings.NewReplacer("O", "0", "o", "0", "l", "1", "I", "1", "|", "1", "S", "5").Replace(strings.TrimSpace(s))
	n := 0
	if s == "" || len(s) > 3 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// DefaultScoreRegion: where Rocket League and many sports games show the
// score (top centre).
var DefaultScoreRegion = Rect{X: 0.35, Y: 0, W: 0.3, H: 0.12}

// DefaultKillfeedRegion: top right, where most shooters list eliminations.
var DefaultKillfeedRegion = Rect{X: 0.6, Y: 0, W: 0.4, H: 0.35}

func (e *Env) scoreChange(n *Node) []Event {
	fr := e.ocrFrames()
	if len(fr) == 0 {
		return nil
	}
	r := n.Region
	if r == nil {
		r = &DefaultScoreRegion
	}
	dt := ocrStep(fr)
	type reading struct{ l, r int }
	var stable *reading
	var cand *reading
	candN := 0
	var out []Event
	for _, f := range fr {
		var nums []OCRWord
		for _, w := range regionWords(f, r, 40) {
			if _, ok := ocrDigits(w.Text); ok {
				nums = append(nums, w)
			}
		}
		if len(nums) < 2 {
			continue
		}
		sort.Slice(nums, func(i, j int) bool { return nums[i].Box[0] < nums[j].Box[0] })
		// The two numbers nearest the region's centre line.
		if len(nums) > 2 {
			mid := r.X + r.W/2
			sort.Slice(nums, func(i, j int) bool {
				return math.Abs(float64(nums[i].Box[0]+nums[i].Box[2]/2)-mid) < math.Abs(float64(nums[j].Box[0]+nums[j].Box[2]/2)-mid)
			})
			nums = nums[:2]
			sort.Slice(nums, func(i, j int) bool { return nums[i].Box[0] < nums[j].Box[0] })
		}
		l, _ := ocrDigits(nums[0].Text)
		rr, _ := ocrDigits(nums[1].Text)
		cur := reading{l, rr}
		if cand != nil && *cand == cur {
			candN++
		} else {
			cand, candN = &cur, 1
		}
		need := 2
		if dt >= 2 {
			need = 1
		}
		if candN < need {
			continue
		}
		if stable == nil {
			stable = &reading{cur.l, cur.r}
			continue
		}
		if cur == *stable {
			continue
		}
		dl, dr := cur.l-stable.l, cur.r-stable.r
		t := f.T - float64(need-1)*dt
		switch {
		case dl >= 1 && dl <= 3 && dr == 0 && (n.Side == "" || n.Side == "any" || n.Side == "left"):
			out = append(out, ev("score_change", DOCR, t-2*dt-1, t+0.5, 0.8, fmt.Sprintf("score %d–%d → %d–%d (left scores)", stable.l, stable.r, cur.l, cur.r)))
		case dr >= 1 && dr <= 3 && dl == 0 && (n.Side == "" || n.Side == "any" || n.Side == "right"):
			out = append(out, ev("score_change", DOCR, t-2*dt-1, t+0.5, 0.8, fmt.Sprintf("score %d–%d → %d–%d (right scores)", stable.l, stable.r, cur.l, cur.r)))
		}
		*stable = cur // resets (a new match) are taken without a moment
	}
	return out
}

type ocrLine struct {
	words []OCRWord
	text  string
	x0    float32
	x1    float32
}

func lines(ws []OCRWord) []ocrLine {
	sort.Slice(ws, func(i, j int) bool { return ws[i].Box[1] < ws[j].Box[1] })
	var out []ocrLine
	for _, w := range ws {
		cy := w.Box[1] + w.Box[3]/2
		placed := false
		for i := range out {
			l := &out[i]
			ref := l.words[0]
			if math.Abs(float64(cy-(ref.Box[1]+ref.Box[3]/2))) < float64(ref.Box[3])*0.6 {
				l.words = append(l.words, w)
				placed = true
				break
			}
		}
		if !placed {
			out = append(out, ocrLine{words: []OCRWord{w}})
		}
	}
	for i := range out {
		l := &out[i]
		sort.Slice(l.words, func(a, b int) bool { return l.words[a].Box[0] < l.words[b].Box[0] })
		var ts []string
		l.x0, l.x1 = 1, 0
		for _, w := range l.words {
			if t := normWord(w.Text); t != "" {
				ts = append(ts, t)
			}
			l.x0 = float32(math.Min(float64(l.x0), float64(w.Box[0])))
			l.x1 = float32(math.Max(float64(l.x1), float64(w.Box[0]+w.Box[2])))
		}
		l.text = strings.Join(ts, " ")
	}
	return out
}

func (e *Env) killfeed(n *Node) []Event {
	fr := e.ocrFrames()
	if len(fr) == 0 {
		return nil
	}
	r := n.Region
	if r == nil {
		r = &DefaultKillfeedRegion
	}
	dt := ocrStep(fr)
	name := tokens(n.Name)
	seen := map[string]float64{}
	var out []Event
	for _, f := range fr {
		for _, l := range lines(regionWords(f, r, 45)) {
			if len(l.text) < 4 {
				continue
			}
			last, ok := seen[l.text]
			seen[l.text] = f.T
			if ok && f.T-last <= 6 {
				continue // still the same line
			}
			if len(name) == 0 {
				out = append(out, ev("killfeed", DOCR, f.T-dt-1, f.T+0.5, 0.5, "kill feed: "+clipText(l.text)))
				continue
			}
			toks := tokens(l.text)
			at := -1
			for i := 0; i+len(name) <= len(toks); i++ {
				if seqMatch(toks[i:i+len(name)], name) {
					at = i
					break
				}
			}
			if at < 0 {
				continue
			}
			// Position of the name in the line: killers are listed first.
			var nx float32
			k := 0
			for _, w := range l.words {
				if normWord(w.Text) == "" {
					continue
				}
				if k == at {
					nx = w.Box[0] + w.Box[2]/2
				}
				k++
			}
			role := "kill"
			if l.x1 > l.x0 && (nx-l.x0)/(l.x1-l.x0) > 0.5 {
				role = "death"
			}
			if n.Side != "" && n.Side != "any" && n.Side != role {
				continue
			}
			out = append(out, ev("killfeed", DOCR, f.T-dt-1, f.T+0.5, 0.75, fmt.Sprintf("kill feed (%s, inferred): %s", role, clipText(l.text))))
		}
	}
	return out
}
