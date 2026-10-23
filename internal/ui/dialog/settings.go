package dialog

import (
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/update"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// SettingsID is the identifier for the settings dialog.
const SettingsID = "settings"

// ActionOpenSettings opens the settings dialog.
type ActionOpenSettings struct{}

// ActionSetSetting asks the UI to save Value (a bool or a string) to the
// config field Key in the global config and hand the outcome back through
// SetResult.
type ActionSetSetting struct {
	Key   string
	Value any
}

// SettingsQuery reports whether value is the settings command typed into
// the editor.
func SettingsQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/settings", "/preferences":
		return true
	}
	return false
}

// Settings groups, in display order.
const (
	settingsGroupDisplay   = "Display"
	settingsGroupBehaviour = "Behaviour"
	settingsGroupPrivacy   = "Privacy"
	settingsGroupAdvanced  = "Advanced"
)

// When a change takes effect, for settings that do not apply at once.
const (
	appliesRestart   = "Applies on restart."
	appliesNextStart = "Applies the next time Ta-Da starts."
	appliesOnQuit    = "Applies when you quit."
	appliesNewTurns  = "Applies to new turns."
)

type settingKind int

const (
	settingBool settingKind = iota
	settingEnum
)

// settingChoice is one value of an enum setting.
type settingChoice struct {
	value string
	label string
}

// setting describes one row of the settings screen.
type setting struct {
	key   string
	group string
	label string
	desc  string
	kind  settingKind
	// choices are the values of an enum setting, in cycling order.
	choices []settingChoice
	// stored reads the value as the config holds it: a bool for a bool
	// setting, a string for an enum.
	stored func(o *config.Options) any
	// invert shows a bool setting as the opposite of its stored value,
	// for "disable_*" fields.
	invert bool
	// env is the environment variable that can force the setting, and
	// envValue reports the stored value it forces, if it does.
	env      string
	envValue func() (any, bool)
	// applies says when a change takes effect; empty means at once.
	applies string
	// save turns an enum choice into the value written to the config,
	// for enums stored as something other than a string. Nil saves the
	// choice as is.
	save func(choice string) any
}

func tuiOpts(o *config.Options) *config.TUIOptions {
	if o == nil || o.TUI == nil {
		return &config.TUIOptions{}
	}
	return o.TUI
}

// envOff reports whether a TADA_NO_* style variable is set to anything
// but "", "0" or "false".
func envOff(name string) func() (any, bool) {
	return func() (any, bool) {
		v, ok := os.LookupEnv(name)
		v = strings.TrimSpace(v)
		if !ok || v == "" || v == "0" || strings.EqualFold(v, "false") {
			return nil, false
		}
		return false, true
	}
}

