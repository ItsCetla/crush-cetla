package opencode

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// mcpV1Server is the V1 mcp.<name> entry (packages/core/src/v1/config/mcp.ts).
type mcpV1Server struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Cwd         string            `json:"cwd"`
	Environment map[string]string `json:"environment"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	OAuth       json.RawMessage   `json:"oauth"`
	Enabled     *bool             `json:"enabled"`
	Timeout     *float64          `json:"timeout"`
}

type mcpV1OAuth struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	Scope        string `json:"scope"`
	CallbackPort int    `json:"callbackPort"`
	RedirectURI  string `json:"redirectUri"`
}

// mcpV2Timeout is the V2 timeout object, in milliseconds.
type mcpV2Timeout struct {
	Startup   *float64 `json:"startup"`
	Catalog   *float64 `json:"catalog"`
	Execution *float64 `json:"execution"`
}

// mcpV2Server is the V2 mcp.servers.<name> entry
// (packages/schema/src/mcp.ts).
type mcpV2Server struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Cwd         string            `json:"cwd"`
	Environment map[string]string `json:"environment"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	OAuth       json.RawMessage   `json:"oauth"`
	Disabled    *bool             `json:"disabled"`
	Timeout     *mcpV2Timeout     `json:"timeout"`
}

type mcpV2OAuth struct {
	ClientID              string `json:"client_id"`
	ClientSecret          string `json:"client_secret"`
	Scope                 string `json:"scope"`
	CallbackPort          int    `json:"callback_port"`
	RedirectURI           string `json:"redirect_uri"`
	AuthServerMetadataURL string `json:"auth_server_metadata_url"`
}

// mcpDef is one server definition from a single document.
type mcpDef struct {
	server    MCPServer
	enabled   *bool
	v1Timeout int
	v2Timeout int
}

// applyMCP merges one document's mcp key. Under mcp, "servers" is the V2
// server map and "timeout" the V2 default timeouts, unless their value is
// itself a local/remote server; every other key is a V1 server. An entry
// with only a boolean "enabled" and no type is a V1 toggle for a server
// defined elsewhere. Each server name is replaced whole by a later
// document, except for the enabled flag, which carries over like V1's deep
// merge when the later definition does not set it.
func (m *merger) applyMCP(raw json.RawMessage, source string) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		m.warnf("%s: mcp: expected an object", source)
		return
	}

	legacy := map[string]mcpDef{}
	native := map[string]mcpDef{}
	toggles := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		value := entries[name]
		if enabled, ok := mcpToggle(value); ok {
			toggles[name] = enabled
			continue
		}
		direct := isDirectServer(value)
		switch {
		case name == "servers" && !direct:
			var servers map[string]json.RawMessage
			if err := json.Unmarshal(value, &servers); err != nil {
				m.warnf("%s: mcp.servers: expected an object", source)
				continue
			}
			for _, srvName := range slices.Sorted(maps.Keys(servers)) {
				def, err := decodeV2Server(srvName, servers[srvName], source)
				if err != nil {
					m.warnf("%s: mcp.servers.%s: %v", source, srvName, err)
					continue
				}
				native[srvName] = def
			}
		case name == "timeout" && !direct:
			var t mcpV2Timeout
			if err := json.Unmarshal(value, &t); err != nil {
				m.warnf("%s: mcp.timeout: %v", source, err)
				continue
			}
			if t.Startup != nil && *t.Startup > 0 {
				m.globalStartup = int(*t.Startup)
			}
		default:
			def, err := decodeV1Server(name, value, source)
			if err != nil {
				m.warnf("%s: mcp.%s: %v", source, name, err)
				continue
			}
			legacy[name] = def
		}
	}

	defs := legacy
	maps.Copy(defs, native)
	for _, name := range slices.Sorted(maps.Keys(toggles)) {
		enabled := toggles[name]
		m.mcpEnabled[name] = enabled
		if srv, ok := m.cfg.MCP[name]; ok {
			srv.Disabled = !enabled
			m.cfg.MCP[name] = srv
		}
	}
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		def := defs[name]
		if def.enabled != nil {
			m.mcpEnabled[name] = *def.enabled
		} else if enabled, ok := m.mcpEnabled[name]; ok {
			def.server.Disabled = !enabled
		}
		m.cfg.MCP[name] = def.server
		m.v1Timeout[name] = def.v1Timeout
		m.v2Timeout[name] = def.v2Timeout
	}
}

