package model

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// DefaultStatusTTL is the default time-to-live for status messages.
const DefaultStatusTTL = 5 * time.Second

// badgeLeftInset is the number of cells between the status bar's left edge
// and the mode badge.
const badgeLeftInset = 1

// statusMinHelp is how many cells the help hints keep before the pills on
// the right start dropping.
const statusMinHelp = 24

// Status is the status bar and help model.
type Status struct {
	com      *common.Common
	hideHelp bool
	help     help.Model
	helpKm   help.KeyMap
	msg      util.InfoMsg

	// inputMode, yolo and edits drive the mode badge shown before the
	// help hints.
	inputMode uiInputMode
	yolo      bool
	edits     bool

	// cost is the spend pill drawn on the right, or "" for none.
	cost      string
	costLevel costLevel

	// net is the network pill drawn left of the cost pill, or "" for
	// none. netExternal colors it for traffic that left the machine.
	net         string
	netExternal bool

	// waiting is how many permission requests and questions wait for the
	// user across all tabs, and mission the open mission's progress
	// ("◆ 2/5") or "". Both draw left of the network pill.
	waiting int
	mission string
}

// Pill priorities: when the status bar is too narrow for every pill, the
// lowest one goes first.
const (
	pillPriorityCost = iota
	pillPriorityNet
	pillPriorityMission
	pillPriorityWaiting
)

// statusPill is one rendered pill on the right of the status bar.
type statusPill struct {
	text     string
	priority int
}

// costLevel picks the cost pill's color.
type costLevel int

const (
	costLevelPlain costLevel = iota
	costLevelWarn
	costLevelOver
)

// NewStatus creates a new status bar and help model.
func NewStatus(com *common.Common, km help.KeyMap) *Status {
	s := new(Status)
	s.com = com
	s.help = help.New()
	s.help.Styles = com.Styles.Help
	s.helpKm = km
	return s
}

// SetInfoMsg sets the status info message.
func (s *Status) SetInfoMsg(msg util.InfoMsg) {
	s.msg = msg
}

// ClearInfoMsg clears the status info message.
func (s *Status) ClearInfoMsg() {
	s.msg = util.InfoMsg{}
}

// SetMode sets the input mode, Auto (skip-permissions) and Edits state
// used for the mode badge.
func (s *Status) SetMode(mode uiInputMode, yolo, edits bool) {
	s.inputMode = mode
	s.yolo = yolo
	s.edits = edits
}

// SetCost sets the spend pill. An empty text hides it.
func (s *Status) SetCost(text string, level costLevel) {
	s.cost = text
	s.costLevel = level
}

// SetNetwork sets the network pill. An empty text hides it.
func (s *Status) SetNetwork(text string, external bool) {
	s.net = text
	s.netExternal = external
}

// SetCockpit sets the waiting and mission pills. Zero waiting and an
// empty mission hide them.
func (s *Status) SetCockpit(waiting int, mission string) {
	s.waiting = waiting
	s.mission = mission
}

// waitingPill renders the waiting pill, or "".
func (s *Status) waitingPill() string {
	if s.waiting <= 0 {
		return ""
	}
	return s.com.Styles.Status.WaitingPill.Render(waitingText(s.waiting))
}

// waitingText renders "1 waiting" or "3 waiting".
func waitingText(n int) string {
	return strconv.Itoa(n) + " waiting"
}

// missionPill renders the mission pill, or "".
func (s *Status) missionPill() string {
	if s.mission == "" {
		return ""
	}
	return s.com.Styles.Status.MissionPill.Render(s.mission)
}

// pills returns the right-hand pills in display order: waiting, mission,
// network, cost. Hidden pills are left out.
func (s *Status) pills() []statusPill {
	all := []statusPill{
		{s.waitingPill(), pillPriorityWaiting},
		{s.missionPill(), pillPriorityMission},
		{s.netPill(), pillPriorityNet},
		{s.costPill(), pillPriorityCost},
	}
	out := all[:0]
	for _, p := range all {
		if p.text != "" {
			out = append(out, p)
		}
	}
	return out
}

// fitPills joins the pills that fit in budget cells, keeping their order.
// It drops the lowest priority pill first until the rest fit.
func fitPills(pills []statusPill, budget int) string {
	keep := slices.Clone(pills)
	width := func() int {
		w := 0
		for _, p := range keep {
			w += lipgloss.Width(p.text)
		}
		return w
	}
	for len(keep) > 0 && width() > budget {
		low := 0
		for i, p := range keep {
			if p.priority < keep[low].priority {
				low = i
			}
		}
		keep = slices.Delete(keep, low, low+1)
	}
	var b strings.Builder
	for _, p := range keep {
		b.WriteString(p.text)
	}
	return b.String()
}

// netPill renders the network pill, or "".
func (s *Status) netPill() string {
	if s.net == "" {
		return ""
	}
	t := s.com.Styles
	if s.netExternal {
		return t.Status.NetPillExternal.Render(s.net)
	}
	return t.Status.NetPill.Render(s.net)
}

// costPill renders the spend pill, or "".
func (s *Status) costPill() string {
	if s.cost == "" {
		return ""
	}
	t := s.com.Styles
	switch s.costLevel {
	case costLevelOver:
		return t.Status.CostPillOver.Render(s.cost)
	case costLevelWarn:
		return t.Status.CostPillWarn.Render(s.cost)
	default:
		return t.Status.CostPill.Render(s.cost)
	}
}

// modeBadge renders the badge for the current permission mode: MANUAL,
// PLAN, EDITS or AUTO. One is always shown so the mode is never a guess.
func (s *Status) modeBadge() string {
	t := s.com.Styles
	switch permissionModeOf(s.inputMode, s.yolo, s.edits) {
	case permissionModePlan:
		return t.Status.ModeBadgePlan.String()
	case permissionModeAuto:
		return t.Status.ModeBadgeYolo.String()
	case permissionModeEdits:
		return t.Status.ModeBadgeEdits.String()
	default:
		return t.Status.ModeBadgeManual.String()
	}
}

