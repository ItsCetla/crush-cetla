package config

import (
	"context"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/opencode"
)

// OpenCodeMCPStatus describes one OpenCode MCP server and what Crush did
// with it.
type OpenCodeMCPStatus struct {
	Name   string
	Type   string // "local" or "remote"
	Target string // command line or URL
	State  string
}

// OpenCodeCatalogStatus describes OpenCode's cached models.dev catalog.
type OpenCodeCatalogStatus struct {
	Source    string
	Path      string
	UpdatedAt time.Time
	// Providers and Models count what OpenCode cached.
	Providers int
	Models    int
	// Usable counts providers Crush can talk to.
	Usable int
	// Listed counts providers new to Crush that are in its catalog now;
	// the rest appear once their API key variable is set.
	Listed int
	// AddedModels counts models added to providers Catwalk defines.
	AddedModels int
}

// OpenCodeStatus summarizes what the last config load read from a local
// OpenCode install.
type OpenCodeStatus struct {
	Enabled             bool
	MCPEnabled          bool
	ProvidersEnabled    bool
	ModelsEnabled       bool
	CredentialsEnabled  bool
	InstructionsEnabled bool
	SkillsEnabled       bool
	CommandsEnabled     bool
	ToolsEnabled        bool
	PermissionsEnabled  bool
	CompactionEnabled   bool
	ProjectIncluded     bool

	Paths    opencode.Paths
	Files    []string
	Warnings []string

	MCP         []OpenCodeMCPStatus
	Providers   []string
	Model       string
	SmallModel  string
	Credentials int

	// Applied is what the import changed beyond MCP servers and provider
	// settings, and what it found but left alone.
	Applied OpenCodeApplied

	Catalog    *OpenCodeCatalogStatus
	CatalogErr error
	// CatalogLocations lists where a catalog was looked for when none was
	// found.
	CatalogLocations []string
}

// OpenCodeStatus reports what was imported from OpenCode. It reads only
// local files.
func (s *ConfigStore) OpenCodeStatus(ctx context.Context) OpenCodeStatus {
	cfg := s.Config()
	settings := openCodeSettingsFor(cfg.Options)
	st := OpenCodeStatus{
		Enabled:             settings.enabled,
		MCPEnabled:          settings.mcp,
		ProvidersEnabled:    settings.providers,
		ModelsEnabled:       settings.models,
		CredentialsEnabled:  settings.credentials,
		InstructionsEnabled: settings.instructions,
		SkillsEnabled:       settings.skills,
		CommandsEnabled:     settings.commands,
		ToolsEnabled:        settings.tools,
		PermissionsEnabled:  settings.permissions,
		CompactionEnabled:   settings.compaction,
		Paths:               openCodePaths(),
	}
	if !settings.enabled {
		return st
	}

	if imp := s.openCode; imp != nil {
		st.ProjectIncluded = imp.includeProject
		st.Credentials = len(imp.creds)
		st.Applied = imp.applied.clone()
		if oc := imp.config; oc != nil {
			st.Files = oc.Files
			st.Warnings = oc.Warnings
			st.Model = oc.Model
			st.SmallModel = oc.SmallModel
			for _, id := range slices.Sorted(maps.Keys(oc.Providers)) {
				st.Providers = append(st.Providers, opencode.CatwalkID(id))
			}
			for _, name := range slices.Sorted(maps.Keys(oc.MCP)) {
				st.MCP = append(st.MCP, mcpStatus(name, oc.MCP[name], cfg.MCP, settings.mcp))
			}
		}
	}

	if settings.models {
		st.Catalog, st.CatalogErr = s.openCodeCatalogStatus(ctx)
		if st.Catalog == nil {
			st.CatalogLocations = opencode.CatalogLocations(st.Paths, os.Getenv)
		}
	}
	return st
}

func mcpStatus(name string, srv opencode.MCPServer, current MCPs, enabled bool) OpenCodeMCPStatus {
	st := OpenCodeMCPStatus{Name: name, Type: srv.Type, Target: srv.URL}
	if srv.Type == "local" {
		st.Target = strings.Join(srv.Command, " ")
	}
	want, ok := mcpFromOpenCode(srv)
	switch {
	case !ok:
		st.State = "skipped: unsupported"
	case !enabled:
		st.State = "not imported (options.opencode.mcp is false)"
	default:
		got, exists := current[name]
		switch {
		case !exists:
			st.State = "not loaded"
		case got.Type != want.Type || got.Command != want.Command || got.URL != want.URL:
			st.State = "overridden by Crush config"
		case got.Disabled:
			st.State = "imported (disabled)"
		default:
			st.State = "imported"
		}
	}
	return st
}

func (s *ConfigStore) openCodeCatalogStatus(ctx context.Context) (*OpenCodeCatalogStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cat, err := opencode.LoadCatalog(ctx, openCodePaths(), os.Getenv)
	if cat == nil {
		return nil, err
	}
	st := &OpenCodeCatalogStatus{
		Source:    cat.Source,
		Path:      cat.Path,
		UpdatedAt: cat.UpdatedAt,
		Providers: len(cat.Providers),
	}
	for _, p := range cat.Providers {
		st.Models += len(p.Models)
	}

	usable := opencode.ToCatwalk(cat.Providers)
	st.Usable = len(usable)

	catwalkIDs := make(map[catwalk.InferenceProvider]catwalk.Provider)
	for _, p := range loadCatwalkCatalog(cachePathFor("providers")) {
		catwalkIDs[p.ID] = p
	}
	known := make(map[catwalk.InferenceProvider]catwalk.Provider)
	for _, p := range s.knownProviders {
		known[p.ID] = p
	}
	for _, p := range usable {
		k, listed := known[p.ID]
		cw, inCatwalk := catwalkIDs[p.ID]
		switch {
		case listed && !inCatwalk:
			st.Listed++
		case listed && inCatwalk:
			st.AddedModels += len(k.Models) - len(cw.Models)
		}
	}
	return st, err
}
