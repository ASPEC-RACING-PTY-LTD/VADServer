package smart

import (
	"strconv"
	"strings"
)

// Detectors: each measures one kind of signal in a recording and is
// cached per content. The version is part of the cache key: raising it
// makes recordings be measured again.
const (
	DAudioDSP    = "audio.dsp"         // server: loudness, onsets, speech band
	DVideoDSP    = "video.dsp"         // server: motion, scene changes, brightness
	DVAD         = "speech.vad"        // worker: Silero voice activity
	DTranscript  = "speech.transcript" // worker: Whisper (+ sentence and voice embeddings)
	DAudioEvents = "audio.events"      // worker: AST AudioSet classifier
	DAudioCLAP   = "audio.clap"        // worker: CLAP audio embeddings
	DEmotion     = "speech.emotion"    // worker: wav2vec2 speech emotion
	DFrames      = "vision.clip"       // worker: CLIP frame embeddings
	DFaces       = "vision.faces"      // worker: YuNet faces (+ CLIP expressions)
	DOCR         = "vision.ocr"        // worker: Tesseract on-screen text
)

// AllDetectors in processing order (cheap server measurements first).
var AllDetectors = []string{DAudioDSP, DVideoDSP, DVAD, DTranscript, DAudioEvents, DAudioCLAP, DEmotion, DFrames, DFaces, DOCR}

// DetectorVersion: bump to measure recordings again after a change.
var DetectorVersion = map[string]int{
	DAudioDSP: 1, DVideoDSP: 1, DVAD: 1, DTranscript: 1, DAudioEvents: 1, DAudioCLAP: 1, DEmotion: 1, DFrames: 1, DFaces: 1, DOCR: 1,
}

// WorkerDetectors are measured by the inference worker.
var WorkerDetectors = map[string]bool{DVAD: true, DTranscript: true, DAudioEvents: true, DAudioCLAP: true, DEmotion: true, DFrames: true, DFaces: true, DOCR: true}

// AudioDetectors need the decoded audio; the others decode frames.
var AudioDetectors = map[string]bool{DAudioDSP: true, DVAD: true, DTranscript: true, DAudioEvents: true, DAudioCLAP: true, DEmotion: true}

// DetectorModel is the worker model a detector needs.
var DetectorModel = map[string]string{
	DVAD: "silero-vad", DTranscript: "whisper", DAudioEvents: "ast", DAudioCLAP: "clap",
	DEmotion: "ser", DFrames: "clip", DFaces: "yunet", DOCR: "tesseract",
}

// DetectorInfo describes a detector for the interface.
type DetectorInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Model       string `json:"model,omitempty"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
}

var detectorText = map[string][2]string{
	DAudioDSP:    {"Loudness and sound changes", "Measured by the server with FFmpeg: level, sudden changes, silence, onsets."},
	DVideoDSP:    {"Motion and scene changes", "Measured by the server with FFmpeg: frame differences, cuts, brightness."},
	DVAD:         {"Voice activity", "Silero VAD: when someone is speaking."},
	DTranscript:  {"Speech to text", "Whisper transcription with word timestamps; sentence embeddings for semantic search and anonymous speaker numbers when those models are installed."},
	DAudioEvents: {"Sound events", "Audio Spectrogram Transformer trained on AudioSet (527 sound classes: laughter, screaming, applause, cheering, music, ...)."},
	DAudioCLAP:   {"Described sounds", "CLAP: matches sounds against free-text descriptions."},
	DEmotion:     {"Speech emotion", "wav2vec2 speech emotion recognition (angry, happy, sad, neutral)."},
	DFrames:      {"Described scenes", "CLIP: matches frames against free-text descriptions."},
	DFaces:       {"Faces and expressions", "YuNet face detection (counts, people entering or leaving) and zero-shot expressions of the largest face."},
	DOCR:         {"On-screen text", "Tesseract OCR: text, scoreboards and kill feeds."},
}

// Availability says what this server can measure now.
type Availability struct {
	FFmpeg bool
	Models map[string]bool // installed worker models
}

// Has reports whether a detector can run.
func (a Availability) Has(d string) bool {
	if !a.FFmpeg {
		return false
	}
	if m, ok := DetectorModel[d]; ok {
		return a.Models[m]
	}
	return true
}

func (a Availability) model(m string) bool { return a.Models[m] }

// Detectors describes every detector and whether it can run.
func (a Availability) Detectors() []DetectorInfo {
	var out []DetectorInfo
	for _, d := range AllDetectors {
		t := detectorText[d]
		i := DetectorInfo{ID: d, Title: t[0], Description: t[1], Model: DetectorModel[d], Available: a.Has(d)}
		switch {
		case !a.FFmpeg:
			i.Reason = "FFmpeg is not installed on the server"
		case !i.Available:
			i.Reason = "Needs the " + i.Model + " model of the smart processing worker"
		}
		out = append(out, i)
	}
	return out
}

// KindSpec describes a detector condition.
type KindSpec struct {
	Kind        string   `json:"kind"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Reliability string   `json:"reliability"`
	Modes       []string `json:"modes,omitempty"`
	LabelSet    []string `json:"labels,omitempty"`
	NeedsText   bool     `json:"needs_text,omitempty"`
	// Primary detectors it needs; Fallback ones are used (and reported)
	// when a primary one cannot run; Models are further worker models.
	Primary  []string `json:"primary"`
	Fallback []string `json:"fallback,omitempty"`
	Models   []string `json:"models,omitempty"`
	// DefaultScore is the minimum score when the condition gives none.
	DefaultScore float64 `json:"default_score"`
}

