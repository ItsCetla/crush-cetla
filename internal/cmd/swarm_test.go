package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/stretchr/testify/require"
)

func TestSwarmOutputEscapesControlCharacters(t *testing.T) {
	t.Parallel()

	evil := "box\x1b]52;c;aGk=\x07\u202e"
	a := control.SwarmAssignment{Machine: evil, Model: evil, Branch: evil}

	var plan bytes.Buffer
	writeSwarmPlan(&plan, control.SwarmPlan{
		MissionTitle: evil,
		Open:         1,
		Assignments:  []control.SwarmAssignment{a},
	})
	var run bytes.Buffer
	writeSwarmRun(&run, control.SwarmRun{
		ID:          "r1",
		Target:      evil,
		Assignments: []control.SwarmAssignment{a},
	}, time.Now())

	for _, out := range []string{plan.String(), run.String()} {
		require.NotContains(t, out, "\x1b")
		require.NotContains(t, out, "\x07")
		require.NotContains(t, out, "\u202e")
		require.Contains(t, out, `\x1b`)
	}
}
