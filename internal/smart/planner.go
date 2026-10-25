package smart

import (
	"regexp"
	"strings"
)

// Plan is how a natural-language description was interpreted: the filter
// it became, which detectors that needs and whether they can run, and
// what could not be expressed.
type Plan struct {
	Description string      `json:"description"`
	Interpreter string      `json:"interpreter"` // "rules" or "model:<name>"
	Filter      Filter      `json:"filter"`
	Components  []Component `json:"components"`
	// Unsupported: parts of the request no detector can find reliably.
	Unsupported []string `json:"unsupported"`
	Notes       []string `json:"notes"`
	// Status: supported, partial (some parts missing or unavailable),
	// unavailable (nothing can run here) or unsupported.
	Status string `json:"status"`
}

// concept is a recognisable idea and the condition that detects it.
type concept struct {
	re   *regexp.Regexp
	name string
	node func(m []string) Node
	note string
	// unsupported: the idea has no detector; note says what is used instead.
	unsupported bool
}

func w(words string) *regexp.Regexp { return regexp.MustCompile(`\b(?:` + words + `)`) }

var concepts = []concept{
	{re: w(`clutch`), name: "clutch play", unsupported: true,
		note: "\"Clutch\" moments have no dedicated detector; excited reactions and sudden action are used as the closest signals.",
		node: func([]string) Node {
			return op("or", 0, sound("shout", "cheering"), op("and", 2, leaf("motion", func(n *Node) { n.Mode = "sudden" }), leaf("loudness", func(n *Node) { n.Mode = "loud" })))
		}},
	{re: w(`rocket league|(?:scores?|scored|scoring) a goal|goals?\b|scoreboard|score (?:changes?|updates?|goes up)`), name: "goal",
		note: "Goals are read from the scoreboard at the top centre of the screen; adjust the region for other games.",
		node: func([]string) Node {
			r := DefaultScoreRegion
			return leaf("score_change", func(n *Node) { n.Region = &r })
		}},
	{re: w(`kills?\b|eliminat|frags?\b|headshot`), name: "kill",
		note: "Eliminations are read from the kill feed (top right); add your player name to only find your own.",
		node: func([]string) Node { return leaf("killfeed", nil) }},
	{re: w(`dies\b|died\b|death|get killed|gets killed`), name: "death",
		note: "Deaths are read from the kill feed and need your player name to tell them from kills.",
		node: func([]string) Node { return leaf("killfeed", func(n *Node) { n.Side = "death" }) }},
	{re: w(`victor|win\b|wins\b|won\b|winning`), name: "victory",
		node: func([]string) Node {
			return leaf("onscreen_text", func(n *Node) { n.Text = "victory|winner|you win|victory royale|mvp" })
		}},
	{re: w(`defeat|lose\b|loses\b|lost\b|game over`), name: "defeat",
		node: func([]string) Node {
			return leaf("onscreen_text", func(n *Node) { n.Text = "defeat|you lose|game over|eliminated" })
		}},
	{re: w(`joke|funny|hilarious|punchline|comedy|banter`), name: "joke",
		node: func([]string) Node {
			return op("then", 4, leaf("speech", func(n *Node) { n.Mode = "active" }), sound("laughter"))
		}},
	{re: w(`laugh|giggl|chuckl|crack(?:s|ing)? up`), name: "laughter",
		node: func([]string) Node { return sound("laughter") }},
	{re: w(`scream|shriek`), name: "scream",
		node: func([]string) Node { return sound("scream") }},
	{re: w(`scared|fright|jump ?scare|terrif|afraid`), name: "fright",
		node: func([]string) Node {
			return op("or", 0, sound("scream", "gasp"), leaf("expression", func(n *Node) { n.Label = "scared" }), leaf("loudness", func(n *Node) { n.Mode = "sudden_rise" }))
		}},
	{re: w(`shout|yell`), name: "shouting", node: func([]string) Node { return sound("shout") }},
	{re: w(`cheer|celebrat|hype|excite`), name: "celebration", node: func([]string) Node { return sound("cheering", "shout") }},
	{re: w(`applau|clapping`), name: "applause", node: func([]string) Node { return sound("applause") }},
	{re: w(`cry|crie|sob|tears|sad`), name: "sadness",
		note: "Sadness is inferred from crying sounds and voice emotion; a suggestion, not a fact.",
		node: func([]string) Node {
			return op("or", 0, sound("crying"), leaf("emotion", func(n *Node) { n.Label = "sad" }))
		}},
	{re: w(`argu|disagree|fight|row\b|heated`), name: "argument",
		note: "Arguments are inferred from angry-sounding or shouted speech with interruptions.",
		node: func([]string) Node {
			return op("and", 6, op("or", 0, leaf("emotion", func(n *Node) { n.Label = "angry" }), sound("shout")), leaf("speech", func(n *Node) { n.Mode = "overlap" }))
		}},
	{re: w(`angry|anger|frustrat|rage|mad\b|annoyed`), name: "anger",
		note: "Anger is inferred from voice emotion and shouting; a suggestion, not a fact.",
		node: func([]string) Node {
			return op("or", 0, leaf("emotion", func(n *Node) { n.Label = "angry" }), sound("shout"))
		}},
	{re: w(`surpris|shock|unexpected|wtf|omg|astonish|stunned`), name: "surprise",
		node: func([]string) Node {
			return op("or", 0, leaf("expression", func(n *Node) { n.Label = "surprised" }), sound("gasp", "scream"), leaf("loudness", func(n *Node) { n.Mode = "sudden_rise" }))
		}},
	{re: w(`confus|puzzled|doesn'?t understand|don'?t understand`), name: "confusion",
		node: func([]string) Node {
			return op("or", 0, leaf("keyword", func(n *Node) { n.Terms = []string{"what", "huh", "what do you mean", "i don't understand", "confused"} }),
				leaf("dialogue", func(n *Node) { n.Text = "someone is confused and does not understand" }))
		}},
	{re: w(`awkward`), name: "awkward silence", node: func([]string) Node { return leaf("speech", func(n *Node) { n.Mode = "awkward_silence" }) }},
	{re: w(`silen|quiet`), name: "silence", node: func([]string) Node { return leaf("loudness", func(n *Node) { n.Mode = "silence" }) }},
	{re: w(`music (?:start|begin|kick)`), name: "music starts", node: func([]string) Node { return leaf("music_change", func(n *Node) { n.Mode = "start" }) }},
	{re: w(`music (?:stop|end|cut)`), name: "music stops", node: func([]string) Node { return leaf("music_change", func(n *Node) { n.Mode = "stop" }) }},
	{re: w(`music|song|singing`), name: "music", node: func([]string) Node { return sound("music", "singing") }},
	{re: w(`(?:leaves?|leaving|left|disconnect|skips?|ends? the (?:call|chat|conversation)|hangs? up)`), name: "someone leaves",
		node: func([]string) Node {
			return op("or", 0, leaf("faces", func(n *Node) { n.Mode = "leave" }), op("and", 3, leaf("scene_change", nil), leaf("speech", func(n *Node) { n.Mode = "end" })))
		}},
	{re: w(`enters?|joins?|appears?|shows up`), name: "someone appears", node: func([]string) Node { return leaf("faces", func(n *Node) { n.Mode = "enter" }) }},
	{re: w(`smil|grin`), name: "smile", node: func([]string) Node { return leaf("expression", func(n *Node) { n.Label = "happy" }) }},
	{re: w(`insult|roast|mock|diss\b|trash ?talk`), name: "insult",
		note: "Insults are found by meaning (semantic similarity) and swearing; sarcasm and tone are not understood.",
		node: func([]string) Node {
			return op("or", 0, leaf("dialogue", func(n *Node) { n.Text = "insulting, mocking or roasting someone" }), leaf("profanity", nil))
		}},
	{re: w(`compliment|praise|flatter`), name: "compliment", node: func([]string) Node {
		return leaf("dialogue", func(n *Node) { n.Text = "giving someone a compliment" })
	}},
	{re: w(`flirt|chat(?:ting)? up|pick ?up line|romantic`), name: "flirting", node: func([]string) Node {
		return leaf("dialogue", func(n *Node) { n.Text = "flirting or playful romantic banter" })
	}},
	{re: w(`swear|curs|profan|cuss`), name: "swearing", node: func([]string) Node { return leaf("profanity", nil) }},
	{re: w(`question|asks?\b|asking`), name: "question", node: func([]string) Node { return leaf("question", nil) }},
	{re: w(`respond|repl|reacts?|reaction|answers?`), name: "reply",
		node: func([]string) Node { return leaf("speech", func(n *Node) { n.Mode = "speaker_change" }) }},
	{re: w(`loud`), name: "loud", node: func([]string) Node { return leaf("loudness", func(n *Node) { n.Mode = "loud" }) }},
	{re: w(`scene change|cuts? to|camera (?:change|switch)`), name: "scene change", node: func([]string) Node { return leaf("scene_change", nil) }},
	{re: w(`mov(?:es|ing) fast|sudden movement|action|chaos|chaotic`), name: "movement", node: func([]string) Node { return leaf("motion", func(n *Node) { n.Mode = "sudden" }) }},
	{re: w(`on[- ]?screen|text (?:appears|shows)|subtitle`), name: "on-screen text", node: func([]string) Node { return leaf("onscreen_text", nil) }},
}