func boolPtr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// settingsList is every setting the screen shows, grouped and in order.
var settingsList = []setting{
	{
		key: "options.tui.compact_mode", group: settingsGroupDisplay, label: "Compact mode",
		desc:   "Hide the sidebar and use a tighter layout.",
		stored: func(o *config.Options) any { return tuiOpts(o).CompactMode },
	},
	{
		key: "options.tui.diff_mode", group: settingsGroupDisplay, label: "Diff style",
		desc: "Diffs in permission prompts: picked by width, unified or split.",
		kind: settingEnum,
		choices: []settingChoice{
			{"", "auto"}, {"unified", "unified"}, {"split", "split"},
		},
		stored: func(o *config.Options) any { return tuiOpts(o).DiffMode },
	},
	{
		key: "options.tui.scrollbar", group: settingsGroupDisplay, label: "Chat scrollbar",
		desc:    "Show the chat scrollbar while scrolling, always, or never.",
		kind:    settingEnum,
		applies: appliesRestart,
		choices: []settingChoice{
			{config.ScrollbarDefault, "default"}, {config.ScrollbarAlways, "always"}, {config.ScrollbarNever, "never"},
		},
		stored: func(o *config.Options) any {
			if s := tuiOpts(o).Scrollbar; s != "" {
				return s
			}
			return config.ScrollbarDefault
		},
	},
	{
		key: "options.tui.transparent", group: settingsGroupDisplay, label: "Transparent background",
		desc:   "Let the terminal's own background show through.",
		stored: func(o *config.Options) any { return tuiOpts(o).IsTransparent() },
	},
	{
		key: "options.tui.text_width", group: settingsGroupDisplay, label: "Chat width",
		desc: "How wide chat text may grow: the whole window or a fixed column count.",
		kind: settingEnum,
		choices: []settingChoice{
			{"0", "full"}, {"100", "100"}, {"120", "120"}, {"160", "160"},
		},
		stored: func(o *config.Options) any { return strconv.Itoa(tuiOpts(o).TextWidth) },
		save: func(choice string) any {
			n, _ := strconv.Atoi(choice)
			return n
		},
	},
	{
		key: "options.tui.rich_blocks", group: settingsGroupDisplay, label: "Rich tables and charts",
		desc:     "Draw tables and charts in replies as styled blocks.",
		env:      "TADA_NO_RICH",
		envValue: envOff("TADA_NO_RICH"),
		stored:   func(o *config.Options) any { return boolPtr(tuiOpts(o).RichBlocks, true) },
	},
	{
		key: "options.tui.fold_tools", group: settingsGroupDisplay, label: "Fold tool calls",
		desc:    "Fold a finished turn's tool calls into one \"Worked for\" line.",
		applies: appliesNewTurns,
		stored:  func(o *config.Options) any { return tuiOpts(o).FoldToolsEnabled() },
	},
	{
		key: "options.tui.ambient", group: settingsGroupDisplay, label: "Ambient line",
		desc:     "Draw a calm line above the editor while the agent works.",
		env:      "TADA_NO_AMBIENT",
		envValue: envOff("TADA_NO_AMBIENT"),
		stored:   func(o *config.Options) any { return boolPtr(tuiOpts(o).Ambient, true) },
	},
	{
		key: "options.tui.vertical_tabs", group: settingsGroupDisplay, label: "Tabs in sidebar",
		desc:   "List session tabs in the sidebar instead of above the chat.",
		stored: func(o *config.Options) any { return tuiOpts(o).VerticalTabs },
	},
	{
		key: "options.tui.intro", group: settingsGroupDisplay, label: "Startup intro",
		desc:     "Play the short animated intro when Ta-Da starts.",
		applies:  appliesNextStart,
		env:      "TADA_NO_INTRO",
		envValue: envOff("TADA_NO_INTRO"),
		stored:   func(o *config.Options) any { return tuiOpts(o).IntroEnabled() },
	},
	{
		key: "options.tui.exit_banner", group: settingsGroupDisplay, label: "Exit banner",
		desc:    "What Ta-Da prints after you quit.",
		kind:    settingEnum,
		applies: appliesOnQuit,
		choices: []settingChoice{
			{string(config.ExitBannerDefault), "default"},
			{string(config.ExitBannerCompact), "compact"},
			{string(config.ExitBannerNone), "none"},
		},
		stored: func(o *config.Options) any {
			if b := tuiOpts(o).ExitBanner; b != "" {
				return string(b)
			}
			return string(config.ExitBannerDefault)
		},
	},
	{
		key: "options.tui.mouse", group: settingsGroupBehaviour, label: "Mouse",
		desc:   "Capture the mouse for clicks and scrolling. Off lets the terminal select text.",
		stored: func(o *config.Options) any { return boolPtr(tuiOpts(o).Mouse, true) },
	},
	{
		key: "options.tui.restore_tabs", group: settingsGroupBehaviour, label: "Restore tabs",
		desc:   "Reopen the last run's session tabs in this project on start.",
		stored: func(o *config.Options) any { return tuiOpts(o).RestoreTabsEnabled() },
	},
	{
		key: "options.notifications", group: settingsGroupBehaviour, label: "Notifications",
		desc: "How Ta-Da notifies you: picked for the terminal, or a fixed style.",
		kind: settingEnum,
		choices: []settingChoice{
			{"auto", "auto"}, {"native", "native"}, {"osc", "osc"}, {"bell", "bell"}, {"disabled", "off"},
		},
		stored: func(o *config.Options) any {
			if o != nil && o.Notifications != "" {
				return o.Notifications
			}
			return "auto"
		},
	},
	{
		key: "options.tui.notify_bell", group: settingsGroupBehaviour, label: "Bell for other tabs",
		desc:   "Ring the bell when a session you are not looking at needs you.",
		stored: func(o *config.Options) any { return tuiOpts(o).NotifyBell },
	},
	{
		key: "options.disable_auto_summarize", group: settingsGroupBehaviour, label: "Auto-summarize",
		desc:    "Summarize a chat and carry on when it fills the model's context.",
		invert:  true,
		applies: appliesRestart,
		stored:  func(o *config.Options) any { return o != nil && o.DisableAutoSummarize },
	},
	{
		key: "options.tui.claude_sessions", group: settingsGroupBehaviour, label: "Claude Code sessions",
		desc:    "Show the Claude Code sessions running on this machine in the sidebar.",
		applies: appliesRestart,
		stored:  func(o *config.Options) any { return tuiOpts(o).ClaudeSessions },
	},
	{
		key: "options.enable_web_tools", group: settingsGroupPrivacy, label: "Web tools",
		desc: "Let the agent fetch pages and files from the web.",
		env:  "TADA_ENABLE_WEB_TOOLS",
		envValue: func() (any, bool) {
			if v, err := strconv.ParseBool(os.Getenv("TADA_ENABLE_WEB_TOOLS")); err == nil {
				return v, true
			}
			return nil, false
		},
		stored: func(o *config.Options) any { return o != nil && o.EnableWebTools },
	},
	{
		key: "options.update_check", group: settingsGroupPrivacy, label: "Update check",
		desc:     "Ask GitHub for a new Ta-Da release now and then. Only api.github.com.",
		applies:  appliesRestart,
		env:      update.NoCheckEnv,
		envValue: envOff(update.NoCheckEnv),
		stored:   func(o *config.Options) any { return o == nil || boolPtr(o.UpdateCheck, true) },
	},
	{
		key: "options.auto_lsp", group: settingsGroupAdvanced, label: "Auto LSP",
		desc:    "Start language servers on their own based on the project's files.",
		applies: appliesRestart,
		stored:  func(o *config.Options) any { return o == nil || boolPtr(o.AutoLSP, true) },
	},
	{
		key: "options.progress", group: settingsGroupAdvanced, label: "Terminal progress",
		desc:   "Show a progress indicator in the terminal while the agent works.",
		stored: func(o *config.Options) any { return o == nil || boolPtr(o.Progress, true) },
	},
	{
		key: "options.debug", group: settingsGroupAdvanced, label: "Debug logging",
		desc:    "Write debug detail to the log file.",
		applies: appliesRestart,
		stored:  func(o *config.Options) any { return o != nil && o.Debug },
	},
	{
		key: "options.debug_lsp", group: settingsGroupAdvanced, label: "LSP debug logging",
		desc:    "Log the messages exchanged with language servers.",
		applies: appliesRestart,
		stored:  func(o *config.Options) any { return o != nil && o.DebugLSP },
	},
}

