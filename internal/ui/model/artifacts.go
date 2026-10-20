package model

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/artifacts"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// openArtifactsCmd starts the artifact viewer if none is running and opens
// its gallery in the browser.
func (m *UI) openArtifactsCmd() tea.Cmd {
	var opts artifacts.ServerOptions
	if cfg := m.com.Config(); cfg != nil {
		a := cfg.Options.ArtifactsOrDefault()
		opts = artifacts.ServerOptions{Port: a.Port, AllowCDN: a.AllowCDN}
	}
	return func() tea.Msg {
		svc, err := artifacts.Default()
		if err != nil {
			return util.ReportError(err)()
		}
		svc.Server = opts
		u, err := svc.BaseURL()
		if err != nil {
			return util.ReportError(err)()
		}
		if err := artifacts.OpenBrowser(u); err != nil {
			return util.NewInfoMsg("Artifacts gallery: " + u)
		}
		return util.NewInfoMsg("Opened the artifacts gallery at " + u)
	}
}
