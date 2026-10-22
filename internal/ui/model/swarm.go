package model

// Swarm entry points: planning a swarm, confirming it, and the merge board
// that reviews each machine's branch. Every swarm command goes through the
// control hub off-thread like the cockpit's (see control.go).

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// swarmRefreshInterval is how often an open merge board reloads while a
// swarm is still working.
const swarmRefreshInterval = 3 * time.Second

// swarmRefreshMsg reloads the merge board.
type swarmRefreshMsg struct{}

// isSwarmOp reports whether op is a swarm command.
func isSwarmOp(op control.Op) bool {
	return strings.HasPrefix(string(op), "swarm")
}

// startSwarm plans a swarm for missionID, or for the attached mission when
// it's empty. The plan comes back as a reply that opens the confirmation.
func (m *UI) startSwarm(missionID string) tea.Cmd {
	if m.ctl.unavailable {
		return util.ReportWarn("Control isn't available in this workspace.")
	}
	if missionID == "" {
		if s := m.controlSnapshot(); s != nil && s.Mission != nil {
			missionID = s.Mission.ID
		}
	}
	if missionID == "" {
		return util.ReportWarn("Attach a mission (cockpit s) or pick one on the cockpit's Missions page and press w.")
	}
	return m.controlCmd(control.Command{Op: control.OpSwarmPlan, MissionID: missionID})
}

// launchSwarm starts the plan the user confirmed.
func (m *UI) launchSwarm(msg dialog.ActionSwarmLaunch) tea.Cmd {
	m.dialog.CloseDialog(dialog.SwarmConfirmID)
	return tea.Batch(
		util.ReportInfo("Starting the swarm…"),
		m.controlCmd(control.Command{Op: control.OpSwarmStart, MissionID: msg.MissionID, Assign: msg.Assign}),
	)
}

// openMergeBoard opens the merge board, focused on the run focus when it
// is set, and loads it.
func (m *UI) openMergeBoard(focus string) tea.Cmd {
	if m.ctl.unavailable {
		return util.ReportWarn("Control isn't available in this workspace.")
	}
	if m.dialog.ContainsDialog(dialog.MergeBoardID) {
		m.dialog.CloseDialog(dialog.MergeBoardID)
	}
	d, load := dialog.NewMergeBoard(m.com, focus)
	m.dialog.OpenDialog(d)
	return m.runControlCmds(load.Cmds)
}

// mergeBoard returns the open merge board, or nil.
func (m *UI) mergeBoard() *dialog.MergeBoard {
	d, _ := m.dialog.Dialog(dialog.MergeBoardID).(*dialog.MergeBoard)
	return d
}

// handleSwarmReply applies the reply to a swarm command.
func (m *UI) handleSwarmReply(msg controlReplyMsg) tea.Cmd {
	board := m.mergeBoard()
	switch msg.op {
	case control.OpSwarmPlan:
		if msg.err != nil {
			return util.ReportError(msg.err)
		}
		if msg.reply.Plan == nil {
			return util.ReportWarn("The swarm plan came back empty.")
		}
		if m.dialog.ContainsDialog(dialog.SwarmConfirmID) {
			m.dialog.CloseDialog(dialog.SwarmConfirmID)
		}
		m.dialog.OpenDialog(dialog.NewSwarmConfirm(m.com, *msg.reply.Plan))
		return nil
	case control.OpSwarmStart:
		if msg.err != nil {
			return util.ReportError(msg.err)
		}
		m.invalidateControl()
		focus := ""
		if msg.reply.Swarm != nil {
			focus = msg.reply.Swarm.ID
		}
		cmds := []tea.Cmd{m.openMergeBoard(focus)}
		if b := m.mergeBoard(); b != nil {
			b.SetReply(msg.op, msg.reply, nil)
		}
		if msg.reply.Message != "" {
			cmds = append(cmds, util.ReportInfo(msg.reply.Message))
		}
		return tea.Batch(cmds...)
	case control.OpSwarmRuns:
		if board == nil {
			return nil
		}
		board.SetReply(msg.op, msg.reply, msg.err)
		if msg.err == nil && board.Running() && !m.swarmTicking {
			m.swarmTicking = true
			return tea.Tick(swarmRefreshInterval, func(time.Time) tea.Msg { return swarmRefreshMsg{} })
		}
		return nil
	case control.OpSwarmDiff:
		if board != nil {
			board.SetReply(msg.op, msg.reply, msg.err)
		}
		return nil
	case control.OpSwarmMerge, control.OpSwarmDiscard:
		// Merging ticks mission criteria.
		m.invalidateControl()
		if board != nil {
			board.SetReply(msg.op, msg.reply, msg.err)
			return nil
		}
		if msg.err != nil {
			return util.ReportError(msg.err)
		}
		if msg.reply.Message != "" {
			return util.ReportInfo(msg.reply.Message)
		}
	}
	return nil
}

// refreshMergeBoard reloads an open merge board.
func (m *UI) refreshMergeBoard() tea.Cmd {
	m.swarmTicking = false
	if m.mergeBoard() == nil {
		return nil
	}
	return m.controlCmd(control.Command{Op: control.OpSwarmRuns})
}
