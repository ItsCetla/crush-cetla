package dialog

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/discover"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// localServersMsg carries the model servers found on this computer.
type localServersMsg struct {
	servers []discover.LocalServer
}

// localProviderSavedMsg reports that a discovered server was saved as a
// provider, so the chosen model can now be selected the normal way.
type localProviderSavedMsg struct {
	providerID string
	modelID    string
	modelType  ModelType
	err        error
}

// localPick is a model on a discovered server that isn't configured yet.
type localPick struct {
	server     discover.LocalServer
	providerID string
	modelID    string
}

// DiscoverLocalCmd looks for model servers running on this computer, such
// as Ollama or LM Studio. The probes are short and run off the UI thread;
// whatever answers shows up at the top of the list.
func (m *Models) DiscoverLocalCmd() tea.Cmd {
	return func() tea.Msg {
		return localServersMsg{servers: discover.FindLocal(context.Background())}
	}
}

// handleLocalMsg handles the discovery and save messages. The bool is
// false for messages it doesn't own.
func (m *Models) handleLocalMsg(msg tea.Msg) (Action, bool) {
	switch msg := msg.(type) {
	case localServersMsg:
		m.localServers = msg.servers
		if err := m.setProviderItems(); err != nil {
			return ActionCmd{util.ReportError(err)}, true
		}
		if q := m.input.Value(); q != "" {
			m.list.SetFilter(q)
			m.list.SelectFirst()
		} else if first := m.firstLocalItemID(); first != "" && m.isOnboarding {
			m.list.SetSelectedItem(first)
			m.list.ScrollToTop()
		}
		return nil, true
	case localProviderSavedMsg:
		m.savingLocal = false
		if msg.err != nil {
			err := fmt.Errorf("saving %s: %w", msg.providerID, msg.err)
			return ActionCmd{util.ReportError(err)}, true
		}
		p, ok := m.com.Config().Providers.Get(msg.providerID)
		if !ok || !slices.ContainsFunc(p.Models, func(mod catwalk.Model) bool { return mod.ID == msg.modelID }) {
			warn := fmt.Sprintf("Saved %s, but couldn't load %s from it. Is the server still running?", msg.providerID, msg.modelID)
			return ActionCmd{util.ReportWarn(warn)}, true
		}
		model := config.SelectedModel{Provider: msg.providerID, Model: msg.modelID}
		return ActionSelectModel{
			Provider:  p.ToProvider(),
			Model:     model,
			ModelType: msg.modelType.Config(),
		}, true
	}
	return nil, false
}

// selectLocal saves the server behind a discovered model as a provider.
// Saving reloads the config, which lists the server's models, so it runs
// as a command.
func (m *Models) selectLocal(item *ModelItem) (Action, bool) {
	pick, ok := m.localPicks[item.ID()]
	if !ok {
		return nil, false
	}
	if m.savingLocal {
		return nil, true
	}
	m.savingLocal = true
	ws := m.com.Workspace
	typ := m.modelType
	return ActionCmd{func() tea.Msg {
		prefix := "providers." + pick.providerID + "."
		err := ws.SetConfigFields(config.ScopeGlobal, map[string]any{
			prefix + "name":     pick.server.Name,
			prefix + "type":     string(pick.server.ProviderType),
			prefix + "base_url": pick.server.BaseURL,
			prefix + "disable":  false,
		})
		return localProviderSavedMsg{
			providerID: pick.providerID,
			modelID:    pick.modelID,
			modelType:  typ,
			err:        err,
		}
	}}, true
}

// localGroups builds a list group for each discovered server that has
// models and isn't already configured.
func (m *Models) localGroups() []ModelGroup {
	m.localPicks = map[string]localPick{}
	if len(m.localServers) == 0 {
		return nil
	}
	t := m.com.Styles
	cfg := m.com.Config()

	configured := map[string]bool{}
	taken := map[string]bool{}
	for id, p := range cfg.Providers.Seq2() {
		taken[id] = true
		if hp := hostPort(p.BaseURL); hp != "" {
			configured[hp] = true
		}
	}

	var groups []ModelGroup
	for _, s := range m.localServers {
		if len(s.Models) == 0 || configured[hostPort(s.Root)] {
			continue
		}
		id := uniqueProviderID(s.ProviderID, taken)
		taken[id] = true

		prov := catwalk.Provider{
			ID:          catwalk.InferenceProvider(id),
			Name:        s.Name,
			Type:        s.ProviderType,
			APIEndpoint: s.BaseURL,
		}
		group := NewModelGroup(t, localGroupTitle(s), false)
		for _, lm := range s.Models {
			name := lm.ID
			if lm.ParameterSize != "" {
				name += " (" + lm.ParameterSize + ")"
			}
			item := NewModelItem(t, prov, catwalk.Model{ID: lm.ID, Name: name}, m.modelType, false)
			group.AppendItems(item)
			m.localPicks[item.ID()] = localPick{server: s, providerID: id, modelID: lm.ID}
		}
		groups = append(groups, group)
	}
	return groups
}

func (m *Models) firstLocalItemID() string {
	for _, it := range m.list.VisibleItems() {
		if mi, ok := it.(*ModelItem); ok {
			if _, local := m.localPicks[mi.ID()]; local {
				return mi.ID()
			}
		}
	}
	return ""
}

func localGroupTitle(s discover.LocalServer) string {
	name := s.Name
	if s.Version != "" {
		name += " " + s.Version
	}
	return name + " · running on this computer"
}

func uniqueProviderID(base string, taken map[string]bool) string {
	if base == "" {
		base = "local"
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

// hostPort returns a comparable host:port for a base URL, treating the
// loopback spellings as one host. It returns "" for URLs it can't read.
func hostPort(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	if host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.") {
		host = "localhost"
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(host, port)
}
