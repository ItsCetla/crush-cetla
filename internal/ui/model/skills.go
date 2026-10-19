package model

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

type skillStatusItem struct {
	icon  string
	name  string
	title string
	// description is reserved for future use (e.g. showing error details).
	description string
}

var builtinSkillsCache struct {
	once   sync.Once
	skills []*skills.Skill
}

func cachedBuiltinSkills() []*skills.Skill {
	builtinSkillsCache.once.Do(func() {
		builtinSkillsCache.skills = skills.DiscoverBuiltin()
	})
	return builtinSkillsCache.skills
}

// skillsInfo renders the skill discovery status section showing loaded and
// invalid skills.
func (m *UI) skillsInfo(width, maxItems int, isSection bool) string {
	t := m.com.Styles

	title := t.Resource.Heading.Render("Skills")
	if isSection {
		title = common.Section(t, title, width)
	}

	items := m.skillStatusItems()
	if len(items) == 0 {
		list := t.Resource.AdditionalText.Render("None")
		return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
	}

	list := skillsList(t, items, width, maxItems)
	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
}

func (m *UI) skillStatusItems() []skillStatusItem {
	t := m.com.Styles
	var items []skillStatusItem
	stateNames := make(map[string]struct{}, len(m.skillStates))

	disabledSet := make(map[string]bool)
	if m.com != nil && m.com.Workspace != nil {
		if cfg := m.com.Config(); cfg != nil {
			for _, name := range cfg.Options.DisabledSkills {
				disabledSet[name] = true
			}
		}
	}

	states := slices.Clone(m.skillStates)
	slices.SortStableFunc(states, func(a, b *skills.SkillState) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, state := range states {
		name := state.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(state.Path))
		}
		if disabledSet[name] {
			continue
		}
		if _, exists := stateNames[name]; exists {
			continue
		}
		stateNames[name] = struct{}{}
		icon := t.Resource.OnlineIcon.String()
		if state.State == skills.StateError {
			icon = t.Resource.ErrorIcon.String()
		}
		items = append(items, skillStatusItem{
			icon:  icon,
			name:  name,
			title: t.Resource.Name.Render(name),
		})
	}

	builtin := cachedBuiltinSkills()
	slices.SortStableFunc(builtin, func(a, b *skills.Skill) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, skill := range builtin {
		if _, ok := stateNames[skill.Name]; ok {
			continue
		}
		if disabledSet[skill.Name] {
			continue
		}
		items = append(items, skillStatusItem{
			icon:  t.Resource.OnlineIcon.String(),
			name:  skill.Name,
			title: t.Resource.Name.Render(skill.Name),
		})
	}

	slices.SortStableFunc(items, func(a, b skillStatusItem) int {
		return strings.Compare(a.name, b.name)
	})

	return items
}

func skillsList(t *styles.Styles, items []skillStatusItem, width, maxItems int) string {
	if maxItems <= 0 {
		return ""
	}

	if len(items) > maxItems {
		visibleItems := items[:maxItems-1]
		remaining := len(items) - (maxItems - 1)
		items = append(visibleItems, skillStatusItem{
			name:  "more",
			title: t.Resource.AdditionalText.Render(fmt.Sprintf("…and %d more", remaining)),
		})
	}

	renderedItems := make([]string, 0, len(items))
	for _, item := range items {
		renderedItems = append(renderedItems, common.Status(t, common.StatusOpts{
			Icon:        item.icon,
			Title:       item.title,
			Description: item.description,
		}, width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, renderedItems...)
}

// skillsGrid renders the landing page's skills section: a heading with the
// skill count, then the skills flowed top-to-bottom into as many columns as
// fit in width. At most height lines are used; overflow ends in "…and N more".
func (m *UI) skillsGrid(width, height int) string {
	t := m.com.Styles
	items := m.skillStatusItems()

	title := t.Resource.Heading.Render("Skills")
	if len(items) == 0 {
		return lipgloss.NewStyle().Width(width).Render(title + "\n\n" + t.Resource.AdditionalText.Render("None"))
	}
	title += " " + t.Resource.AdditionalText.Render(fmt.Sprint(len(items)))

	rows := height - 2 // heading + blank line
	if rows <= 0 {
		return lipgloss.NewStyle().Width(width).Render(title)
	}
	return lipgloss.NewStyle().Width(width).Render(title + "\n\n" + skillsGridBody(t, items, width, rows))
}

// skillsGridBody lays items out column-major in a grid width cells wide and
// rows lines tall.
func skillsGridBody(t *styles.Styles, items []skillStatusItem, width, rows int) string {
	const (
		minColWidth = 16
		maxColWidth = 30
		gap         = 2
	)
	longest := 0
	for _, it := range items {
		longest = max(longest, lipgloss.Width(it.name))
	}
	// icon + space + name + gap
	colWidth := min(max(longest+2+gap, minColWidth), maxColWidth, max(width, 1))
	cols := max(1, width/colWidth)

	if capacity := rows * cols; len(items) > capacity {
		shown := capacity - 1
		items = append(items[:shown:shown], skillStatusItem{
			name:  "more",
			title: t.Resource.AdditionalText.Render(fmt.Sprintf("…and %d more", len(items)-shown)),
		})
	}
	usedRows := min(rows, (len(items)+cols-1)/cols)

	columns := make([]string, 0, cols)
	for c := 0; c*usedRows < len(items); c++ {
		end := min((c+1)*usedRows, len(items))
		cells := make([]string, 0, usedRows)
		for _, it := range items[c*usedRows : end] {
			cell := common.Status(t, common.StatusOpts{Icon: it.icon, Title: it.title}, colWidth)
			cell = ansi.Truncate(cell, colWidth-gap, "…")
			cells = append(cells, lipgloss.NewStyle().Width(colWidth).Render(cell))
		}
		columns = append(columns, lipgloss.JoinVertical(lipgloss.Left, cells...))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, columns...)
}
