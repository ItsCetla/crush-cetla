package discover

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"charm.land/catwalk/pkg/catwalk"
)

// ProbeTimeout bounds each local server probe. Servers on this computer
// answer in a few milliseconds, so a short timeout keeps a closed port
// from slowing anything down.
const ProbeTimeout = 300 * time.Millisecond

// ServerKind names the kind of local model server a probe found.
type ServerKind string

const (
	// KindOllama is an Ollama server.
	KindOllama ServerKind = "ollama"
	// KindLMStudio is an LM Studio server.
	KindLMStudio ServerKind = "lmstudio"
	// KindLlamaCpp is llama.cpp's llama-server.
	KindLlamaCpp ServerKind = "llamacpp"
	// KindOpenAICompat is any other OpenAI-compatible server.
	KindOpenAICompat ServerKind = "openai-compat"
)

// Target is a place where a local model server might be listening.
type Target struct {
	// Kind is what usually listens there. A KindLlamaCpp target that
	// turns out not to be llama.cpp is reported as KindOpenAICompat.
	Kind ServerKind
	// Root is the server root, without any /v1 suffix.
	Root string
}

// DefaultTargets are the usual ports of the popular local model servers.
var DefaultTargets = []Target{
	{Kind: KindOllama, Root: "http://localhost:11434"},
	{Kind: KindLMStudio, Root: "http://localhost:1234"},
	{Kind: KindLlamaCpp, Root: "http://localhost:8080"},
}

// LocalServer is a model server found on this computer.
type LocalServer struct {
	Kind ServerKind
	// Name is a display name such as "Ollama".
	Name string
	// Version is the server version when it reports one.
	Version string
	// Root is the server root, without any /v1 suffix.
	Root string
	// BaseURL is what goes in the provider's base_url.
	BaseURL string
	// ProviderType is what goes in the provider's type.
	ProviderType catwalk.Type
	// ProviderID is the suggested provider ID, such as "ollama".
	ProviderID string
	// Models holds the chat models the server offers. It may be empty
	// when the server is running but has nothing downloaded yet.
	Models []LocalModel
}

// LocalModel is a model offered by a local server.
type LocalModel struct {
	ID string
	// ParameterSize is a size label such as "7.6B", when known.
	ParameterSize string
}

// FindLocal probes the default targets for local model servers.
func FindLocal(ctx context.Context) []LocalServer {
	return Probe(ctx, DefaultTargets, ProbeTimeout)
}

// Probe checks every target at once and returns the servers that
// answered, in target order. Each probe gets its own timeout; targets
// that are closed, slow or answer with something else are left out.
func Probe(ctx context.Context, targets []Target, timeout time.Duration) []LocalServer {
	found := make([]*LocalServer, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			if s, err := probe(pctx, t); err == nil {
				found[i] = s
			}
		})
	}
	wg.Wait()

	var out []LocalServer
	for _, s := range found {
		if s != nil {
			out = append(out, *s)
		}
	}
	return out
}

func probe(ctx context.Context, t Target) (*LocalServer, error) {
	root := strings.TrimRight(t.Root, "/")
	if t.Kind == KindOllama {
		return probeOllama(ctx, root)
	}
	return probeOpenAI(ctx, t.Kind, root)
}

// probeOllama reads /api/tags and /api/version concurrently.
func probeOllama(ctx context.Context, root string) (*LocalServer, error) {
	var version string
	var wg sync.WaitGroup
	wg.Go(func() {
		var v struct {
			Version string `json:"version"`
		}
		if getJSON(ctx, root+"/api/version", &v) == nil {
			version = label(v.Version, 32)
		}
	})

	var tags struct {
		Models *[]struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	err := getJSON(ctx, root+"/api/tags", &tags)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	if tags.Models == nil {
		return nil, fmt.Errorf("%s doesn't look like Ollama", root)
	}

	s := &LocalServer{
		Kind:         KindOllama,
		Name:         "Ollama",
		Version:      version,
		Root:         root,
		BaseURL:      root + "/v1",
		ProviderType: catwalk.Type(KindOllama),
		ProviderID:   string(KindOllama),
	}
	for _, m := range *tags.Models {
		id := cmp.Or(m.Name, m.Model)
		if !isChatModel(id) {
			continue
		}
		s.Models = append(s.Models, LocalModel{ID: id, ParameterSize: label(m.Details.ParameterSize, 16)})
	}
	return s, nil
}

// probeOpenAI reads an OpenAI-style /v1/models listing.
func probeOpenAI(ctx context.Context, kind ServerKind, root string) (*LocalServer, error) {
	var list struct {
		Data *[]struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := getJSON(ctx, root+"/v1/models", &list); err != nil {
		return nil, err
	}
	if list.Data == nil {
		return nil, fmt.Errorf("%s doesn't list models", root)
	}

	s := &LocalServer{Kind: kind, Root: root, BaseURL: root + "/v1"}
	if kind == KindLlamaCpp {
		// Port 8080 is popular; only call it llama.cpp when it says so.
		s.Kind = KindOpenAICompat
		for _, m := range *list.Data {
			if m.OwnedBy == "llamacpp" {
				s.Kind = KindLlamaCpp
				break
			}
		}
	}
	switch s.Kind {
	case KindLMStudio:
		s.Name, s.ProviderID = "LM Studio", "lmstudio"
		s.ProviderType = catwalk.Type(KindLMStudio)
	case KindLlamaCpp:
		// The llamacpp enricher adds /v1 itself, so the base URL is the
		// server root.
		s.Name, s.ProviderID = "llama.cpp", "llamacpp"
		s.ProviderType = catwalk.Type(KindLlamaCpp)
		s.BaseURL = root
	default:
		s.Name, s.ProviderID = "Local server", "local"
		s.ProviderType = catwalk.TypeOpenAICompat
	}
	for _, m := range *list.Data {
		if isChatModel(m.ID) {
			s.Models = append(s.Models, LocalModel{ID: m.ID})
		}
	}
	return s, nil
}

// isChatModel leaves out embedding models, which can't hold a
// conversation, and IDs that aren't plain printable text. Any process on
// this computer can answer a probe, so an ID carrying escape sequences or
// invisible characters is dropped rather than printed or saved.
func isChatModel(id string) bool {
	if id == "" || len(id) > 256 || strings.Contains(strings.ToLower(id), "embed") {
		return false
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// label keeps only the printable characters of a server-supplied display
// string, such as a version, and caps its length.
func label(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

func getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}
