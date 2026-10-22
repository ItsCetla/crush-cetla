package model

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// leadingSpaces counts the spaces at the start of a stripped line.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// drawStatusLines draws the status bar into a screen buffer and returns the
// visible content of each row.
func drawStatusLines(t *testing.T, st *Status, w, h int) []string {
	t.Helper()
	scr := uv.NewScreenBuffer(w, h)
	st.Draw(scr, uv.Rect(0, 0, w, h))
	lines := strings.Split(ansi.Strip(scr.Render()), "\n")
	return lines
}

func TestStatusDrawExpandedHelpRowsAlignWithBadge(t *testing.T) {
	t.Parallel()

	u := newPrismTestUI()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	st := u.status
	st.helpKm = u
	st.SetWidth(100)
	st.ToggleHelp()
	st.SetMode(uiInputModePlan, false, false)

	lines := drawStatusLines(t, st, 100, 6)
	require.True(t, strings.HasPrefix(lines[0], strings.Repeat(" ", badgeLeftInset)+"  PLAN  "),
		"the badge row must start with the badge inset and its padding: %q", lines[0])

	// Every subsequent help row must start at the same column as the hints
	// on the badge row: badge inset + badge width + separator + help padding.
	badge := st.modeBadge()
	wantHintsCol := badgeLeftInset + lipgloss.Width(badge) + 1 + u.com.Styles.Status.Help.GetPaddingLeft()
	for i, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		require.Equal(t, wantHintsCol, leadingSpaces(line),
			"expanded help row %d must align with the badge row hints", i+1)
	}
}

func TestStatusAlwaysShowsModeBadge(t *testing.T) {
	t.Parallel()

	u := newPrismTestUI()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	st := u.status
	st.helpKm = u
	st.SetWidth(100)

	var widths []int
	for _, tc := range []struct {
		mode uiInputMode
		auto bool
		want string
	}{
		{uiInputModeCode, false, "MANUAL"},
		{uiInputModePlan, false, "PLAN"},
		{uiInputModeCode, true, "AUTO"},
	} {
		st.SetMode(tc.mode, tc.auto, false)
		lines := drawStatusLines(t, st, 100, 1)
		require.Equal(t, tc.want, strings.TrimSpace(lines[0][:badgeLeftInset+lipgloss.Width(st.modeBadge())]))
		widths = append(widths, lipgloss.Width(st.modeBadge()))
	}
	require.Equal(t, widths[0], widths[1], "badges share a width so the hints never shift")
	require.Equal(t, widths[0], widths[2])
}

func TestFitPillsDropsLowestPriorityFirst(t *testing.T) {
	t.Parallel()

	pills := []statusPill{
		{"[wait]", pillPriorityWaiting},
		{"[mission]", pillPriorityMission},
		{"[net]", pillPriorityNet},
		{"[cost]", pillPriorityCost},
	}
	for _, tc := range []struct {
		budget int
		want   string
	}{
		{100, "[wait][mission][net][cost]"},
		{26, "[wait][mission][net][cost]"},
		{25, "[wait][mission][net]"},
		{20, "[wait][mission][net]"},
		{19, "[wait][mission]"},
		{15, "[wait][mission]"},
		{14, "[wait]"},
		{6, "[wait]"},
		{5, ""},
		{-3, ""},
	} {
		require.Equal(t, tc.want, fitPills(pills, tc.budget), "budget %d", tc.budget)
	}
	require.Empty(t, fitPills(nil, 10))
}

func TestStatusCockpitPills(t *testing.T) {
	t.Parallel()

	u := newPrismTestUI()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	st := u.status
	st.helpKm = u
	st.SetCockpit(2, "◆ 1/3")
	st.SetNetwork("↑ 2 hosts", true)
	st.SetCost("$0.42 today", costLevelPlain)

	row := func(w int) string {
		return strings.TrimRight(drawStatusLines(t, st, w, 1)[0], " ")
	}

	wide := row(120)
	require.True(t, strings.HasSuffix(wide, "2 waiting  ◆ 1/3  ↑ 2 hosts  $0.42 today"), "pills in order: %q", wide)
	require.Contains(t, wide, "MANUAL", "the mode badge stays on the left")

	// Narrower bars drop the cost first, then the network, then the
	// mission; the waiting count goes last.
	var prev int
	for w := 120; w >= 20; w-- {
		line := row(w)
		require.LessOrEqual(t, ansi.StringWidth(line), w)
		shown := 0
		for _, p := range []string{"2 waiting", "◆ 1/3", "↑ 2 hosts", "$0.42 today"} {
			if strings.Contains(line, p) {
				shown++
			}
		}
		if w < 120 {
			require.LessOrEqual(t, shown, prev, "width %d", w)
		}
		prev = shown
		if strings.Contains(line, "$0.42 today") {
			require.Contains(t, line, "↑ 2 hosts", "width %d", w)
		}
		if strings.Contains(line, "↑ 2 hosts") {
			require.Contains(t, line, "◆ 1/3", "width %d", w)
		}
		if strings.Contains(line, "◆ 1/3") {
			require.Contains(t, line, "2 waiting", "width %d", w)
		}
		require.Contains(t, line, "MANUAL", "width %d", w)
	}
	require.Contains(t, row(60), "2 waiting")
	require.NotContains(t, row(60), "$0.42 today")
	require.NotContains(t, row(30), "2 waiting")

	st.SetCockpit(0, "")
	require.NotContains(t, row(120), "waiting")
	require.NotContains(t, row(120), "◆")
}