// SettingKeys returns the config keys the settings screen shows.
func SettingKeys() []string {
	keys := make([]string, len(settingsList))
	for i, s := range settingsList {
		keys[i] = s.key
	}
	return keys
}

func findSetting(key string) (setting, bool) {
	for _, s := range settingsList {
		if s.key == key {
			return s, true
		}
	}
	return setting{}, false
}

// SettingStored reports the value cfg holds for a settings screen key,
// ignoring environment variables: a bool or a string. ok is false for an
// unknown key.
func SettingStored(cfg *config.Config, key string) (any, bool) {
	s, ok := findSetting(key)
	if !ok {
		return nil, false
	}
	var o *config.Options
	if cfg != nil {
		o = cfg.Options
	}
	return s.stored(o), true
}

// Settings lists the common options with their effective value and where
// it comes from, and saves changes to the global config.
type Settings struct {
	com *common.Common

	width  int
	height int
	cursor int
	offset int

	sources      map[string]config.FieldSource
	sourcesErr   string
	sourcesReady bool

	saving bool
	notice string
	err    string

	help   help.Model
	keyMap struct {
		Up     key.Binding
		Down   key.Binding
		Toggle key.Binding
		Change key.Binding
		Prev   key.Binding
		Next   key.Binding
		Close  key.Binding
	}
}

var _ Dialog = (*Settings)(nil)

// NewSettings creates the settings dialog.
func NewSettings(com *common.Common) *Settings {
	d := &Settings{com: com}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up", "k", "ctrl+p"), key.WithHelp("↑↓", "choose"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j", "ctrl+n"))
	d.keyMap.Toggle = key.NewBinding(key.WithKeys("space", " ", "enter"), key.WithHelp("space", "change"))
	d.keyMap.Change = key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←→", "cycle"))
	d.keyMap.Prev = key.NewBinding(key.WithKeys("left", "h"))
	d.keyMap.Next = key.NewBinding(key.WithKeys("right", "l"))
	d.keyMap.Close = CloseKey
	return d
}

// ID implements Dialog.
func (d *Settings) ID() string { return SettingsID }

// SetSources records which config file sets each key, from
// [config.FieldSources].
func (d *Settings) SetSources(sources map[string]config.FieldSource, err error) {
	d.sourcesReady = true
	d.sources = sources
	d.sourcesErr = ""
	if err != nil {
		d.sourcesErr = err.Error()
	}
}

