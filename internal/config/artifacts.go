package config

import (
	"encoding/json"
	"os"
	"strings"
)

// ArtifactsOptions configure artifacts: pages, documents and slide decks
// the agent publishes to a local viewer.
type ArtifactsOptions struct {
	Disabled     bool  `json:"disabled,omitempty" jsonschema:"description=Turn off the artifact tools and the local viewer,default=false"`
	Port         int   `json:"port,omitempty" jsonschema:"description=Port of the local viewer on 127.0.0.1. The next free port is used when it's taken,default=7480"`
	OpenOnCreate *bool `json:"open_on_create,omitempty" jsonschema:"description=Open a new artifact in the browser when it's first published,default=true"`
	AllowCDN     bool  `json:"allow_cdn,omitempty" jsonschema:"description=Let artifact pages load scripts from cdnjs\\, jsDelivr\\, unpkg\\, the Tailwind CDN and the jQuery CDN\\, and fonts from Google Fonts. Off by default: pages make no outside requests,default=false"`
}

// ArtifactsEnabled reports whether the artifact tools are on.
func (o *Options) ArtifactsEnabled() bool {
	return o == nil || o.Artifacts == nil || !o.Artifacts.Disabled
}

// ArtifactsOrDefault returns the artifacts options with defaults applied.
func (o *Options) ArtifactsOrDefault() ArtifactsOptions {
	var a ArtifactsOptions
	if o != nil && o.Artifacts != nil {
		a = *o.Artifacts
	}
	if a.OpenOnCreate == nil {
		t := true
		a.OpenOnCreate = &t
	}
	return a
}

// GlobalArtifactsOptions reads options.artifacts from the global JSON
// config files only, without loading the full config. It's for commands
// that must start fast, such as the artifacts MCP server.
func GlobalArtifactsOptions() ArtifactsOptions {
	var out Options
	for _, p := range globalConfigPaths() {
		if isShellConfig(p) || !strings.HasSuffix(p, ".json") {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var c struct {
			Options struct {
				Artifacts *ArtifactsOptions `json:"artifacts"`
			} `json:"options"`
		}
		if json.Unmarshal(data, &c) == nil && c.Options.Artifacts != nil {
			out.Artifacts = c.Options.Artifacts
		}
	}
	return out.ArtifactsOrDefault()
}
