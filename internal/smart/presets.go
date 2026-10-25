package smart

// Preset is a saved filter.
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
	Filter      Filter `json:"filter"`
	// Source: the natural-language description it was made from, if any.
	Source    string `json:"source,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

func leaf(kind string, f func(n *Node)) Node {
	n := Node{Kind: kind}
	if f != nil {
		f(&n)
	}
	return n
}

func sound(labels ...string) Node { return Node{Kind: "audio_event", Labels: labels} }

func op(o string, window float64, kids ...Node) Node {
	return Node{Op: o, Window: window, Children: kids}
}

func ctx(f func(c *Context)) Context {
	c := DefaultContext()
	if f != nil {
		f(&c)
	}
	return c
}

// Builtins are the presets every user has. Each is a real combination of
// implemented detectors; editing one saves a copy.
func Builtins() []Preset {
	speechActive := leaf("speech", func(n *Node) { n.Mode = "active" })
	laughter := sound("laughter")
	return []Preset{
		{ID: "builtin:laughter", Name: "Laughter Detection", Builtin: true,
			Description: "Every moment with laughter, giggling or chuckling.",
			Filter: Filter{Rules: []Rule{
				{ID: "laugh", Name: "Laughter", Category: "emotion", When: laughter},
			}, Context: ctx(func(c *Context) { c.Pre, c.Post, c.Threshold = 6, 4, 0.4 })}},
		{ID: "builtin:loud", Name: "Loud Reactions", Builtin: true,
			Description: "Screaming, shouting, cheering and sudden jumps in loudness.",
			Filter: Filter{Rules: []Rule{
				{ID: "voice", Name: "Screaming or shouting", Category: "emotion", When: sound("scream", "shout")},
				{ID: "cheer", Name: "Cheering", Category: "emotion", When: sound("cheering", "applause")},
				{ID: "jump", Name: "Sudden loudness", Category: "audio", When: leaf("loudness", func(n *Node) { n.Mode = "sudden_rise"; n.DB = 15 })},
			}, Context: ctx(func(c *Context) { c.Pre, c.Post = 6, 5 })}},
		{ID: "builtin:funny", Name: "Funny Moments", Builtin: true,
			Description: "Jokes and their reactions: speech followed by laughter, strong laughter, laughing faces.",
			Filter: Filter{Rules: []Rule{
				{ID: "joke", Name: "Joke and reaction", Category: "conversation", When: op("then", 4, speechActive, laughter)},
				{ID: "laugh", Name: "Strong laughter", Category: "emotion", When: leaf("audio_event", func(n *Node) { n.Labels = []string{"laughter"}; n.MinScore = 0.6 })},
				{ID: "face", Name: "Laughing face with laughter", Category: "emotion", When: op("and", 2, leaf("expression", func(n *Node) { n.Label = "happy" }), laughter)},
			}, Context: ctx(func(c *Context) { c.Pre, c.Post = 15, 8 })}},
		{ID: "builtin:ometv", Name: "OmeTV Highlights", Builtin: true,
			Description: "Video-chat highlights: funny exchanges, surprised reactions, arguments, awkward silences and partners leaving.",
			Filter: Filter{Rules: []Rule{
				{ID: "funny", Name: "Funny exchange", Category: "conversation", When: op("then", 5, speechActive, laughter)},
				{ID: "qlaugh", Name: "Question answered with laughter", Category: "conversation", When: op("then", 8, leaf("question", nil), laughter)},
				{ID: "surprise", Name: "Surprised reaction", Category: "emotion", When: op("or", 0,
					leaf("expression", func(n *Node) { n.Label = "surprised" }), sound("gasp", "scream"),
					leaf("loudness", func(n *Node) { n.Mode = "sudden_rise" }))},
				{ID: "argue", Name: "Argument", Category: "conversation", When: op("and", 6,
					op("or", 0, leaf("emotion", func(n *Node) { n.Label = "angry" }), sound("shout")),
					leaf("speech", func(n *Node) { n.Mode = "overlap" }))},
				{ID: "awkward", Name: "Awkward silence", Category: "conversation", When: leaf("speech", func(n *Node) { n.Mode = "awkward_silence"; n.MinDuration = 4 })},
				{ID: "leave", Name: "Partner leaves", Category: "visual", When: op("and", 4, leaf("faces", func(n *Node) { n.Mode = "leave" }), leaf("scene_change", nil))},
			}, Context: ctx(func(c *Context) { c.Pre, c.Post, c.MaxSegment = 15, 10, 120 })}},
		{ID: "builtin:gaming", Name: "Gaming Highlights", Builtin: true,
			Description: "Goals (scoreboard), victories and defeats, eliminations, excited reactions and hectic action.",
			Filter: Filter{Rules: []Rule{
				{ID: "goal", Name: "Score change", Category: "gaming", When: leaf("score_change", nil)},
				{ID: "result", Name: "Victory or defeat", Category: "gaming", When: leaf("onscreen_text", func(n *Node) {
					n.Text = "victory|defeat|winner|you win|you lose|game over|victory royale|eliminated|mvp"
				})},
				{ID: "kill", Name: "Kill feed", Category: "gaming", When: leaf("killfeed", nil)},
				{ID: "hype", Name: "Excited reaction", Category: "emotion", When: op("or", 0, sound("shout", "cheering", "scream"),
					leaf("loudness", func(n *Node) { n.Mode = "sudden_rise"; n.DB = 18 }))},
				{ID: "action", Name: "Hectic action with loud sound", Category: "visual", When: op("and", 2, leaf("motion", func(n *Node) { n.Mode = "sudden" }), leaf("loudness", func(n *Node) { n.Mode = "loud" }))},
			}, Context: ctx(func(c *Context) { c.Pre, c.Post = 12, 6 })}},
		{ID: "builtin:rocketleague", Name: "Rocket League Goals", Builtin: true,
			Description: "Goals read from the scoreboard (top centre), with the reaction afterwards.",
			Filter: Filter{Rules: []Rule{
				{ID: "goal", Name: "Goal", Category: "gaming", When: leaf("score_change", func(n *Node) { r := DefaultScoreRegion; n.Region = &r })},
			}, Context: ctx(func(c *Context) { c.Pre, c.Post, c.Merge = 10, 8, false })}},
		{ID: "builtin:conversation", Name: "Conversation Highlights", Builtin: true,
			Description: "Lively conversation: questions with reactions, laughter during talk, sudden topic changes, rapid exchanges.",
			Filter: Filter{Rules: []Rule{
				{ID: "qa", Name: "Question and reaction", Category: "conversation", When: op("then", 10, leaf("question", nil), op("or", 0, laughter, leaf("speech", func(n *Node) { n.Mode = "speaker_change" })))},
				{ID: "laughtalk", Name: "Laughter while talking", Category: "conversation", When: op("and", 3, laughter, speechActive)},
				{ID: "shift", Name: "Sudden topic change", Category: "conversation", When: leaf("topic_shift", nil)},
				{ID: "rapid", Name: "Rapid exchange", Category: "conversation", When: leaf("speech", func(n *Node) { n.Mode = "overlap" })},
			}, Context: ctx(func(c *Context) { c.Pre, c.Post, c.MaxSegment = 15, 10, 120 })}},
		{ID: "builtin:dialogue", Name: "Interesting Dialogue", Builtin: true,
			Description: "Speech about stories, debates and surprising statements (semantic match; edit the topics to your own).",
			Filter: Filter{Rules: []Rule{
				{ID: "story", Name: "Funny or surprising story", Category: "conversation", When: leaf("dialogue", func(n *Node) { n.Text = "telling a funny or surprising story" })},
				{ID: "debate", Name: "Heated debate", Category: "conversation", When: leaf("dialogue", func(n *Node) { n.Text = "a heated debate or disagreement" })},
				{ID: "question", Name: "Personal question", Category: "conversation", When: op("and", 2, leaf("question", nil), leaf("dialogue", func(n *Node) { n.Text = "asking a personal question"; n.MinScore = 0.4 }))},
			}, Context: ctx(func(c *Context) { c.Pre, c.Post, c.MaxSegment = 10, 10, 150 })}},
		{ID: "builtin:custom", Name: "Custom", Builtin: true,
			Description: "Start empty and add your own conditions or describe what to find.",
			Filter:      Filter{Context: DefaultContext()}},
	}
}

// Builtin returns a built-in preset by id.
func Builtin(id string) (Preset, bool) {
	for _, p := range Builtins() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
