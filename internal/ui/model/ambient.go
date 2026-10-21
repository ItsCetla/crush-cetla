package model

import (
	"image/color"
	"math"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// Ambient mode draws one calm line above the editor while the agent works.
// Nothing pops up: the line breathes slowly, a soft glow drifts along it,
// and its color eases between hues as the agent moves from thinking to
// reading, editing or running commands. A dim label at its right end names
// what is happening. When the turn ends the line brightens once in a done
// (or failed) color and fades out.
//
// It is on unless options.tui.ambient is false or TADA_NO_AMBIENT is set.
// It follows agent events already flowing through Update; nothing polls.

const (
	// ambientFrame is the frame interval while the line shows.
	ambientFrame = time.Second / 15
	// ambientBreath is one slow in-and-out of the whole line.
	ambientBreath = 3200 * time.Millisecond
	// ambientDrift is one pass of the glow from left to right.
	ambientDrift = 2600 * time.Millisecond
	// ambientFadeIn and ambientFadeOut ease the line in and out.
	ambientFadeIn  = 600 * time.Millisecond
	ambientFadeOut = 1500 * time.Millisecond
	// ambientEase is how fast the color settles on a new hue.
	ambientEase = 350 * time.Millisecond

	ambientMinWidth = 12
	ambientMaxLabel = 60
)

// ambientActivity is what the agent is doing, which picks the line's hue.
type ambientActivity int

const (
	ambientThinking ambientActivity = iota
	ambientReading
	ambientEditing
	ambientRunning
	ambientDelegating
)

// ambientEnd is how the last turn ended.
type ambientEnd int

const (
	ambientEndNone ambientEnd = iota
	ambientEndDone
	ambientEndFailed
)

// ambientTickMsg advances the line's animation.
type ambientTickMsg struct{}

// ambientState is ambient mode's state. It lives on the UI model.
type ambientState struct {
	enabled bool

	// session is the session the line follows and phase its last phase.
	session string
	phase   tabPhase

	// on is set while a turn runs. start is when the line came in.
	on    bool
	start time.Time
	// end and endAt describe the fade after a turn ends.
	end   ambientEnd
	endAt time.Time

	activity ambientActivity
	label    string
	// seen holds the tool call IDs already noted this turn, and lastCall
	// the newest one, whose label fills in as its input streams.
	seen     map[string]struct{}
	lastCall string

	// cur is the line's eased hue and stepped when it was last moved.
	cur     colorful.Color
	curSet  bool
	stepped time.Time

	tickArmed bool
}

// newAmbientState returns ambient mode's state, on when enabled.
func newAmbientState(enabled bool) *ambientState {
	return &ambientState{enabled: enabled}
}

// ambientClassify maps a tool name to an activity.
func ambientClassify(name string) ambientActivity {
	switch name {
	case "edit", "multiedit", "write", "lsp_replace_symbol", "lsp_rename":
		return ambientEditing
	case "bash", "job_kill":
		return ambientRunning
	case "agent", "agentic_fetch":
		return ambientDelegating
	case "view", "ls", "read_mcp_resource", "job_output", "list_mcp_resources",
		"grep", "glob", "sourcegraph", "lsp_symbols", "lsp_references",
		"lsp_definition", "lsp_call_hierarchy", "lsp_diagnostics",
		"fetch", "web_fetch", "web_search", "download":
		return ambientReading
	}
	return ambientThinking
}

// ambientLabel describes a tool call in a few quiet words, from what is
// known of its input so far.
func ambientLabel(name, input string) string {
	field := func(keys ...string) string {
		for _, k := range keys {
			if v := jsonStringField(input, k); v != "" {
				return v
			}
		}
		return ""
	}
	base := func(p string) string {
		if p == "" {
			return ""
		}
		return filepath.Base(p)
	}
	var verb, what string
	switch name {
	case "edit", "multiedit", "lsp_replace_symbol", "lsp_rename":
		verb, what = "editing", base(field("file_path", "path"))
	case "write":
		verb, what = "writing", base(field("file_path", "path"))
	case "bash":
		verb, what = "running", field("description", "command")
	case "job_kill":
		verb = "stopping a job"
	case "view", "read_mcp_resource":
		verb, what = "reading", base(field("file_path", "path", "uri"))
	case "ls":
		verb, what = "looking in", base(field("path"))
	case "job_output":
		verb = "checking a job"
	case "grep", "glob", "sourcegraph", "lsp_symbols", "lsp_references",
		"lsp_definition", "lsp_call_hierarchy":
		verb, what = "searching", field("pattern", "query", "symbol")
	case "lsp_diagnostics":
		verb = "checking diagnostics"
	case "agent", "agentic_fetch":
		verb = "a helper is working"
	case "fetch", "web_fetch", "download":
		verb, what = "fetching", field("url")
	case "web_search":
		verb, what = "searching the web", field("query")
	case "todos":
		verb = "planning"
	default:
		verb = "using " + name
	}
	what = strings.Join(strings.Fields(what), " ")
	if len(what) > ambientMaxLabel {
		what = what[:ambientMaxLabel]
	}
	if what == "" {
		return verb
	}
	return verb + " " + what
}

// jsonStringField returns the string value of key in a JSON object that
// may still be streaming in, or "" when it is not there yet. It avoids a
// full parse because the input can be large and arrives many times.
func jsonStringField(input, key string) string {
	needle := `"` + key + `"`
	i := strings.Index(input, needle)
	if i < 0 {
		return ""
	}
	rest := strings.TrimLeft(input[i+len(needle):], " \t\r\n")
	if !strings.HasPrefix(rest, ":") {
		return ""
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	var b strings.Builder
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		switch c {
		case '"':
			return b.String()
		case '\\':
			if j+1 < len(rest) {
				j++
				switch rest[j] {
				case 'n', 't', 'r':
					b.WriteByte(' ')
				default:
					b.WriteByte(rest[j])
				}
			}
		default:
			b.WriteByte(c)
		}
		if b.Len() >= ambientMaxLabel*2 {
			return b.String()
		}
	}
	// The value is still streaming; show what is there.
	return b.String()
}

// startTurn brings the line in for a new turn.
func (a *ambientState) startTurn(now time.Time) {
	if !a.visible(now) {
		a.start = now
	}
	a.on = true
	a.end = ambientEndNone
	a.endAt = time.Time{}
	a.activity = ambientThinking
	a.label = "thinking"
	a.seen = make(map[string]struct{})
	a.lastCall = ""
}

// finish ends the turn with a soft fade in the done or failed color.
func (a *ambientState) finish(now time.Time, end ambientEnd) {
	a.on = false
	a.end = end
	a.endAt = now
	if end == ambientEndFailed {
		a.label = "failed"
	} else {
		a.label = "done"
	}
}

// hide drops the line now.
func (a *ambientState) hide() {
	a.on = false
	a.end = ambientEndNone
	a.endAt = time.Time{}
	a.curSet = false
}

// noteMessage keeps the hue and label in step with an assistant message
// of the followed session.
func (a *ambientState) noteMessage(msg message.Message, created bool) {
	if !a.on {
		return
	}
	calls := msg.ToolCalls()
	if created && len(calls) == 0 {
		a.activity = ambientThinking
		a.label = "thinking"
		return
	}
	for _, tc := range calls {
		if tc.ID == "" || tc.Name == "" {
			continue
		}
		if _, ok := a.seen[tc.ID]; !ok {
			a.seen[tc.ID] = struct{}{}
			a.lastCall = tc.ID
			a.activity = ambientClassify(tc.Name)
			a.label = ambientLabel(tc.Name, tc.Input)
			continue
		}
		if tc.ID == a.lastCall && !tc.Finished {
			a.label = ambientLabel(tc.Name, tc.Input)
		}
	}
}

// visible reports whether the line shows at now.
func (a *ambientState) visible(now time.Time) bool {
	if a == nil || !a.enabled {
		return false
	}
	return a.on || (a.end != ambientEndNone && now.Sub(a.endAt) < ambientFadeOut)
}

// alpha is how present the line is at now, from 0 (gone) to 1.
func (a *ambientState) alpha(now time.Time) float64 {
	if !a.visible(now) {
		return 0
	}
	in := smoothstep(float64(now.Sub(a.start)) / float64(ambientFadeIn))
	if a.on {
		return in
	}
	out := 1 - float64(now.Sub(a.endAt))/float64(ambientFadeOut)
	return in * out * out
}

// ambientPalette is the theme's ambient colors as blendable values.
type ambientPalette struct {
	bg, base, label colorful.Color
	hues            map[ambientActivity]colorful.Color
	done, failed    colorful.Color
}

// palette reads the ambient colors from the theme.
func (m *UI) ambientPalette() ambientPalette {
	t := &m.com.Styles.Ambient
	return ambientPalette{
		bg:    toColorful(t.Bg, colorful.Color{}),
		base:  toColorful(t.Base, colorful.Color{R: 0.2, G: 0.2, B: 0.22}),
		label: toColorful(t.Label, colorful.Color{R: 0.45, G: 0.45, B: 0.5}),
		hues: map[ambientActivity]colorful.Color{
			ambientThinking:   toColorful(t.Thinking, colorful.Color{R: 0.42, G: 0.31, B: 1}),
			ambientReading:    toColorful(t.Reading, colorful.Color{R: 0, G: 0.65, B: 1}),
			ambientEditing:    toColorful(t.Editing, colorful.Color{R: 1, G: 0.38, B: 0.9}),
			ambientRunning:    toColorful(t.Running, colorful.Color{R: 0.9, G: 0.85, B: 0.2}),
			ambientDelegating: toColorful(t.Delegating, colorful.Color{R: 1, G: 0.47, B: 0.6}),
		},
		done:   toColorful(t.Done, colorful.Color{R: 0, G: 1, B: 0.7}),
		failed: toColorful(t.Failed, colorful.Color{R: 1, G: 0.22, B: 0.35}),
	}
}

// toColorful converts c, or returns fallback when c is unset.
func toColorful(c color.Color, fallback colorful.Color) colorful.Color {
	if c == nil {
		return fallback
	}
	if cc, ok := colorful.MakeColor(c); ok {
		return cc
	}
	return fallback
}

// target is the hue the line is easing toward.
func (a *ambientState) target(p ambientPalette) colorful.Color {
	switch a.end {
	case ambientEndDone:
		return p.done
	case ambientEndFailed:
		return p.failed
	}
	return p.hues[a.activity]
}

// step eases the line's hue toward its target.
func (a *ambientState) step(now time.Time, p ambientPalette) {
	want := a.target(p)
	if !a.curSet {
		a.cur, a.curSet, a.stepped = want, true, now
		return
	}
	dt := now.Sub(a.stepped)
	a.stepped = now
	if dt <= 0 {
		return
	}
	// A turn ending switches to its color quickly so the brightening
	// reads as one gentle beat.
	ease := ambientEase
	if a.end != ambientEndNone {
		ease /= 3
	}
	k := 1 - math.Exp(-float64(dt)/float64(ease))
	a.cur = a.cur.BlendLab(want, k).Clamped()
}

// smoothstep eases x in [0, 1] in and out.
func smoothstep(x float64) float64 {
	x = math.Max(0, math.Min(1, x))
	return x * x * (3 - 2*x)
}

// intensity is how strongly the hue shows in column x of a line w wide at
// now: a slow breath over the whole line plus a soft drifting glow, or one
// bright beat fading out after the turn ends.
func (a *ambientState) intensity(x, w int, now time.Time) float64 {
	if a.end != ambientEndNone && !a.on {
		return 0.75
	}
	t := now.Sub(a.start).Seconds()
	breath := 0.5 + 0.5*math.Sin(2*math.Pi*t/ambientBreath.Seconds()-math.Pi/2)
	spread := math.Max(5, float64(w)/7)
	pass := math.Mod(t/ambientDrift.Seconds(), 1)
	center := pass*(float64(w)+4*spread) - 2*spread
	d := (float64(x) - center) / spread
	glow := math.Exp(-d * d)
	return 0.18 + 0.22*breath + 0.4*glow
}

// drawAmbient draws the ambient line on the editor's top row, which is
// kept free for attachments and is blank without them.
func (m *UI) drawAmbient(scr uv.Screen, area uv.Rectangle) {
	a := m.ambient
	now := time.Now()
	if !a.visible(now) || area.Dx() < ambientMinWidth || area.Dy() < 1 ||
		len(m.attachments.List()) > 0 {
		return
	}
	alpha := a.alpha(now)
	if alpha <= 0.01 {
		return
	}
	p := m.ambientPalette()
	hue := a.cur
	if !a.curSet {
		hue = a.target(p)
	}

	w := area.Dx()
	y := area.Min.Y
	label := ""
	if w >= 30 && a.label != "" {
		label = " " + ansi.Truncate(a.label, w/3, "…") + " "
	}
	lineW := w - ansi.StringWidth(label) - 1
	if label == "" {
		lineW = w
	}

	for x := range lineW {
		c := p.base.BlendLab(hue, a.intensity(x, w, now))
		c = p.bg.BlendLab(c, alpha).Clamped()
		scr.SetCell(area.Min.X+x, y, &uv.Cell{
			Content: "─",
			Width:   1,
			Style:   uv.Style{Fg: c},
		})
	}
	if label == "" {
		return
	}
	lc := p.label.BlendLab(hue, 0.35)
	lc = p.bg.BlendLab(lc, alpha).Clamped()
	uv.NewStyledString(ambientFg(lc, label)).Draw(scr, uv.Rect(area.Min.X+lineW, y, w-lineW, 1))
}

// ambientFg colors s with c.
func ambientFg(c colorful.Color, s string) string {
	return ansi.Style{}.ForegroundColor(c).Styled(s)
}

// trackAmbient feeds an Update message to ambient mode and returns its
// next tick. Update calls it after the main update so the tab phases it
// reads are current.
func (m *UI) trackAmbient(msg tea.Msg) tea.Cmd {
	a := m.ambient
	if a == nil || !a.enabled {
		return nil
	}
	now := time.Now()
	switch msg := msg.(type) {
	case ambientTickMsg:
		a.tickArmed = false
		a.step(now, m.ambientPalette())
		m.invalidateFrames()
	case pubsub.Event[message.Message]:
		if msg.Payload.Role == message.Assistant && a.session != "" &&
			m.tabOwner(msg.Payload.SessionID) == a.session {
			a.noteMessage(msg.Payload, msg.Type == pubsub.CreatedEvent)
			m.invalidateFrames()
		}
	}
	m.syncAmbientPhase(now)
	return m.armAmbientTick(now)
}

// syncAmbientPhase follows the viewed session's tab phase: a turn starting
// brings the line in, and one ending fades it out.
func (m *UI) syncAmbientPhase(now time.Time) {
	a := m.ambient
	sid := ""
	if m.state == uiChat && m.session != nil {
		sid = m.session.ID
	}
	phase := tabIdle
	if s := m.tabStatusOf(sid); s != nil && sid != "" {
		phase = s.phase
	}
	if sid != a.session {
		a.session, a.phase = sid, phase
		a.hide()
		if phase == tabWorking {
			a.startTurn(now)
		}
		m.invalidateFrames()
		return
	}
	if phase == a.phase {
		return
	}
	prev := a.phase
	a.phase = phase
	m.invalidateFrames()
	switch phase {
	case tabWorking:
		a.startTurn(now)
	case tabDone:
		if prev == tabWorking && a.on {
			a.finish(now, ambientEndDone)
		}
	case tabFailed:
		if prev == tabWorking && a.on {
			a.finish(now, ambientEndFailed)
		}
	default:
		a.hide()
	}
}

// armAmbientTick schedules the next frame while the line shows.
func (m *UI) armAmbientTick(now time.Time) tea.Cmd {
	a := m.ambient
	if a.tickArmed || !a.visible(now) {
		return nil
	}
	a.tickArmed = true
	return tea.Tick(ambientFrame, func(time.Time) tea.Msg { return ambientTickMsg{} })
}