var (
	quoted   = regexp.MustCompile(`["“”']([^"“”']{2,80})["“”']`)
	aboutRe  = regexp.MustCompile(`\b(?:about|discussing|discuss|regarding|on the topic of|topic of)\s+([a-z0-9' ,-]{3,80}?)(?:\s+(?:and|then|followed|after|before|while|with|or)\b|[.!?]|$)`)
	saysRe   = regexp.MustCompile(`\b(?:says?|saying|said|word|phrase|mentions?|mentioning|calls? me)\s+([a-z0-9']+(?:\s+[a-z0-9']+){0,2})`)
	seqSplit = regexp.MustCompile(`\s*(?:,?\s*followed by|,?\s*and then|,?\s*then|,?\s*after which|,?\s*and (?:the other person|someone else|they|i|he|she) (?:reacts?|responds?|replies|laughs?)|,?\s*and i respond)\s*`)
)

// Interpret turns a description into a plan with the built-in rules.
func Interpret(desc string, avail Availability) Plan {
	d := strings.ToLower(strings.TrimSpace(desc))
	p := Plan{Description: desc, Interpreter: "rules"}
	var keywords []string
	for _, m := range quoted.FindAllStringSubmatch(desc, -1) {
		keywords = append(keywords, strings.TrimSpace(m[1]))
	}
	d = quoted.ReplaceAllString(d, " ")

	// Who: speakers are anonymous.
	if regexp.MustCompile(`\b(me|i|my|myself)\b`).MatchString(d) && regexp.MustCompile(`\b(someone|they|other person|he|she|stranger|partner)\b`).MatchString(d) {
		p.Unsupported = append(p.Unsupported, "Speakers are not identified, so \"me\" and the other person cannot be told apart; a reply by a different voice is used instead.")
	}
	if regexp.MustCompile(`\b(everyone|everybody|all of us|multiple people|group|whole room)\b`).MatchString(d) {
		p.Unsupported = append(p.Unsupported, "The number of people laughing or reacting cannot be counted; strong reactions are used instead.")
	}

	// Sequence: "A followed by B", "A then B", "A and the other person
	// reacts"; "B after A" is reversed.
	var steps []string
	reacts := seqSplit.FindAllString(d, -1)
	for _, part := range seqSplit.Split(d, -1) {
		if strings.TrimSpace(part) != "" {
			steps = append(steps, part)
		}
	}
	for _, r := range reacts {
		if regexp.MustCompile(`react|respond|repl|laugh`).MatchString(r) {
			verb := "respond"
			if strings.Contains(r, "laugh") {
				verb = "laugh"
			}
			steps = append(steps, verb)
		}
	}
	if len(steps) == 1 {
		if a, b, ok := strings.Cut(steps[0], " after "); ok && strings.TrimSpace(b) != "" {
			steps = []string{b, a}
		}
	}

	var nodes []Node
	var names []string
	for _, s := range steps {
		n, found := stepNode(s, &p)
		if found {
			nodes, names = append(nodes, n), append(names, s)
		}
	}
	if len(keywords) > 0 {
		nodes = append(nodes, leaf("keyword", func(n *Node) { n.Terms = keywords }))
	}
	var when Node
	switch {
	case len(nodes) == 0:
		p.Notes = append(p.Notes, "No dedicated detector matched the description, so it is matched by meaning against sounds, frames and speech. These matches are approximate.")
		when = semantic(desc)
	case len(nodes) == 1:
		when = nodes[0]
	default:
		when = op("then", 10, nodes...)
		if len(steps) <= 1 {
			when = op("and", 8, nodes...)
		}
	}
	name := strings.TrimSpace(desc)
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:97]) + "…"
	}
	rules := []Rule{{ID: "r1", Name: name, When: when}}
	if len(nodes) > 0 {
		rules = append(rules, Rule{ID: "r2", Name: "Similar meaning (approximate)", Disabled: true, Category: "custom", When: semantic(desc)})
		p.Notes = append(p.Notes, "An optional rule matching the whole description by meaning is included but switched off; switch it on to find more (and less certain) moments.")
	}
	c := DefaultContext()
	if regexp.MustCompile(`conversation|talk|joke|question|insult|compliment|flirt|argu|dialogue|chat`).MatchString(d) {
		c.Pre, c.Post, c.MaxSegment = 15, 10, 120
	}
	p.Filter = Filter{Rules: rules, Context: c}
	_ = p.Filter.Validate()
	p.finish(avail)
	return p
}

