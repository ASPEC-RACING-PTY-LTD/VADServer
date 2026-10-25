package smart

import (
	"errors"
	"fmt"
	"strings"
)

// A Filter says what to look for: rules (each a condition tree) and how
// found moments become segments (Context).
type Filter struct {
	Rules   []Rule  `json:"rules"`
	Context Context `json:"context"`
}

// Rule is one named condition. Category groups its moments in review
// (emotion, conversation, gaming, visual, audio, speech, custom).
type Rule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Disabled bool   `json:"disabled,omitempty"`
	When     Node   `json:"when"`
}

// Node is a condition: a leaf (Kind set) or a combination (Op set).
//
// Combinations:
//
//	and      every child has a moment within Window seconds of the others
//	or       any child
//	then     each child follows the previous one within Window seconds
//	without  the first child with none of the others within Window seconds
type Node struct {
	Op       string  `json:"op,omitempty"`
	Window   float64 `json:"window,omitempty"`
	Children []Node  `json:"children,omitempty"`

	Kind string `json:"kind,omitempty"`
	// Mode selects a variant of the kind (see the catalog).
	Mode string `json:"mode,omitempty"`
	// Labels: audio event names or aliases (audio_event).
	Labels []string `json:"labels,omitempty"`
	// Label: one class (emotion, expression).
	Label string `json:"label,omitempty"`
	// Text: a description (audio_description, visual_description,
	// dialogue) or a text pattern (onscreen_text; "A|B" alternatives).
	Text string `json:"text,omitempty"`
	// Terms: words or phrases (keyword).
	Terms []string `json:"terms,omitempty"`
	// Match: any (default) or all terms within Window (keyword).
	Match string `json:"match,omitempty"`
	// Region of the frame (fractions) for on-screen text, scores and kill
	// feeds.
	Region *Rect `json:"region,omitempty"`
	// Name: the player name to find in a kill feed.
	Name string `json:"name,omitempty"`
	// Side: score side (left, right, any) or kill feed role (kill, death, any).
	Side string `json:"side,omitempty"`
	// DB: loudness threshold (dBFS) or rise/drop (dB) for loudness modes.
	DB float64 `json:"db,omitempty"`
	// Count: repetitions (repeated_phrase) or words per phrase.
	Count int `json:"count,omitempty"`
	// MinScore: moments scoring lower are ignored (0 uses the kind's default).
	MinScore float64 `json:"min_score,omitempty"`
	// MinDuration: shorter moments are ignored (seconds).
	MinDuration float64 `json:"min_duration,omitempty"`
}

// Rect is a part of the frame as fractions (0..1).
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func (r *Rect) contains(b [4]float32) bool {
	if r == nil {
		return true
	}
	cx, cy := float64(b[0]+b[2]/2), float64(b[1]+b[3]/2)
	return cx >= r.X && cx <= r.X+r.W && cy >= r.Y && cy <= r.Y+r.H
}

// Context turns moments into segments.
type Context struct {
	// Threshold: moments scoring lower are not suggested (0..1).
	Threshold float64 `json:"threshold"`
	// MinEvent: moments shorter than this are ignored (seconds).
	MinEvent float64 `json:"min_event"`
	// MaxSegment: longest suggested segment (seconds).
	MaxSegment float64 `json:"max_segment"`
	// Pre and Post: footage kept before and after a moment (seconds).
	Pre  float64 `json:"pre"`
	Post float64 `json:"post"`
	// Merge joins moments closer than MergeGap seconds into one segment.
	Merge    bool    `json:"merge"`
	MergeGap float64 `json:"merge_gap"`
	// TrimSilence removes silent footage at the ends of a segment (never
	// inside the moment itself).
	TrimSilence bool `json:"trim_silence"`
	// SnapSpeech moves segment edges to sentence boundaries so speech is
	// not cut mid-sentence.
	SnapSpeech bool `json:"snap_speech"`
	// Dedupe drops a weaker suggestion overlapping a stronger one by more
	// than this share (0..1; 0 keeps all).
	Dedupe float64 `json:"dedupe"`
	// MaxSuggestions per recording (0: no limit).
	MaxSuggestions int `json:"max_suggestions"`
}

