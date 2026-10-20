package fleet

import (
	"context"
	"fmt"
	"strings"
	"sync"

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
	machines := Machines(cfg)
	out := make([]Status, len(machines))
	var wg sync.WaitGroup
	for i, m := range machines {
		out[i] = Status{Machine: m, Busy: p.Busy(m)}
		if m.Disabled || m.URL == "" {
			continue
		}
		wg.Go(func() {
			if err := Probe(ctx, m); err != nil {
				out[i].Error = err.Error()
				return
			}
			out[i].Online = true
		})
	}
	wg.Wait()
	return out
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
// parameter"). It returns "" when no machine is usable.
func PromptSection(cfg *config.Config, toolHint string) string {
	machines := Machines(cfg)
	var lines []string
	usable := 0
	for _, m := range machines {
		if m.Usable() {
			usable++
		}
		lines = append(lines, "- "+Describe(m))
	}
	if usable == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<machines>\n")
	b.WriteString("The user has other computers running local models. Each can run as many agents at once as it has slots. ")
	b.WriteString("Hand them self-contained side work (searching and reading code, summarizing, reviewing a file, drafting tests or docs) ")
	fmt.Fprintf(&b, "through %s, and run independent pieces in parallel to keep them busy. ", toolHint)
	b.WriteString("Local models are weaker than you: give them small, specific tasks with the context they need, and check what they return. ")
	b.WriteString("Keep planning, risky edits and final decisions yourself. ")
	b.WriteString("When the user tells you about a machine (added, removed, changed), save it with the machines tool.\n\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n</machines>")
	return b.String()
}
