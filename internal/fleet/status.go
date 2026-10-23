package fleet

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
)

// Status is a machine's live state.
type Status struct {
	Machine
	Busy   int
	Online bool
	Error  string
}

// Free returns how many slots are open.
func (s Status) Free() int {
	return max(s.SlotCount()-s.Busy, 0)
}

// Check probes every machine at once and reports its slots.
func (p *Pool) Check(ctx context.Context, cfg *config.Config) []Status {
	Refresh(ctx, cfg)
	machines := Machines(cfg)
	out := make([]Status, len(machines))
	for i, m := range machines {
		out[i] = Status{Machine: m, Busy: p.Busy(m)}
		if m.Disabled || m.URL == "" {
			continue
		}
		// Refresh just checked it, so this is usually the cached answer.
		live, err := Inspect(ctx, m)
		out[i].Machine = withLive(out[i].Machine, live)
		if err != nil {
			out[i].Error = err.Error()
			continue
		}
		out[i].Online = true
	}
	return out
}

// Ready sums up the machines that answer and have a model: how many
// there are and how many agents they run at once.
func Ready(statuses []Status) (machines, agents int) {
	for _, s := range statuses {
		if s.Online && s.Usable() {
			machines++
			agents += s.SlotCount()
		}
	}
	return machines, agents
}

// CachedReady is [Ready] from the last check, without contacting any
// machine. checked is false when no machine has been checked yet.
func CachedReady(cfg *config.Config) (machines, agents int, checked bool) {
	for _, m := range Machines(cfg) {
		if m.Disabled {
			continue
		}
		probeMu.Lock()
		r, ok := probeCache[m.URL]
		probeMu.Unlock()
		if !ok {
			continue
		}
		checked = true
		if r.err == nil && m.Usable() {
			machines++
			agents += m.SlotCount()
		}
	}
	return machines, agents, checked
}

// Describe renders a one-line summary of a machine for people and the AI.
func Describe(m Machine) string {
	var b strings.Builder
	b.WriteString(m.Name)
	if m.Hardware != "" {
		fmt.Fprintf(&b, " (%s)", m.Hardware)
	}
	fmt.Fprintf(&b, ": %d slot", m.SlotCount())
	if m.SlotCount() != 1 {
		b.WriteString("s")
	}
	switch {
	case m.Disabled:
		b.WriteString(", disabled")
	case m.Model != "":
		fmt.Fprintf(&b, ", model %s", m.Model)
	default:
		b.WriteString(", no model yet (offline at startup)")
	}
	if len(m.Tags) > 0 {
		fmt.Fprintf(&b, ", good for: %s", strings.Join(m.Tags, ", "))
	}
	if m.Role != "" {
		fmt.Fprintf(&b, ", role: %s", m.Role)
	}
	if !m.Disabled && m.UseMode() == config.MachineUseNamed {
		b.WriteString(", only when asked for by name")
	}
	if m.Notes != "" {
		fmt.Fprintf(&b, ". %s", strings.TrimSuffix(m.Notes, "."))
	}
	return b.String()
}

// DescribeStatus is Describe plus live slot and reachability info.
func DescribeStatus(s Status) string {
	line := Describe(s.Machine)
	switch {
	case s.Disabled:
	case s.Online:
		line += fmt.Sprintf(" [online, %d of %d free]", s.Free(), s.SlotCount())
	default:
		line += " [offline"
		if s.Error != "" {
			line += ": " + s.Error
		}
		line += "]"
	}
	return line
}

// PromptSection tells the main agent which machines it can hand work to.
// toolHint names how to reach them (e.g. "the agent tool's machine
// parameter"). It returns "" when no machine is usable. It uses what the
// machines last reported; call Refresh first for live slot counts.
func PromptSection(cfg *config.Config, toolHint string) string {
	machines := Machines(cfg)
	var lines []string
	usable, agents, named := 0, 0, 0
	for _, m := range machines {
		line := "- " + Describe(m)
		switch {
		case knownOffline(m):
			line += " (offline right now)"
		case m.Usable():
			usable++
			agents += m.SlotCount()
			if m.UseMode() == config.MachineUseNamed {
				named++
			}
		}
		lines = append(lines, line)
	}
	if usable == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<machines>\n")
	fmt.Fprintf(&b, "The user has %d other %s running local models, ready to use: %d %s at once in total. ",
		usable, plural(usable, "computer", "computers"), agents, plural(agents, "agent", "agents"))
	b.WriteString("The user wants them used. Whenever a task has self-contained side work (searching and reading code, summarizing, reviewing a file, drafting tests or docs), ")
	fmt.Fprintf(&b, "hand it to the machines through %s instead of doing it all yourself, and send independent pieces in the same turn so they run in parallel and keep every slot busy. ", toolHint)
	b.WriteString("When every slot is busy or a machine is offline the work comes back to you, so never wait for a machine. ")
	b.WriteString("Local models are weaker than you: give them small, specific tasks with the context they need, and check what they return. ")
	b.WriteString("Keep planning, risky edits and final decisions yourself. ")
	b.WriteString("A machine with a role is the first pick for that kind of work. ")
	if named > 0 {
		b.WriteString("A machine marked \"only when asked for by name\" is left out of automatic picks: send it work only when the user asks for that machine, by naming it. ")
	}
	b.WriteString("When the user tells you about a machine (added, removed, changed), save it with the machines tool.\n\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n</machines>")
	return b.String()
}

// knownOffline reports whether m's last check failed. A machine that
// hasn't been checked yet counts as online.
func knownOffline(m Machine) bool {
	probeMu.Lock()
	defer probeMu.Unlock()
	r, ok := probeCache[m.URL]
	return ok && r.err != nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