// DefaultContext keeps generous context: interesting moments often need
// the lead-up and the reaction.
func DefaultContext() Context {
	return Context{Threshold: 0.5, MinEvent: 0.3, MaxSegment: 90, Pre: 8, Post: 6, Merge: true, MergeGap: 6, TrimSilence: true, SnapSpeech: true, Dedupe: 0.6, MaxSuggestions: 40}
}

const (
	maxRules    = 24
	maxDepth    = 6
	maxChildren = 8
	maxText     = 400
	maxTerms    = 100
)

// Validate checks a filter and fills defaults.
func (f *Filter) Validate() error {
	if len(f.Rules) > maxRules {
		return fmt.Errorf("at most %d rules", maxRules)
	}
	ids := map[string]bool{}
	for i := range f.Rules {
		r := &f.Rules[i]
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" {
			r.Name = fmt.Sprintf("Rule %d", i+1)
		}
		if len(r.Name) > 120 {
			return errors.New("rule names can be at most 120 characters")
		}
		if r.ID == "" || ids[r.ID] {
			r.ID = fmt.Sprintf("r%d", i+1)
			for ids[r.ID] {
				r.ID += "x"
			}
		}
		ids[r.ID] = true
		if r.Category == "" {
			r.Category = categoryOf(&r.When)
		}
		if !validCategory(r.Category) {
			return fmt.Errorf("rule %q: unknown category %q", r.Name, r.Category)
		}
		if err := r.When.validate(0); err != nil {
			return fmt.Errorf("rule %q: %w", r.Name, err)
		}
	}
	return f.Context.validate()
}

var categories = []string{"emotion", "conversation", "gaming", "visual", "audio", "speech", "custom"}

func validCategory(c string) bool {
	for _, x := range categories {
		if x == c {
			return true
		}
	}
	return false
}

func (c *Context) validate() error {
	switch {
	case c.Threshold < 0 || c.Threshold > 1:
		return errors.New("the confidence threshold must be between 0 and 1")
	case c.Pre < 0 || c.Pre > 300 || c.Post < 0 || c.Post > 300:
		return errors.New("footage before and after a moment must be 0-300 seconds")
	case c.MaxSegment < 0 || c.MaxSegment > 3600:
		return errors.New("the longest segment must be 0-3600 seconds")
	case c.MinEvent < 0 || c.MinEvent > 600:
		return errors.New("the shortest moment must be 0-600 seconds")
	case c.MergeGap < 0 || c.MergeGap > 600:
		return errors.New("the merge gap must be 0-600 seconds")
	case c.Dedupe < 0 || c.Dedupe > 1:
		return errors.New("duplicate overlap must be between 0 and 1")
	case c.MaxSuggestions < 0 || c.MaxSuggestions > 500:
		return errors.New("at most 500 suggestions per recording")
	}
	if c.MaxSegment == 0 {
		c.MaxSegment = 90
	}
	return nil
}

