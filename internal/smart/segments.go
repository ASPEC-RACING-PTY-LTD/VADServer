package smart

import (
	"math"
	"sort"
	"strings"
)

// Suggestion is a proposed segment.
type Suggestion struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Score float64 `json:"score"`
	// Core: the detected moments themselves (Start..End adds context).
	CoreStart float64  `json:"core_start"`
	CoreEnd   float64  `json:"core_end"`
	Category  string   `json:"category"`
	Label     string   `json:"label"`
	Rules     []string `json:"rules"`
	Reasons   []Reason `json:"reasons"`
	Events    int      `json:"events"`
}

const maxReasons = 12

// Suggest turns moments into segments with context: footage before and
// after, related moments merged, sentence boundaries respected, silent
// ends trimmed, duplicates dropped.
func Suggest(a *Analysis, events []Event, c Context) []Suggestion {
	dur := a.Duration
	if dur <= 0 {
		for _, e := range events {
			dur = math.Max(dur, e.End)
		}
	}
	var evs []Event
	for _, e := range events {
		if e.Score >= c.Threshold && e.End-e.Start >= c.MinEvent-1e-9 {
			e.Start, e.End = math.Max(e.Start, 0), math.Min(e.End, dur)
			if e.End > e.Start {
				evs = append(evs, e)
			}
		}
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].Start < evs[j].Start })
	maxLen := c.MaxSegment
	if maxLen <= 0 {
		maxLen = 90
	}
	var out []Suggestion
	for _, e := range evs {
		s := Suggestion{
			Start: math.Max(e.Start-c.Pre, 0), End: math.Min(e.End+c.Post, dur),
			Score: e.Score, CoreStart: e.Start, CoreEnd: e.End, Category: e.Category,
			Rules: []string{e.RuleName}, Reasons: capReasons(e.Reasons), Events: 1,
		}
		if n := len(out); n > 0 && c.Merge {
			p := &out[n-1]
			joined := math.Max(p.End, s.End) - p.Start
			if s.Start <= p.End+c.MergeGap && joined <= maxLen {
				if s.Score > p.Score {
					p.Category = s.Category
				}
				p.End = math.Max(p.End, s.End)
				p.CoreEnd = math.Max(p.CoreEnd, s.CoreEnd)
				p.Score = math.Max(p.Score, s.Score)
				p.Events++
				if !contains(p.Rules, e.RuleName) {
					p.Rules = append(p.Rules, e.RuleName)
				}
				p.Reasons = capReasons(append(p.Reasons, s.Reasons...))
				continue
			}
		}
		out = append(out, s)
	}
	speech := utteranceSpans(a)
	for i := range out {
		s := &out[i]
		if c.SnapSpeech {
			snapToSpeech(s, speech, c, dur, maxLen)
		}
		if c.TrimSilence && a.Audio != nil {
			trimSilence(s, a.Audio)
		}
		fitLength(s, maxLen)
		s.Label = strings.Join(s.Rules, " + ")
		s.Start, s.End = round2(s.Start), round2(s.End)
	}
	if c.Dedupe > 0 {
		order := append([]Suggestion(nil), out...)
		sort.SliceStable(order, func(i, j int) bool { return order[i].Score > order[j].Score })
		var kept []Suggestion
		for _, s := range order {
			dup := false
			for _, k := range kept {
				if OverlapShare(s.Start, s.End, k.Start, k.End) > c.Dedupe {
					dup = true
					break
				}
			}
			if !dup {
				kept = append(kept, s)
			}
		}
		out = kept
	}
	if c.MaxSuggestions > 0 && len(out) > c.MaxSuggestions {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
		out = out[:c.MaxSuggestions]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// OverlapShare is the overlap of two spans as a share of the shorter one.
func OverlapShare(a0, a1, b0, b1 float64) float64 {
	ov := math.Min(a1, b1) - math.Max(a0, b0)
	short := math.Min(a1-a0, b1-b0)
	if ov <= 0 || short <= 0 {
		return 0
	}
	return ov / short
}

func capReasons(r []Reason) []Reason {
	if len(r) <= maxReasons {
		return r
	}
	s := append([]Reason(nil), r...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].Score > s[j].Score })
	s = s[:maxReasons]
	sort.SliceStable(s, func(i, j int) bool { return s[i].Start < s[j].Start })
	return s
}

func utteranceSpans(a *Analysis) []Interval {
	if a.Transcript != nil && len(a.Transcript.Utterances) > 0 {
		out := make([]Interval, 0, len(a.Transcript.Utterances))
		for _, u := range a.Transcript.Utterances {
			out = append(out, Interval{Start: u.Start, End: u.End})
		}
		return out
	}
	return a.Speech
}

// snapToSpeech widens the edges to the sentence they cut through, a few
// seconds at most and within the length limit.
func snapToSpeech(s *Suggestion, sp []Interval, c Context, dur, maxLen float64) {
	const reach = 6
	for _, u := range sp {
		if u.Start < s.Start && s.Start < u.End && s.Start-u.Start <= reach && s.End-(u.Start-0.2) <= maxLen {
			s.Start = math.Max(u.Start-0.2, 0)
		}
		if u.Start < s.End && s.End < u.End && u.End-s.End <= reach && u.End+0.3-s.Start <= maxLen {
			s.End = math.Min(u.End+0.3, dur)
		}
	}
}

// trimSilence drops silent footage at the ends, keeping a second before
// and after the moment.
func trimSilence(s *Suggestion, a *AudioDSP) {
	floor := noiseFloor(a)
	quiet := func(t float64) bool { return a.Loudness.At(t) <= floor+6 }
	step := 1 / a.Loudness.Rate
	for s.Start+step < s.CoreStart-1 && quiet(s.Start) && quiet(s.Start+step) {
		s.Start += step
	}
	for s.End-step > s.CoreEnd+1 && quiet(s.End-step) && quiet(s.End-2*step) {
		s.End -= step
	}
}

// fitLength keeps a segment within max: the context is shortened evenly;
// a longer moment keeps the part around its middle.
func fitLength(s *Suggestion, maxLen float64) {
	if s.End-s.Start <= maxLen {
		return
	}
	core := s.CoreEnd - s.CoreStart
	if core >= maxLen {
		mid := (s.CoreStart + s.CoreEnd) / 2
		s.Start, s.End = mid-maxLen/2, mid+maxLen/2
		s.CoreStart, s.CoreEnd = s.Start, s.End
		return
	}
	pre, post := s.CoreStart-s.Start, s.End-s.CoreEnd
	room := maxLen - core
	k := room / (pre + post)
	s.Start = s.CoreStart - pre*k
	s.End = s.CoreEnd + post*k
}