func semantic(desc string) Node {
	return op("or", 0,
		leaf("audio_description", func(n *Node) { n.Text = desc }),
		leaf("visual_description", func(n *Node) { n.Text = desc }),
		leaf("dialogue", func(n *Node) { n.Text = desc }))
}

// stepNode recognises the ideas of one step.
func stepNode(s string, p *Plan) (Node, bool) {
	var parts []Node
	used := map[string]bool{}
	sudden := regexp.MustCompile(`\bsudden`).MatchString(s)
	for _, c := range concepts {
		if !c.re.MatchString(s) || used[c.name] {
			continue
		}
		// "laughter" inside a joke step is already covered.
		if c.name == "laughter" && used["joke"] {
			continue
		}
		if c.name == "loud" && sudden {
			parts = append(parts, leaf("loudness", func(n *Node) { n.Mode = "sudden_rise" }))
			used[c.name] = true
			continue
		}
		used[c.name] = true
		parts = append(parts, c.node(nil))
		if c.unsupported {
			p.Unsupported = append(p.Unsupported, c.note)
		} else if c.note != "" {
			p.Notes = append(p.Notes, c.note)
		}
	}
	if m := aboutRe.FindStringSubmatch(s); m != nil {
		topic := strings.TrimSpace(m[1])
		parts = append(parts, leaf("dialogue", func(n *Node) { n.Text = "a conversation about " + topic }))
	}
	if m := saysRe.FindStringSubmatch(s); m != nil {
		parts = append(parts, leaf("keyword", func(n *Node) { n.Terms = []string{strings.TrimSpace(m[1])} }))
	}
	if sudden && !used["loud"] && len(parts) == 0 {
		parts = append(parts, op("or", 0, leaf("loudness", func(n *Node) { n.Mode = "sudden_rise" }), leaf("motion", func(n *Node) { n.Mode = "sudden" }), leaf("scene_change", nil)))
	}
	switch len(parts) {
	case 0:
		return Node{}, false
	case 1:
		return parts[0], true
	}
	if regexp.MustCompile(`\bor\b`).MatchString(s) && !regexp.MustCompile(`\band\b|\bwith\b|\bwhile\b`).MatchString(s) {
		return op("or", 0, parts...), true
	}
	return op("and", 6, parts...), true
}