func (n *Node) validate(depth int) error {
	if depth > maxDepth {
		return errors.New("conditions are nested too deeply")
	}
	if n.Window < 0 || n.Window > 600 {
		return errors.New("time windows must be 0-600 seconds")
	}
	if n.MinScore < 0 || n.MinScore > 1 {
		return errors.New("minimum scores must be between 0 and 1")
	}
	if n.MinDuration < 0 || n.MinDuration > 600 {
		return errors.New("minimum durations must be 0-600 seconds")
	}
	if n.Op != "" {
		if n.Kind != "" {
			return errors.New("a condition is either a combination or a detector, not both")
		}
		switch n.Op {
		case "and", "or", "then", "without":
		default:
			return fmt.Errorf("unknown combination %q", n.Op)
		}
		if len(n.Children) < 1 || len(n.Children) > maxChildren || (n.Op != "or" && len(n.Children) < 2) {
			return fmt.Errorf("%q needs 2-%d conditions", n.Op, maxChildren)
		}
		if n.Window == 0 && n.Op != "or" {
			n.Window = 5
		}
		for i := range n.Children {
			if err := n.Children[i].validate(depth + 1); err != nil {
				return err
			}
		}
		return nil
	}
	if len(n.Children) > 0 {
		return errors.New("a detector condition has no sub-conditions")
	}
	k, ok := kinds[n.Kind]
	if !ok {
		return fmt.Errorf("unknown detector %q", n.Kind)
	}
	n.Text = strings.TrimSpace(n.Text)
	if len(n.Text) > maxText || len(n.Name) > 100 || len(n.Label) > 60 {
		return errors.New("text is too long")
	}
	if len(n.Terms) > maxTerms || len(n.Labels) > maxTerms {
		return fmt.Errorf("at most %d terms or labels", maxTerms)
	}
	for _, t := range n.Terms {
		if len(t) > 200 {
			return errors.New("terms can be at most 200 characters")
		}
	}
	if len(k.Modes) > 0 {
		if n.Mode == "" {
			n.Mode = k.Modes[0]
		}
		if !contains(k.Modes, n.Mode) {
			return fmt.Errorf("%s: unknown mode %q (one of %s)", n.Kind, n.Mode, strings.Join(k.Modes, ", "))
		}
	}
	if len(k.LabelSet) > 0 {
		if n.Label == "" {
			return fmt.Errorf("%s needs a label (one of %s)", n.Kind, strings.Join(k.LabelSet, ", "))
		}
		if !contains(k.LabelSet, n.Label) {
			return fmt.Errorf("%s: unknown label %q (one of %s)", n.Kind, n.Label, strings.Join(k.LabelSet, ", "))
		}
	}
	if k.NeedsText && n.Text == "" {
		return fmt.Errorf("%s needs a description", n.Kind)
	}
	if n.Kind == "keyword" && len(cleanTerms(n.Terms)) == 0 {
		return errors.New("keyword needs at least one word or phrase")
	}
	if n.Kind == "audio_event" {
		if len(n.Labels) == 0 {
			return errors.New("audio_event needs at least one sound")
		}
		for _, l := range n.Labels {
			if !KnownSound(l) {
				return fmt.Errorf("unknown sound %q", l)
			}
		}
	}
	if r := n.Region; r != nil && (r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 || r.X+r.W > 1.0001 || r.Y+r.H > 1.0001) {
		return errors.New("the screen region must be inside the frame")
	}
	if n.Count < 0 || n.Count > 50 {
		return errors.New("counts must be 0-50")
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Leaves calls f for every detector condition of the tree.
func (n *Node) Leaves(f func(*Node)) {
	if n.Op == "" {
		f(n)
		return
	}
	for i := range n.Children {
		n.Children[i].Leaves(f)
	}
}

func categoryOf(n *Node) string {
	cat := ""
	n.Leaves(func(l *Node) {
		if cat == "" {
			if k, ok := kinds[l.Kind]; ok {
				cat = k.Category
			}
		}
	})
	if cat == "" {
		return "custom"
	}
	return cat
}

// Detectors returns the detectors the enabled rules need.
func (f *Filter) Detectors(avail Availability) []string {
	set := map[string]bool{}
	for i := range f.Rules {
		if f.Rules[i].Disabled {
			continue
		}
		f.Rules[i].When.Leaves(func(l *Node) {
			for _, d := range needs(l, avail) {
				set[d] = true
			}
		})
	}
	var out []string
	for _, d := range AllDetectors {
		if set[d] {
			out = append(out, d)
		}
	}
	return out
}