// SetResult shows the outcome of a change.
func (d *Settings) SetResult(notice string, err error) {
	d.saving = false
	if err != nil {
		d.err = err.Error()
		d.notice = ""
		return
	}
	d.err = ""
	d.notice = notice
}

func (d *Settings) options() *config.Options {
	if d.com == nil || d.com.Workspace == nil {
		return nil
	}
	if cfg := d.com.Config(); cfg != nil {
		return cfg.Options
	}
	return nil
}

// value returns the effective stored value of s and whether an
// environment variable forces it.
func (d *Settings) value(s setting) (any, bool) {
	if s.envValue != nil {
		if v, ok := s.envValue(); ok {
			return v, true
		}
	}
	return s.stored(d.options()), false
}

// next returns the value a change of s by step (+1 or -1) saves.
func (d *Settings) next(s setting, step int) any {
	v, _ := d.value(s)
	if s.kind == settingBool {
		b, _ := v.(bool)
		return !b
	}
	cur, _ := v.(string)
	i := slices.IndexFunc(s.choices, func(c settingChoice) bool { return c.value == cur })
	if i < 0 {
		i = 0
		if step < 0 {
			i = 1
		}
	}
	n := len(s.choices)
	return s.choices[((i+step)%n+n)%n].value
}

// HandleMsg implements Dialog.
func (d *Settings) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(k, d.keyMap.Close):
		return ActionClose{}
	case key.Matches(k, d.keyMap.Up):
		d.move(-1)
	case key.Matches(k, d.keyMap.Down):
		d.move(1)
	case key.Matches(k, d.keyMap.Toggle):
		return d.change(1, true)
	case key.Matches(k, d.keyMap.Prev):
		return d.change(-1, false)
	case key.Matches(k, d.keyMap.Next):
		return d.change(1, false)
	}
	return nil
}

func (d *Settings) move(step int) {
	d.cursor = max(0, min(len(settingsList)-1, d.cursor+step))
	d.notice, d.err = "", ""
}

// change asks to save the next value of the selected setting. Left and
// right only cycle enums; space and enter change any setting.
func (d *Settings) change(step int, anyKind bool) Action {
	s := settingsList[d.cursor]
	if d.saving || (!anyKind && s.kind != settingEnum) {
		return nil
	}
	if _, locked := d.value(s); locked {
		d.notice, d.err = "", ""
		return nil
	}
	// Bool settings flip the stored value, so an inverted one needs no
	// special case here.
	v := d.next(s, step)
	if str, ok := v.(string); ok && s.save != nil {
		v = s.save(str)
	}
	d.saving = true
	d.notice, d.err = "", ""
	return ActionSetSetting{Key: s.key, Value: v}
}

// sourceLabel says where the selected value of s comes from.
func (d *Settings) sourceLabel(s setting, locked bool) string {
	if locked {
		return "env"
	}
	if !d.sourcesReady {
		return "…"
	}
	src, ok := d.sources[s.key]
	if !ok {
		return "default"
	}
	switch src.Kind {
	case config.FieldSourceProject, config.FieldSourceWorkspace:
		return "project"
	case config.FieldSourceGlobal:
		if src.Shell {
			return "tadarc"
		}
		return "tada.json"
	}
	return "default"
}

// valueLabel renders the shown value of s as plain text.
func valueLabel(s setting, v any) string {
	if s.kind == settingBool {
		b, _ := v.(bool)
		if b != s.invert {
			return "On"
		}
		return "Off"
	}
	str, _ := v.(string)
	for _, c := range s.choices {
		if c.value == str {
			return c.label
		}
	}
	if str == "" {
		return s.choices[0].label
	}
	return str
}

// settingsRow is one line of the list: a group header or a setting.
type settingsRow struct {
	header  string
	setting int
}

func settingsRows() []settingsRow {
	var rows []settingsRow
	group := ""
	for i, s := range settingsList {
		if s.group != group {
			group = s.group
			rows = append(rows, settingsRow{header: group, setting: -1})
		}
		rows = append(rows, settingsRow{setting: i})
	}
	return rows
}