// finish checks the plan against what can run.
func (p *Plan) finish(avail Availability) {
	if p.Unsupported == nil {
		p.Unsupported = []string{}
	}
	if p.Notes == nil {
		p.Notes = []string{}
	}
	p.Components = Check(&p.Filter, avail)
	enabled := map[string]bool{}
	for _, r := range p.Filter.Rules {
		if !r.Disabled {
			enabled[r.Name] = true
		}
	}
	ok, bad := 0, 0
	for _, c := range p.Components {
		if !enabled[c.Rule] {
			continue
		}
		if c.Available {
			ok++
		} else {
			bad++
		}
	}
	switch {
	case ok == 0 && bad == 0:
		p.Status = "unsupported"
	case ok == 0:
		p.Status = "unavailable"
	case bad > 0 || len(p.Unsupported) > 0:
		p.Status = "partial"
	default:
		p.Status = "supported"
	}
	if bad > 0 {
		p.Notes = append(p.Notes, "Some conditions need models that are not installed; they find nothing until an administrator installs them under Administration → Smart processing.")
	}
}

// FinishPlan validates a filter proposed by the local language model and
// completes the plan; ok is false when the proposal is unusable.
func FinishPlan(desc, model string, f Filter, notes []string, avail Availability) (Plan, bool) {
	if len(f.Rules) == 0 || f.Validate() != nil {
		return Plan{}, false
	}
	p := Plan{Description: desc, Interpreter: "model:" + model, Filter: f, Notes: notes}
	if regexp.MustCompile(`\b(me|i|my)\b`).MatchString(strings.ToLower(desc)) && regexp.MustCompile(`\b(someone|other person|they|stranger)\b`).MatchString(strings.ToLower(desc)) {
		p.Unsupported = append(p.Unsupported, "Speakers are not identified, so \"me\" and the other person cannot be told apart.")
	}
	p.finish(avail)
	return p, true
}