var kindList = []KindSpec{
	{Kind: "audio_event", Title: "Sound", Category: "audio", Primary: []string{DAudioEvents}, DefaultScore: 0.3,
		Description: "A sound class: laughter, giggling, screaming, shouting, cheering, applause, crying, gasps, music, gunshots, explosions, ...",
		Reliability: "Good for laughter, cheering, applause, screaming and music; weaker for quiet or overlapping sounds."},
	{Kind: "audio_description", Title: "Described sound", Category: "audio", Primary: []string{DAudioCLAP}, NeedsText: true, DefaultScore: 0.5,
		Description: "Sounds matching a free-text description (\"people laughing loudly\", \"a car crash\").",
		Reliability: "Approximate: zero-shot similarity between the description and the audio."},
	{Kind: "loudness", Title: "Loudness", Category: "audio", Primary: []string{DAudioDSP}, Modes: []string{"loud", "sudden_rise", "sudden_drop", "silence", "quiet"}, DefaultScore: 0.3,
		Description: "Loud moments, sudden jumps or drops in level, silence.",
		Reliability: "Measured directly; reliable. Loud is relative to the recording."},
	{Kind: "music_change", Title: "Music starts or stops", Category: "audio", Primary: []string{DAudioEvents}, Modes: []string{"start", "stop"}, DefaultScore: 0.4,
		Description: "Music beginning or ending.", Reliability: "Good for clear music; background music under speech is less certain."},
	{Kind: "speech", Title: "Speech", Category: "speech", Primary: []string{DVAD}, Fallback: []string{DAudioDSP}, Modes: []string{"active", "start", "end", "awkward_silence", "overlap", "speaker_change"}, DefaultScore: 0.3,
		Description: "Someone speaking, speech starting or ending, awkward silences in a conversation, rapid exchanges, a different speaker.",
		Reliability: "Voice activity is reliable with the speech model; silences are measured. Speaker changes and overlaps are estimated from anonymous voice embeddings."},
	{Kind: "question", Title: "Question", Category: "conversation", Primary: []string{DTranscript}, DefaultScore: 0.5,
		Description: "Someone asking a question.", Reliability: "Depends on the transcription's punctuation."},
	{Kind: "keyword", Title: "Words or phrases", Category: "speech", Primary: []string{DTranscript}, DefaultScore: 0.3,
		Description: "Exact words, phrases, names or nicknames in the speech.", Reliability: "As good as the transcription; names and slang may be misheard."},
	{Kind: "profanity", Title: "Profanity", Category: "speech", Primary: []string{DTranscript}, DefaultScore: 0.3,
		Description: "Swearing (built-in word list).", Reliability: "As good as the transcription."},
	{Kind: "repeated_phrase", Title: "Repeated phrase", Category: "speech", Primary: []string{DTranscript}, DefaultScore: 0.3,
		Description: "The same phrase said again within a minute.", Reliability: "As good as the transcription."},
	{Kind: "dialogue", Title: "Dialogue about", Category: "conversation", Primary: []string{DTranscript}, Models: []string{"minilm"}, NeedsText: true, DefaultScore: 0.5,
		Description: "Speech whose meaning matches a description or topic (\"talking about relationships\", \"someone insulting the other person\").",
		Reliability: "Approximate: semantic similarity of what was said; tone and sarcasm are not understood."},
	{Kind: "topic_shift", Title: "Conversation changes", Category: "conversation", Primary: []string{DTranscript}, Models: []string{"minilm"}, DefaultScore: 0.5,
		Description: "The conversation suddenly turns to something else.", Reliability: "Approximate: change in meaning between consecutive sentences."},
	{Kind: "emotion", Title: "Voice emotion", Category: "emotion", Primary: []string{DEmotion}, LabelSet: []string{"angry", "happy", "sad", "neutral"}, DefaultScore: 0.6,
		Description: "Angry, happy or sad sounding speech.",
		Reliability: "Uncertain: the model was trained on acted English speech. A suggestion, never a fact about how someone feels."},
	{Kind: "visual_description", Title: "Described scene", Category: "visual", Primary: []string{DFrames}, NeedsText: true, DefaultScore: 0.5,
		Description: "Frames matching a free-text description (\"a goal explosion in Rocket League\", \"someone covering their face\").",
		Reliability: "Approximate: zero-shot similarity between the description and sampled frames."},
	{Kind: "faces", Title: "People in frame", Category: "visual", Primary: []string{DFaces}, Modes: []string{"enter", "leave", "present", "absent", "multiple", "count_change"}, DefaultScore: 0.3,
		Description: "Faces appearing or disappearing, several people, nobody in frame.",
		Reliability: "Detects faces, not identities; people turned away or very small are missed."},
	{Kind: "expression", Title: "Facial expression", Category: "emotion", Primary: []string{DFaces}, Models: []string{"clip"}, LabelSet: []string{"happy", "surprised", "angry", "sad", "scared", "neutral"}, DefaultScore: 0.55,
		Description: "The expression of the most visible face.",
		Reliability: "Uncertain: zero-shot reading of a face; a suggestion, never a fact about how someone feels."},
	{Kind: "motion", Title: "Movement", Category: "visual", Primary: []string{DVideoDSP}, Modes: []string{"sudden", "high", "still"}, DefaultScore: 0.3,
		Description: "Sudden movement, sustained activity or a still picture.", Reliability: "Measured directly; camera moves count as movement."},
	{Kind: "scene_change", Title: "Scene change", Category: "visual", Primary: []string{DVideoDSP}, DefaultScore: 0.35,
		Description: "Cuts and large changes between frames.", Reliability: "Measured directly; reliable for cuts, fades are weaker."},
	{Kind: "onscreen_text", Title: "On-screen text", Category: "visual", Primary: []string{DOCR}, DefaultScore: 0.3,
		Description: "Text appearing on screen, or specific text (\"VICTORY|DEFEAT\").", Reliability: "Good for large clear text; stylised game fonts may be misread."},
	{Kind: "score_change", Title: "Score changes", Category: "gaming", Primary: []string{DOCR}, DefaultScore: 0.3,
		Description: "A scoreboard number going up (goals). Set the screen region of the score; left and right are the two teams.",
		Reliability: "Reliable when the region shows only the score; stylised fonts may be misread."},
	{Kind: "killfeed", Title: "Kill feed", Category: "gaming", Primary: []string{DOCR}, DefaultScore: 0.3,
		Description: "New kill feed lines, optionally with your player name as killer or victim.",
		Reliability: "Approximate: reads the kill feed with OCR; killer/victim is inferred from the name's position in the line."},
}

