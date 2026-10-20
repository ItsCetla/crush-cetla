package dialog

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/magiccontext"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ContextID is the identifier for the context settings dialog.
const ContextID = "context"

// ActionOpenContext opens the context settings dialog.
type ActionOpenContext struct{}

// ActionSetMagicContext asks the UI to turn Magic Context on or off and
// hand the result back through SetResult.
type ActionSetMagicContext struct {
	Enabled bool
}

// ContextQuery reports whether value is the context command typed into
// the editor ("/context" or "/magic-context").
func ContextQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/context", "/magic-context":
		return true
	}
	return false
}

// Context shows how Ta-Da manages long conversations and lets the user
// turn Magic Context on or off.
type Context struct {
	com   *common.Common
	width int

	saving bool
	notice string
	err    string

	help   help.Model
	keyMap struct {
		Toggle key.Binding
		Close  key.Binding
	}
}

var _ Dialog = (*Context)(nil)

// NewContext creates the context settings dialog.
func NewContext(com *common.Common) *Context {
	d := &Context{com: com}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Toggle = key.NewBinding(
		key.WithKeys("space", " ", "enter"),
		key.WithHelp("space", "on/off"),
	)
	d.keyMap.Close = CloseKey
	return d
}

// ID implements Dialog.
func (d *Context) ID() string { return ContextID }

// SetResult shows the outcome of a change.
func (d *Context) SetResult(notice string, err error) {
	d.saving = false
	if err != nil {
		d.err = err.Error()
		d.notice = ""
		return
	}
	d.err = ""
	d.notice = notice
}

func (d *Context) options() *config.Options {
	if cfg := d.com.Config(); cfg != nil {
		return cfg.Options
	}
	return nil
}

// HandleMsg implements Dialog.
func (d *Context) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(k, d.keyMap.Close):
		return ActionClose{}
	case key.Matches(k, d.keyMap.Toggle):
		if d.saving {
			return nil
		}
		d.saving = true
		d.notice, d.err = "", ""
		return ActionSetMagicContext{Enabled: !d.options().MagicContextEnabled()}
	}
	return nil
}

// Draw implements Dialog.
func (d *Context) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	d.width = max(0, min(76, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	DrawCenter(scr, area, d.content())
	return nil
}

func (d *Context) content() string {
	t := d.com.Styles
	inner := max(0, d.width-t.Dialog.View.GetHorizontalFrameSize())
	width := max(0, inner-2)
	rc := NewRenderContext(t, d.width)
	rc.Title = "Context"

	opts := d.options()
	on := opts.MagicContextEnabled()
	state := t.Control.Off.Render("Off")
	if on {
		state = t.Control.On.Render("On")
	}
	threshold := magiccontext.DefaultExecuteThreshold
	if opts != nil && opts.MagicContext != nil && opts.MagicContext.ExecuteThresholdPercentage > 0 {
		threshold = opts.MagicContext.ExecuteThresholdPercentage
	}

	m := t.Control.Muted
	lines := []string{
		t.Control.Label.Render("Magic Context") + "  " + state,
		"",
	}
	if on {
		lines = append(lines,
			m.Render("Long chats stay usable: old tool output the model no longer"),
			m.Render("needs is dropped, and a running history is written in the"),
			m.Render(fmt.Sprintf("background once the context is %d%% full. Use /ctx-status", threshold)),
			m.Render("to see what it is doing."),
		)
	} else {
		lines = append(lines,
			m.Render("Off: when a chat fills the model's context, Ta-Da summarizes"),
			m.Render("it and starts over from the summary."),
			"",
			m.Render("On: old tool output the model no longer needs is dropped,"),
			m.Render("and a running history is written in the background, so long"),
			m.Render("chats keep going without a full summary."),
		)
	}
	lines = append(lines, "",
		m.Render("Saved to your global config. Applies from the next message."),
	)

	pad := lipgloss.NewStyle().Padding(0, 1)
	for i, l := range lines {
		lines[i] = pad.Render(ansi.Truncate(l, width, "…"))
	}
	rc.AddPart(strings.Join(lines, "\n"))

	var foot string
	switch {
	case d.saving:
		foot = m.Render("Saving…")
	case d.err != "":
		foot = t.Control.Alert.Render("! " + d.err)
	case d.notice != "":
		foot = m.Render(d.notice)
	}
	rc.AddPart(pad.Render(ansi.Truncate(foot, width, "…")))
	rc.Help = renderDialogHelp(t, &d.help, d, inner)
	return rc.Render()
}

// ShortHelp implements help.KeyMap.
func (d *Context) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Toggle, d.keyMap.Close}
}

// FullHelp implements help.KeyMap.
func (d *Context) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