// SetWidth sets the width of the status bar and help view.
func (s *Status) SetWidth(width int) {
	helpStyle := s.com.Styles.Status.Help
	horizontalPadding := helpStyle.GetPaddingLeft() + helpStyle.GetPaddingRight()
	s.help.SetWidth(width - horizontalPadding)
}

// ShowingAll returns whether the full help view is shown.
func (s *Status) ShowingAll() bool {
	return s.help.ShowAll
}

// ToggleHelp toggles the full help view.
func (s *Status) ToggleHelp() {
	s.help.ShowAll = !s.help.ShowAll
}

// SetHideHelp sets whether the app is on the onboarding flow.
func (s *Status) SetHideHelp(hideHelp bool) {
	s.hideHelp = hideHelp
}

// Draw draws the status bar onto the screen.
func (s *Status) Draw(scr uv.Screen, area uv.Rectangle) {
	if !s.hideHelp {
		helpStyle := s.com.Styles.Status.Help
		helpWidth := area.Dx() - helpStyle.GetPaddingLeft() - helpStyle.GetPaddingRight()
		badge := s.modeBadge()
		// The pills get what's left after the badge and a minimum of
		// hints. Narrow terminals drop the least urgent pills first; the
		// cockpit has the rest.
		budget := area.Dx() - statusMinHelp - 1
		if badge != "" {
			budget -= lipgloss.Width(badge) + 1 + badgeLeftInset
		}
		pill := fitPills(s.pills(), budget)
		pillWidth := lipgloss.Width(pill)
		if pillWidth > 0 {
			helpWidth -= pillWidth + 1
		}
		if badge != "" {
			// Shrink the hints so the badge does not push them past the
			// status area.
			helpWidth -= lipgloss.Width(badge) + 1 + badgeLeftInset
		}
		s.help.SetWidth(max(0, helpWidth))
		helpView := helpStyle.Render(s.help.View(s.helpKm))
		if badge != "" {
			// Indent the rows after the first so the expanded help lines up
			// with the hints on the badge row.
			indent := strings.Repeat(" ", badgeLeftInset+lipgloss.Width(badge)+1)
			helpView = strings.ReplaceAll(helpView, "\n", "\n"+indent)
			helpView = strings.Repeat(" ", badgeLeftInset) + badge + " " + helpView
		}
		uv.NewStyledString(helpView).Draw(scr, area)
		if pillWidth > 0 {
			pillArea := uv.Rect(area.Max.X-pillWidth-1, area.Min.Y, pillWidth, 1)
			uv.NewStyledString(pill).Draw(scr, pillArea)
		}
	}

	// Render notifications
	if s.msg.IsEmpty() {
		return
	}

	var indStyle lipgloss.Style
	var msgStyle lipgloss.Style
	// Mode banners show the same badge that sits next to the help hints, so
	// they honor the same left inset to keep the indicator from jumping when
	// the banner appears or expires.
	indInset := 0
	switch s.msg.Type {
	case util.InfoTypePlan:
		indStyle = s.com.Styles.Status.ModeBannerPlanBadge
		msgStyle = s.com.Styles.Status.ModeBannerPlan
		indInset = badgeLeftInset
	case util.InfoTypeYolo:
		indStyle = s.com.Styles.Status.ModeBannerYoloBadge
		msgStyle = s.com.Styles.Status.ModeBannerYolo
		indInset = badgeLeftInset
	case util.InfoTypeManual:
		indStyle = s.com.Styles.Status.ModeBannerManualBadge
		msgStyle = s.com.Styles.Status.ModeBannerManual
		indInset = badgeLeftInset
	case util.InfoTypeEdits:
		indStyle = s.com.Styles.Status.ModeBannerEditsBadge
		msgStyle = s.com.Styles.Status.ModeBannerEdits
		indInset = badgeLeftInset
	case util.InfoTypeError:
		indStyle = s.com.Styles.Status.ErrorIndicator
		msgStyle = s.com.Styles.Status.ErrorMessage
	case util.InfoTypeWarn:
		indStyle = s.com.Styles.Status.WarnIndicator
		msgStyle = s.com.Styles.Status.WarnMessage
	case util.InfoTypeInfo:
		indStyle = s.com.Styles.Status.InfoIndicator
		msgStyle = s.com.Styles.Status.InfoMessage
	case util.InfoTypeSuccess:
		indStyle = s.com.Styles.Status.SuccessIndicator
		msgStyle = s.com.Styles.Status.SuccessMessage
	}

	ind := indStyle.String()
	indWidth := lipgloss.Width(ind)
	msgPad := msgStyle.GetPaddingLeft() + msgStyle.GetPaddingRight()
	avail := max(0, area.Dx()-indWidth-msgPad-indInset)
	msg := strings.Join(strings.Split(s.msg.Msg, "\n"), " ")
	msg = ansi.Truncate(msg, avail, "…")
	if w := lipgloss.Width(msg); w < avail {
		msg += strings.Repeat(" ", avail-w)
	}
	info := msgStyle.Render(msg)

	// Draw the info message over the help view
	uv.NewStyledString(strings.Repeat(" ", indInset)+ind+info).Draw(scr, area)
}

// clearInfoMsgCmd returns a command that clears the info message after the
// given TTL.
func clearInfoMsgCmd(ttl time.Duration) tea.Cmd {
	return tea.Tick(ttl, func(time.Time) tea.Msg {
		return util.ClearStatusMsg{}
	})
}