// Draw implements Dialog.
func (d *Settings) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	d.width = max(0, min(76, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	d.height = area.Dy()
	DrawCenter(scr, area, d.content())
	return nil
}

// listHeight is how many list rows fit: all of them, or what the height
// leaves after the rest of the dialog.
func (d *Settings) listHeight(total int) int {
	if d.height <= 0 {
		return total
	}
	overhead := lipgloss.Height(d.render(0))
	return max(1, min(total, d.height-overhead))
}

func (d *Settings) content() string {
	rows := settingsRows()
	return d.render(d.listHeight(len(rows)))
}

func (d *Settings) render(visible int) string {
	t := d.com.Styles
	inner := max(0, d.width-t.Dialog.View.GetHorizontalFrameSize())
	rc := NewRenderContext(t, d.width)
	rc.Title = "Settings"

	if visible > 0 {
		rc.AddPart(d.renderList(inner, visible))
	}

	s := settingsList[d.cursor]
	_, locked := d.value(s)
	m := t.Control.Muted
	pad := lipgloss.NewStyle().Padding(0, 1)
	width := max(0, inner-2)
	var status string
	switch {
	case d.saving:
		status = m.Render("Saving…")
	case d.err != "":
		status = t.Control.Alert.Render("! " + d.err)
	case d.notice != "":
		status = m.Render(d.notice)
	case locked:
		status = m.Render("Set by " + s.env + ". Unset it to change this here.")
	case d.sourceLabel(s, false) == "project":
		status = m.Render("A project config sets this. Saving here will not change it in this project.")
	case d.sourcesErr != "":
		status = t.Control.Alert.Render("! " + d.sourcesErr)
	case s.applies != "":
		status = m.Render(s.applies)
	default:
		status = m.Render("Applies now.")
	}
	foot := []string{
		pad.Render(ansi.Truncate(m.Render(s.desc), width, "…")),
		pad.Render(ansi.Truncate(status, width, "…")),
	}
	rc.AddPart("\n" + strings.Join(foot, "\n"))
	rc.Help = renderDialogHelp(t, &d.help, d, inner)
	return rc.Render()
}

// renderList draws visible rows of the list, scrolled to keep the
// selected setting (and its group header when possible) in view.
func (d *Settings) renderList(inner, visible int) string {
	t := d.com.Styles
	rows := settingsRows()
	sel := slices.IndexFunc(rows, func(r settingsRow) bool { return r.setting == d.cursor })
	top := sel
	if sel > 0 && rows[sel-1].setting < 0 {
		top = sel - 1
	}
	if top < d.offset {
		d.offset = top
	}
	if sel >= d.offset+visible {
		d.offset = sel - visible + 1
	}
	d.offset = max(0, min(d.offset, len(rows)-visible))

	rowWidth := inner
	scroll := visible < len(rows)
	if scroll {
		rowWidth = max(0, inner-1)
	}
	lines := make([]string, 0, visible)
	for _, r := range rows[d.offset:min(len(rows), d.offset+visible)] {
		if r.setting < 0 {
			if rowWidth < 3 {
				lines = append(lines, common.Section(t, r.header, rowWidth))
			} else {
				lines = append(lines, " "+common.Section(t, r.header, rowWidth-2)+" ")
			}
			continue
		}
		lines = append(lines, d.renderRow(r.setting, rowWidth))
	}
	view := strings.Join(lines, "\n")
	if scroll {
		view = joinScrollbar(t, view, visible, len(rows), visible, d.offset)
	}
	return view
}

// Column widths of a setting row.
const (
	settingsValueWidth  = 8
	settingsSourceWidth = 9
)

func padRight(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func (d *Settings) renderRow(i, rowWidth int) string {
	t := d.com.Styles
	s := settingsList[i]
	v, locked := d.value(s)
	value := valueLabel(s, v)
	source := d.sourceLabel(s, locked)

	text := max(0, rowWidth-2)
	sourceW := settingsSourceWidth
	labelW := text - settingsValueWidth - sourceW - 4
	if labelW < 16 {
		sourceW = 0
		labelW = text - settingsValueWidth - 2
	}
	labelW = max(0, labelW)

	if i == d.cursor {
		plain := padRight(s.label, labelW) + "  " + padRight(value, settingsValueWidth)
		if sourceW > 0 {
			plain += "  " + padRight(source, sourceW)
		}
		return t.Dialog.SelectedItem.Render(padRight(plain, text))
	}

	label := t.Dialog.NormalItem.UnsetPadding().Render(padRight(s.label, labelW))
	valueStyle := t.Control.Value
	switch {
	case locked:
		valueStyle = t.Control.Muted
	case value == "On":
		valueStyle = t.Control.On
	case value == "Off":
		valueStyle = t.Control.Off
	}
	row := " " + label + "  " + valueStyle.Render(padRight(value, settingsValueWidth))
	if sourceW > 0 {
		row += "  " + t.Control.Muted.Render(padRight(source, sourceW))
	}
	return padRight(row, rowWidth)
}

// ShortHelp implements help.KeyMap.
func (d *Settings) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Up, d.keyMap.Toggle, d.keyMap.Change, d.keyMap.Close}
}

// FullHelp implements help.KeyMap.
func (d *Settings) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