var kinds = func() map[string]KindSpec {
	m := map[string]KindSpec{}
	for _, k := range kindList {
		m[k.Kind] = k
	}
	return m
}()

// Kinds lists every condition kind.
func Kinds() []KindSpec { return kindList }

// needs returns the detectors a condition uses with what is available.
func needs(l *Node, avail Availability) []string {
	k, ok := kinds[l.Kind]
	if !ok {
		return nil
	}
	prim := k.Primary
	if l.Kind == "speech" && (l.Mode == "overlap" || l.Mode == "speaker_change") {
		prim = []string{DTranscript}
	}
	all := func(ds []string) bool {
		for _, d := range ds {
			if !avail.Has(d) {
				return false
			}
		}
		return true
	}
	out := append([]string(nil), prim...)
	if !all(prim) && len(k.Fallback) > 0 && all(k.Fallback) && l.Kind == "speech" && l.Mode != "overlap" && l.Mode != "speaker_change" {
		out = append([]string(nil), k.Fallback...)
	}
	// Silences are confirmed with the measured level; music is ruled out
	// with sound events when they are measured.
	if l.Kind == "speech" && l.Mode == "awkward_silence" {
		out = append(out, DAudioDSP)
		if avail.Has(DAudioEvents) {
			out = append(out, DAudioEvents)
		}
	}
	if l.Kind == "speech" && (l.Mode == "active" || l.Mode == "start" || l.Mode == "end") && avail.Has(DTranscript) && !avail.Has(DVAD) {
		out = []string{DTranscript}
	}
	return out
}

