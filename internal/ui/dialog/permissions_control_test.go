package dialog

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newGradedPermissions(t *testing.T, req permission.PermissionRequest) *Permissions {
	t.Helper()
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	req.ID, req.ToolCallID = "perm-test", "tool-call-test"
	return NewPermissions(com, req)
}

func TestPermissions_AlwaysAskHidesSessionOption(t *testing.T) {
	t.Parallel()

	for _, req := range []permission.PermissionRequest{
		{ToolName: "bash", Class: permission.ClassDelete, AlwaysAsk: true, Reason: "deletes files"},
		{ToolName: permission.BudgetToolName, Class: permission.ClassSpend, Description: "Today's budget is used up."},
	} {
		p := newGradedPermissions(t, req)
		require.False(t, p.sessionAllowed())

		// Tab skips "Allow for Session" both ways.
		p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
		require.Equal(t, 2, p.selectedOption)
		p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
		require.Equal(t, 0, p.selectedOption)
		p.HandleMsg(keyMsg('h'))
		require.Equal(t, 2, p.selectedOption)

		// The shortcut does nothing.
		require.Nil(t, p.HandleMsg(keyMsg('s')))

		buttons := ansi.Strip(p.renderButtons(80, false))
		require.NotContains(t, buttons, "Allow for Session")
		require.Contains(t, buttons, "Allow")
		require.Contains(t, buttons, "Deny")
	}
}

func TestPermissions_HeaderShowsKindAndWhy(t *testing.T) {
	t.Parallel()

	p := newGradedPermissions(t, permission.PermissionRequest{
		ToolName: "bash",
		Class:    permission.ClassNetwork,
		Reason:   "network always asks",
	})
	require.True(t, p.sessionAllowed())
	header := ansi.Strip(p.renderHeader(80))
	require.Contains(t, header, "Permission Required")
	require.Contains(t, header, "network")
	require.Contains(t, header, "network always asks")

	p = newGradedPermissions(t, permission.PermissionRequest{
		ToolName: permission.BudgetToolName,
		Class:    permission.ClassSpend,
		Path:     "/should/not/show",
	})
	header = ansi.Strip(p.renderHeader(80))
	require.Contains(t, header, "Budget Reached")
	require.Contains(t, header, "spend")
	require.False(t, strings.Contains(header, "/should/not/show"))
}

func TestPermissions_AllowAutoOption(t *testing.T) {
	t.Parallel()

	// Not offered: no button, the shortcut does nothing.
	p := newGradedPermissions(t, permission.PermissionRequest{ToolName: "bash"})
	require.NotContains(t, ansi.Strip(p.renderButtons(100, false)), "Continue in Auto")
	require.Nil(t, p.HandleMsg(keyMsg('c')))

	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	req := permission.PermissionRequest{ID: "perm-auto", ToolCallID: "tc", ToolName: "bash"}
	p = NewPermissions(com, req, WithAutoOption(true))
	require.Contains(t, ansi.Strip(p.renderButtons(100, false)), "Allow & Continue in Auto")

	// Tab order: Allow, Allow for Session, Allow & Continue in Auto, Deny.
	for _, want := range []int{optionAllowSession, optionAllowAuto, optionDeny, optionAllow} {
		p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
		require.Equal(t, want, p.selectedOption)
	}

	got := p.HandleMsg(keyMsg('c'))
	require.Equal(t, PermissionAllowAuto, got.(ActionPermissionResponse).Action)

	p.selectedOption = optionAllowAuto
	got = p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, PermissionAllowAuto, got.(ActionPermissionResponse).Action)

	// Still offered when "Allow for Session" is hidden.
	req.AlwaysAsk = true
	p = NewPermissions(com, req, WithAutoOption(true))
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, optionAllowAuto, p.selectedOption)
}
