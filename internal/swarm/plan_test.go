package swarm

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/stretchr/testify/require"
)

// readyMachine is an online machine with a model and one free slot.
func readyMachine(name string) fleet.Status {
	return fleet.Status{
		Machine: fleet.Machine{
			Name:          name,
			MachineConfig: config.MachineConfig{URL: "http://" + name + ".local:11434/v1"},
			Model:         "qwen-" + name,
		},
		Online: true,
	}
}

func testMission(open, done int) control.Mission {
	m := control.Mission{ID: "m1", Title: "Ship the Login Page!"}
	for i := range open + done {
		m.Criteria = append(m.Criteria, control.Criterion{
			ID:   i + 1,
			Text: "criterion " + string(rune('a'+i)),
			Done: i < done,
		})
	}
	return m
}

func TestPlanSplitsOpenCriteria(t *testing.T) {
	t.Parallel()

	disabled := readyMachine("delta")
	disabled.Disabled = true
	offline := readyMachine("echo")
	offline.Online = false
	offline.Error = "connection refused"
	noModel := readyMachine("foxtrot")
	noModel.Model = ""
	full := readyMachine("golf")
	full.Busy = 1
	noURL := readyMachine("hotel")
	noURL.URL = ""

	m := testMission(5, 1)
	p := Plan(m, []fleet.Status{
		readyMachine("bravo"), full, disabled, readyMachine("alpha"), offline, noModel, noURL,
	})

	require.Equal(t, "m1", p.MissionID)
	require.Equal(t, "Ship the Login Page!", p.MissionTitle)
	require.Equal(t, 5, p.Open)
	require.Len(t, p.Assignments, 2)

	// Machines are taken in name order and the criteria split in order.
	a, b := p.Assignments[0], p.Assignments[1]
	require.Equal(t, "alpha", a.Machine)
	require.Equal(t, "qwen-alpha", a.Model)
	require.Equal(t, []int{2, 3}, a.Criteria)
	require.Equal(t, []string{"criterion b", "criterion c"}, a.CriteriaText)
	require.Equal(t, "swarm/ship-the-login-page/alpha", a.Branch)
	require.Equal(t, control.SwarmPlanned, a.Status)
	require.Equal(t, "bravo", b.Machine)
	require.Equal(t, []int{4, 5, 6}, b.Criteria)

	reasons := map[string]string{}
	for _, s := range p.Skipped {
		reasons[s.Machine] = s.Reason
	}
	require.Equal(t, map[string]string{
		"delta":   "disabled",
		"echo":    "offline: connection refused",
		"foxtrot": "has no model",
		"golf":    "no free slot",
		"hotel":   "has no URL",
	}, reasons)
	require.Equal(t, map[string][]int{"alpha": {2, 3}, "bravo": {4, 5, 6}}, p.Assign())
}

func TestPlanMoreMachinesThanCriteria(t *testing.T) {
	t.Parallel()

	p := Plan(testMission(1, 0), []fleet.Status{readyMachine("b"), readyMachine("a")})
	require.Len(t, p.Assignments, 1)
	require.Equal(t, "a", p.Assignments[0].Machine)
	require.Equal(t, []int{1}, p.Assignments[0].Criteria)
	require.Equal(t, []control.SwarmSkip{{Machine: "b", Reason: "nothing left to assign"}}, p.Skipped)
}

func TestPlanNothingOpen(t *testing.T) {
	t.Parallel()

	p := Plan(testMission(0, 2), []fleet.Status{readyMachine("a")})
	require.Zero(t, p.Open)
	require.Empty(t, p.Assignments)
	require.Equal(t, []control.SwarmSkip{{Machine: "a", Reason: "nothing left to assign"}}, p.Skipped)
}

func TestSlugAndBranchName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "ship-the-login-page", Slug("  Ship the Login Page!  "))
	require.Equal(t, "", Slug("!!!"))
	require.LessOrEqual(t, len(Slug("a very long mission title that goes on and on and on forever")), 40)

	// A title with nothing branch-safe falls back to the mission id.
	m := control.Mission{ID: "abc123", Title: "???"}
	require.Equal(t, "swarm/abc123/studio", BranchName(m, "Studio"))
}

func TestDescribeCriteria(t *testing.T) {
	t.Parallel()

	require.Equal(t, "none", describeCriteria(nil))
	require.Equal(t, "4", describeCriteria([]int{4}))
	require.Equal(t, "1, 2 and 3", describeCriteria([]int{1, 2, 3}))
}

func TestPromptListsShareAndOthers(t *testing.T) {
	t.Parallel()

	m := testMission(3, 0)
	m.Goal = "Users can sign in."
	m.Criteria[0].Check = "go test ./auth"
	a := control.SwarmAssignment{Criteria: []int{1}, Branch: "swarm/x/alpha", Worktree: "/tmp/wt"}
	got := Prompt(m, a)
	require.Contains(t, got, "Users can sign in.")
	require.Contains(t, got, "1. criterion a\n   It is checked by running: go test ./auth")
	require.Contains(t, got, "leave them alone:\n2. criterion b\n3. criterion c")
	require.Contains(t, got, "/tmp/wt")
	require.Contains(t, got, "swarm/x/alpha")
}