// Component is one detector condition of a filter and whether it can be
// evaluated here.
type Component struct {
	Rule        string   `json:"rule"`
	Kind        string   `json:"kind"`
	Summary     string   `json:"summary"`
	Detectors   []string `json:"detectors"`
	Available   bool     `json:"available"`
	Fallback    bool     `json:"fallback,omitempty"`
	Missing     []string `json:"missing,omitempty"` // models to install
	Reliability string   `json:"reliability"`
}

// Check lists every condition of the filter with its availability.
func Check(f *Filter, avail Availability) []Component {
	var out []Component
	for i := range f.Rules {
		r := &f.Rules[i]
		r.When.Leaves(func(l *Node) {
			k := kinds[l.Kind]
			c := Component{Rule: r.Name, Kind: l.Kind, Summary: Describe(l), Detectors: needs(l, avail), Reliability: k.Reliability, Available: true}
			missing := map[string]bool{}
			for _, d := range c.Detectors {
				if !avail.Has(d) {
					c.Available = false
					if m := DetectorModel[d]; m != "" {
						missing[m] = true
					} else {
						missing["ffmpeg"] = true
					}
				}
			}
			for _, m := range k.Models {
				if !avail.model(m) {
					c.Available = false
					missing[m] = true
				}
			}
			for m := range missing {
				c.Missing = append(c.Missing, m)
			}
			if len(k.Fallback) > 0 && len(c.Detectors) > 0 && contains(k.Fallback, c.Detectors[0]) && !contains(k.Primary, c.Detectors[0]) {
				c.Fallback = true
				c.Reliability = "Estimated from loudness and the speech frequency band because the speech model is not installed; music and noise can be mistaken for speech."
			}
			out = append(out, c)
		})
	}
	return out
}

// Describe summarises a condition in words.
func Describe(n *Node) string {
	if n.Op != "" {
		parts := make([]string, len(n.Children))
		for i := range n.Children {
			parts[i] = Describe(&n.Children[i])
		}
		switch n.Op {
		case "and":
			return "(" + strings.Join(parts, " and ") + ")" + windowText(n.Window, "together")
		case "or":
			return "(" + strings.Join(parts, " or ") + ")"
		case "then":
			return "(" + strings.Join(parts, " then ") + ")" + windowText(n.Window, "apart")
		case "without":
			return "(" + parts[0] + " without " + strings.Join(parts[1:], " or ") + ")" + windowText(n.Window, "around")
		}
	}
	k := kinds[n.Kind]
	s := k.Title
	switch {
	case len(n.Labels) > 0:
		s += ": " + strings.Join(n.Labels, ", ")
	case len(n.Terms) > 0:
		s += ": " + strings.Join(n.Terms, ", ")
	case n.Text != "":
		s += ": \"" + n.Text + "\""
	case n.Label != "":
		s += ": " + n.Label
	}
	if n.Mode != "" && len(k.Modes) > 1 {
		s += " (" + strings.ReplaceAll(n.Mode, "_", " ") + ")"
	}
	if n.Name != "" {
		s += " for " + n.Name
	}
	return s
}

func windowText(w float64, word string) string {
	if w <= 0 {
		return ""
	}
	return " within " + trimFloat(w) + " s " + word
}

func trimFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
