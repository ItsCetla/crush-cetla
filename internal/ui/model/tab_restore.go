package model

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	tea "charm.land/bubbletea/v2"
)

// Restored tabs: the open tabs, their names, groups and folds are saved
// to <data directory>/tabs.json whenever they change, and opened again
// on the next start in the same project (options.tui.restore_tabs, on by
// default). Sessions deleted in between are skipped.

// tabsFileName is the saved tab list inside the data directory.
const tabsFileName = "tabs.json"

// savedTabs is the tabs.json format.
type savedTabs struct {
	Version   int        `json:"version"`
	Tabs      []savedTab `json:"tabs"`
	Collapsed []string   `json:"collapsed,omitempty"`
}

type savedTab struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Group string `json:"group,omitempty"`
	Aside bool   `json:"aside,omitempty"`
}

// tabsRestoredMsg carries the saved tabs whose sessions still exist.
type tabsRestoredMsg struct {
	tabs      []sessionTab
	collapsed []string
}

// tabsFilePath is where the tab list is saved, or "" when restoring is
// off or there is no data directory.
func (m *UI) tabsFilePath() string {
	if m.com == nil || m.com.Workspace == nil {
		return ""
	}
	cfg := m.com.Config()
	if cfg == nil || cfg.Options == nil || cfg.Options.DataDirectory == "" {
		return ""
	}
	if cfg.Options.TUI != nil && !cfg.Options.TUI.RestoreTabsEnabled() {
		return ""
	}
	return filepath.Join(cfg.Options.DataDirectory, tabsFileName)
}

// restoreTabs reads the saved tabs in the background.
func (m *UI) restoreTabs() tea.Cmd {
	path := m.tabsFilePath()
	ws := m.com.Workspace
	if path == "" || ws == nil {
		return nil
	}
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		if err != nil {
			return tabsRestoredMsg{}
		}
		var saved savedTabs
		if err := json.Unmarshal(data, &saved); err != nil {
			slog.Warn("Ignoring unreadable saved tabs", "path", path, "error", err)
			return tabsRestoredMsg{}
		}
		ctx := context.Background()
		var tabs []sessionTab
		for _, t := range saved.Tabs {
			if t.ID == "" {
				continue
			}
			s, err := ws.GetSession(ctx, t.ID)
			if err != nil || s.ParentSessionID != "" {
				continue
			}
			tabs = append(tabs, sessionTab{id: s.ID, title: tabTitle(s), group: cleanTabName(t.Group), aside: t.Aside, dir: s.WorkingDir})
		}
		var collapsed []string
		for _, g := range saved.Collapsed {
			if g = cleanTabName(g); g != "" {
				collapsed = append(collapsed, g)
			}
		}
		return tabsRestoredMsg{tabs: tabs, collapsed: collapsed}
	}
}

// handleTabsRestored puts the saved tabs first, ahead of any opened since
// start, and allows saving from now on.
func (m *UI) handleTabsRestored(msg tabsRestoredMsg) {
	m.tabx.restored = true
	if len(msg.tabs) == 0 {
		return
	}
	merged := make([]sessionTab, 0, len(msg.tabs)+len(m.tabs))
	for _, t := range msg.tabs {
		if i := m.tabIndex(t.id); i >= 0 {
			// Already open: keep the live title.
			t.title = m.tabs[i].title
		}
		merged = append(merged, t)
	}
	for _, t := range m.tabs {
		if !containsTab(msg.tabs, t.id) {
			merged = append(merged, t)
		}
	}
	normalizeTabGroups(merged)
	m.tabs = merged
	for _, g := range msg.collapsed {
		if g != "" && m.tabGroupMembers(g) != nil {
			if m.tabx.collapsed == nil {
				m.tabx.collapsed = make(map[string]bool)
			}
			m.tabx.collapsed[g] = true
		}
	}
}

func containsTab(tabs []sessionTab, id string) bool {
	for _, t := range tabs {
		if t.id == id {
			return true
		}
	}
	return false
}

// encodeTabs serializes the tab list for tabs.json.
func (m *UI) encodeTabs() string {
	saved := savedTabs{Version: 1, Tabs: make([]savedTab, 0, len(m.tabs))}
	for _, t := range m.tabs {
		saved.Tabs = append(saved.Tabs, savedTab{ID: t.id, Title: t.title, Group: t.group, Aside: t.aside})
	}
	for g := range m.tabx.collapsed {
		if m.tabGroupMembers(g) != nil {
			saved.Collapsed = append(saved.Collapsed, g)
		}
	}
	sort.Strings(saved.Collapsed)
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return ""
	}
	return string(data) + "\n"
}

// saveTabsIfChanged writes tabs.json in the background when the tab list
// changed since the last write. Update calls it after every message.
func (m *UI) saveTabsIfChanged() tea.Cmd {
	if !m.tabx.restored {
		return nil
	}
	path := m.tabsFilePath()
	if path == "" {
		return nil
	}
	data := m.encodeTabs()
	if data == "" || data == m.tabx.saved {
		return nil
	}
	m.tabx.saved = data
	return func() tea.Msg {
		if err := writeFileAtomic(path, []byte(data)); err != nil {
			slog.Warn("Could not save open tabs", "path", path, "error", err)
		}
		return nil
	}
}

// writeFileAtomic writes data to path through a temporary file, so a
// crash never leaves half a file.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
