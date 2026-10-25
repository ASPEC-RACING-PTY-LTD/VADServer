package smart

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// SoundAliases maps the sound names filters use to AudioSet classes (as
// the AST model names them).
var SoundAliases = map[string][]string{
	"laughter":     {"Laughter", "Giggle", "Snicker", "Belly laugh", "Chuckle, chortle", "Baby laughter"},
	"giggle":       {"Giggle", "Snicker", "Chuckle, chortle"},
	"belly_laugh":  {"Belly laugh"},
	"scream":       {"Screaming"},
	"shout":        {"Shout", "Yell", "Bellow", "Children shouting", "Whoop"},
	"cheering":     {"Cheering", "Whoop", "Crowd"},
	"applause":     {"Applause", "Clapping"},
	"crying":       {"Crying, sobbing", "Baby cry, infant cry", "Whimper", "Wail, moan"},
	"sigh":         {"Sigh"},
	"gasp":         {"Gasp"},
	"groan":        {"Groan", "Grunt"},
	"music":        {"Music"},
	"singing":      {"Singing", "Choir", "Rapping"},
	"speech":       {"Speech", "Conversation", "Narration, monologue"},
	"conversation": {"Conversation", "Speech"},
	"crowd":        {"Crowd", "Chatter", "Hubbub, speech noise, speech babble"},
	"gunshot":      {"Gunshot, gunfire", "Machine gun", "Artillery fire", "Cap gun"},
	"explosion":    {"Explosion", "Boom", "Fireworks", "Burst, pop"},
	"crash":        {"Smash, crash", "Shatter", "Glass", "Slam", "Bang"},
	"alarm":        {"Alarm", "Siren", "Smoke detector, smoke alarm", "Buzzer"},
	"whistle":      {"Whistling", "Whistle"},
	"cough":        {"Cough"},
	"burp":         {"Burping, eructation"},
	"fart":         {"Fart"},
	"silence":      {"Silence"},
	"sound_effect": {"Sound effect", "Beep, bleep", "Ding", "Boing", "Zing"},
	"dog":          {"Dog", "Bark"},
	"cat":          {"Cat", "Meow"},
	"vehicle":      {"Vehicle", "Car", "Engine", "Motorcycle"},
}

// KnownSound accepts an alias or an AudioSet class name.
func KnownSound(s string) bool {
	if _, ok := SoundAliases[s]; ok {
		return true
	}
	s = strings.TrimSpace(s)
	return s != "" && len(s) <= 80 && !strings.ContainsAny(s, "\n\t")
}

// soundClasses expands aliases.
func soundClasses(labels []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range labels {
		cls, ok := SoundAliases[l]
		if !ok {
			cls = []string{l}
		}
		for _, c := range cls {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// profanity: common English swear word stems (matched at word start).
var profanity = []string{
	"fuck", "shit", "bitch", "bastard", "asshole", "arsehole", "dick", "cunt", "piss", "crap", "damn", "bollock", "wanker",
	"twat", "prick", "slut", "whore", "motherfuck", "bullshit", "goddamn", "douche", "jackass", "dumbass", "retard", "fag",
}

// profane reports whether a normalised word is a swear word.
func profane(w string) bool {
	if w == "ass" || w == "arse" || w == "hell" || w == "bloody" {
		return w == "ass" || w == "arse"
	}
	for _, p := range profanity {
		if strings.HasPrefix(w, p) {
			return true
		}
	}
	return false
}

// normWord lowercases and strips punctuation.
func normWord(w string) string {
	return strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' }))
}

// tokens splits text into normalised words.
func tokens(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '-' || r == '/' }) {
		if w := normWord(f); w != "" {
			out = append(out, w)
		}
	}
	return out
}

func cleanTerms(terms []string) [][]string {
	var out [][]string
	for _, t := range terms {
		if ws := tokens(t); len(ws) > 0 {
			out = append(out, ws)
		}
	}
	return out
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Round(p * float64(len(s)-1)))
	return s[min(max(i, 0), len(s)-1)]
}

func clamp01(v float64) float64 { return math.Min(math.Max(v, 0), 1) }

func floats(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, f := range v {
		out[i] = float64(f)
	}
	return out
}
