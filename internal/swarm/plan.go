// Package swarm splits a mission across the user's machines. Each
// machine gets some of the mission's unchecked criteria, a worktree and
// a branch of its own, and an agent that works there. When the agents
// finish, the mission's approved checks run in each worktree, and the
// merge board lets the user merge or discard each branch.
package swarm

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/worktree"
)

// BranchPrefix starts every swarm branch.
const BranchPrefix = "swarm/"

// Plan works out which ready machine gets which of the mission's
// unchecked criteria. Disabled, offline and full machines are skipped,
// with the reason. Criteria are split in order into runs of nearly equal
// size, one run per machine, so every machine in the plan gets at least
// one criterion. It doesn't contact or lease anything.
func Plan(m control.Mission, statuses []fleet.Status) control.SwarmPlan {
	p := control.SwarmPlan{MissionID: m.ID, MissionTitle: m.Title}
	var open []control.Criterion
	for _, c := range m.Criteria {
		if !c.Done {
			open = append(open, c)
		}
	}
	p.Open = len(open)

	statuses = slices.Clone(statuses)
	slices.SortFunc(statuses, func(a, b fleet.Status) int { return cmp.Compare(a.Name, b.Name) })
	var ready []fleet.Status
	for _, s := range statuses {
		if reason := notReady(s); reason != "" {
			p.Skipped = append(p.Skipped, control.SwarmSkip{Machine: s.Name, Reason: reason})
			continue
		}
		ready = append(ready, s)
	}

	n := min(len(ready), len(open))
	for i, s := range ready {
		if i >= n {
			p.Skipped = append(p.Skipped, control.SwarmSkip{Machine: s.Name, Reason: "nothing left to assign"})
			continue
		}
		lo, hi := i*len(open)/n, (i+1)*len(open)/n
		a := control.SwarmAssignment{
			Machine: s.Name,
			Model:   s.Model,
			Branch:  BranchName(m, s.Name),
			Status:  control.SwarmPlanned,
		}
		for _, c := range open[lo:hi] {
			a.Criteria = append(a.Criteria, c.ID)
			a.CriteriaText = append(a.CriteriaText, c.Text)
		}
		p.Assignments = append(p.Assignments, a)
	}
	return p
}

// notReady says why a machine can't take part, or "" when it can.
func notReady(s fleet.Status) string {
	switch {
	case s.Disabled:
		return "disabled"
	case s.URL == "":
		return "has no URL"
	case !s.Online:
		if s.Error != "" {
			return "offline: " + s.Error
		}
		return "offline"
	case !s.Usable():
		return "has no model"
	case s.Free() == 0:
		return "no free slot"
	}
	return ""
}

// BranchName is the branch a machine's share of the mission is built on:
// swarm/<mission-slug>/<machine>.
func BranchName(m control.Mission, machine string) string {
	slug := Slug(m.Title)
	if slug == "" {
		slug = m.ID
	}
	return worktree.Sanitize(BranchPrefix + slug + "/" + strings.ToLower(machine))
}

// Slug turns a mission title into a short branch-safe name.
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if b.Len() >= 40 {
			break
		}
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		switch {
		case ok:
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// describeCriteria lists criterion ids as "1, 2 and 3".
func describeCriteria(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	switch len(parts) {
	case 0:
		return "none"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
