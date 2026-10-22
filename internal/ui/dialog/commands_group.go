package dialog

import (
	"slices"

	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// CommandGroup names a section of the command palette. Every system
// command belongs to exactly one group; set it with
// [CommandItem.InGroup].
type CommandGroup string

// Command palette sections, shown in the order of commandGroupOrder.
const (
	CommandGroupSession CommandGroup = "Session"
	CommandGroupTabs    CommandGroup = "Tabs & Folders"
	CommandGroupModel   CommandGroup = "Model"
	CommandGroupControl CommandGroup = "Control"
	CommandGroupTools   CommandGroup = "Tools"
	CommandGroupView    CommandGroup = "View"
	CommandGroupApp     CommandGroup = "App"
)

// commandGroupOther collects commands added without a known group, so a
// forgotten InGroup never hides a command. Tests require every default
// command to have a real group.
const commandGroupOther CommandGroup = "Other"

// commandGroupRecent is the title of the recently used section.
const commandGroupRecent = "Recent"

// MaxRecentCommands is how many recently used commands the palette keeps.
const MaxRecentCommands = 5

// commandGroupOrder is the order sections appear in the palette.
var commandGroupOrder = []CommandGroup{
	CommandGroupSession,
	CommandGroupTabs,
	CommandGroupModel,
	CommandGroupControl,
	CommandGroupTools,
	CommandGroupView,
	CommandGroupApp,
}

// PushRecentCommand returns recent with id moved to the front, without
// duplicates or empty IDs and capped at [MaxRecentCommands]. The input is
// not modified.
func PushRecentCommand(recent []string, id string) []string {
	out := make([]string, 0, MaxRecentCommands)
	if id != "" {
		out = append(out, id)
	}
	for _, r := range recent {
		if len(out) >= MaxRecentCommands {
			break
		}
		if r == "" || slices.Contains(out, r) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// commandSectionHeader is a non-selectable section title in the command
// palette. Its Filter is empty, so it never matches a typed query.
type commandSectionHeader struct {
	*list.Versioned
	title string
	t     *styles.Styles
}

var _ list.FilterableItem = (*commandSectionHeader)(nil)

func newCommandSectionHeader(t *styles.Styles, title string) *commandSectionHeader {
	return &commandSectionHeader{Versioned: list.NewVersioned(), title: title, t: t}
}

// Filter implements [list.FilterableItem].
func (h *commandSectionHeader) Filter() string { return "" }

// Finished implements [list.Item].
func (h *commandSectionHeader) Finished() bool { return true }

// Render implements [list.Item]. The one-cell gutter on each side lines
// the title up with the command names below it.
func (h *commandSectionHeader) Render(width int) string {
	if width < 3 {
		return common.Section(h.t, h.title, width)
	}
	return " " + common.Section(h.t, h.title, width-2) + " "
}

// groupCommandItems arranges items into palette sections: a Recent
// section with the recent IDs that exist (most recent first), then each
// group in commandGroupOrder. Recent commands appear only in the Recent
// section. Empty sections are left out.
func groupCommandItems(t *styles.Styles, items []*CommandItem, recent []string) []list.FilterableItem {
	byID := make(map[string]*CommandItem, len(items))
	for _, it := range items {
		byID[it.id] = it
	}

	out := make([]list.FilterableItem, 0, len(items)+len(commandGroupOrder)+2)
	inRecent := make(map[string]bool, len(recent))
	var recentItems []list.FilterableItem
	for _, id := range recent {
		it, ok := byID[id]
		if !ok || inRecent[id] {
			continue
		}
		inRecent[id] = true
		recentItems = append(recentItems, it)
		if len(recentItems) >= MaxRecentCommands {
			break
		}
	}
	if len(recentItems) > 0 {
		out = append(out, newCommandSectionHeader(t, commandGroupRecent))
		out = append(out, recentItems...)
	}

	known := make(map[CommandGroup]bool, len(commandGroupOrder))
	for _, g := range commandGroupOrder {
		known[g] = true
	}
	sections := make(map[CommandGroup][]list.FilterableItem, len(commandGroupOrder)+1)
	for _, it := range items {
		if inRecent[it.id] {
			continue
		}
		group := it.group
		if !known[group] {
			group = commandGroupOther
		}
		sections[group] = append(sections[group], it)
	}
	for _, g := range append(slices.Clone(commandGroupOrder), commandGroupOther) {
		section := sections[g]
		if len(section) == 0 {
			continue
		}
		out = append(out, newCommandSectionHeader(t, string(g)))
		out = append(out, section...)
	}
	return out
}