// mcpToggle reports whether value is a V1 enabled-only entry.
func mcpToggle(value json.RawMessage) (enabled bool, ok bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(value, &obj) != nil || obj == nil {
		return false, false
	}
	if _, hasType := obj["type"]; hasType {
		return false, false
	}
	raw, ok := obj["enabled"]
	if !ok {
		return false, false
	}
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return false, false
	}
	return enabled, true
}

func isDirectServer(value json.RawMessage) bool {
	var probe struct {
		Type any `json:"type"`
	}
	if json.Unmarshal(value, &probe) != nil {
		return false
	}
	return probe.Type == "local" || probe.Type == "remote"
}

func decodeV1Server(name string, raw json.RawMessage, source string) (mcpDef, error) {
	var v mcpV1Server
	if err := json.Unmarshal(raw, &v); err != nil {
		return mcpDef{}, err
	}
	srv, err := baseServer(name, v.Type, v.Command, v.URL, source)
	if err != nil {
		return mcpDef{}, err
	}
	srv.Cwd = v.Cwd
	srv.Environment = v.Environment
	srv.Headers = v.Headers
	if v.Enabled != nil {
		srv.Disabled = !*v.Enabled
	}
	if srv.Type == "remote" {
		if err := decodeOAuth(&srv, v.OAuth, func(raw json.RawMessage) (*MCPOAuth, error) {
			var o mcpV1OAuth
			if err := json.Unmarshal(raw, &o); err != nil {
				return nil, err
			}
			return &MCPOAuth{
				ClientID:     o.ClientID,
				ClientSecret: o.ClientSecret,
				Scope:        o.Scope,
				CallbackPort: o.CallbackPort,
				RedirectURI:  o.RedirectURI,
			}, nil
		}); err != nil {
			return mcpDef{}, err
		}
	}
	def := mcpDef{server: srv, enabled: v.Enabled}
	if v.Timeout != nil && *v.Timeout > 0 {
		def.v1Timeout = int(*v.Timeout)
	}
	return def, nil
}

func decodeV2Server(name string, raw json.RawMessage, source string) (mcpDef, error) {
	var v mcpV2Server
	if err := json.Unmarshal(raw, &v); err != nil {
		return mcpDef{}, err
	}
	srv, err := baseServer(name, v.Type, v.Command, v.URL, source)
	if err != nil {
		return mcpDef{}, err
	}
	srv.Cwd = v.Cwd
	srv.Environment = v.Environment
	srv.Headers = v.Headers
	def := mcpDef{server: srv}
	if v.Disabled != nil {
		def.server.Disabled = *v.Disabled
		enabled := !*v.Disabled
		def.enabled = &enabled
	}
	if def.server.Type == "remote" {
		if err := decodeOAuth(&def.server, v.OAuth, func(raw json.RawMessage) (*MCPOAuth, error) {
			var o mcpV2OAuth
			if err := json.Unmarshal(raw, &o); err != nil {
				return nil, err
			}
			return &MCPOAuth{
				ClientID:              o.ClientID,
				ClientSecret:          o.ClientSecret,
				Scope:                 o.Scope,
				CallbackPort:          o.CallbackPort,
				RedirectURI:           o.RedirectURI,
				AuthServerMetadataURL: o.AuthServerMetadataURL,
			}, nil
		}); err != nil {
			return mcpDef{}, err
		}
	}
	if v.Timeout != nil && v.Timeout.Startup != nil && *v.Timeout.Startup > 0 {
		def.v2Timeout = int(*v.Timeout.Startup)
	}
	return def, nil
}

func baseServer(name, typ string, command []string, url, source string) (MCPServer, error) {
	srv := MCPServer{Name: name, Type: typ, SourcePath: source}
	switch typ {
	case "local":
		if len(command) == 0 {
			return srv, fmt.Errorf("local server needs a command")
		}
		srv.Command = command
	case "remote":
		if url == "" {
			return srv, fmt.Errorf("remote server needs a url")
		}
		srv.URL = url
	default:
		return srv, fmt.Errorf("unknown server type %q", typ)
	}
	return srv, nil
}

func decodeOAuth(srv *MCPServer, raw json.RawMessage, decode func(json.RawMessage) (*MCPOAuth, error)) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return fmt.Errorf("oauth must be an object or false")
		}
		srv.OAuthDisabled = true
		return nil
	}
	o, err := decode(raw)
	if err != nil {
		return fmt.Errorf("oauth: %w", err)
	}
	srv.OAuth = o
	return nil
}
