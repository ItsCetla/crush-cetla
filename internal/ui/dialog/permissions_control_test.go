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
