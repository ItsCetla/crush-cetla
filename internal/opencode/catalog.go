package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Catalog source kinds reported in [Catalog.Source].
const (
	SourceModelsPath = "OPENCODE_MODELS_PATH"
	SourceDB         = "opencode.db"
	SourceModelsJSON = "models.json"
)

// catalogKey is the kv row where OpenCode V2 caches the default
// models.dev catalog (packages/core/src/models-dev.ts).
const catalogKey = "models-dev:catalog"

// ModelsDevProvider is one provider of the models.dev api.json catalog.
type ModelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	Env    []string                  `json:"env"`
	NPM    string                    `json:"npm"`
	API    string                    `json:"api,omitempty"`
	Doc    string                    `json:"doc,omitempty"`
	Models map[string]ModelsDevModel `json:"models"`
}

// ModelsDevModel is one model of a models.dev provider.
type ModelsDevModel struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Family           string                     `json:"family,omitempty"`
	Attachment       bool                       `json:"attachment"`
	Reasoning        bool                       `json:"reasoning"`
	ToolCall         bool                       `json:"tool_call"`
	Temperature      *bool                      `json:"temperature,omitempty"`
	ReleaseDate      string                     `json:"release_date,omitempty"`
	LastUpdated      string                     `json:"last_updated,omitempty"`
	Modalities       ModelsDevModalities        `json:"modalities"`
	OpenWeights      bool                       `json:"open_weights"`
	Cost             *ModelsDevCost             `json:"cost,omitempty"`
	Limit            ModelsDevLimit             `json:"limit"`
	ReasoningOptions []ModelsDevReasoningOption `json:"reasoning_options,omitempty"`
	// Status is "", "alpha", "beta" or "deprecated".
	Status string `json:"status,omitempty"`
	// Provider overrides the provider's package or endpoint for this model.
	Provider *ModelsDevModelProvider `json:"provider,omitempty"`
	// Type is "" for chat models, or "embedding", "reranking", "video".
	Type string `json:"type,omitempty"`
}

// ModelsDevModalities lists input and output modalities ("text", "image",
// "audio", "video", "pdf").
type ModelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// ModelsDevCost is pricing in USD per million tokens.
type ModelsDevCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// ModelsDevLimit holds token limits.
type ModelsDevLimit struct {
	Context int64 `json:"context"`
	Input   int64 `json:"input,omitempty"`
	Output  int64 `json:"output"`
}

// ModelsDevReasoningOption describes one way to control reasoning: Type is
// "effort" (with Values), "toggle", or "budget_tokens" (with Min/Max).
type ModelsDevReasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
	Min    *int64   `json:"min,omitempty"`
	Max    *int64   `json:"max,omitempty"`
}

// ModelsDevModelProvider is a per-model package/endpoint override.
type ModelsDevModelProvider struct {
	NPM string `json:"npm,omitempty"`
	API string `json:"api,omitempty"`
	// Shape is "responses" or "completions" when the model needs one API.
	Shape string `json:"shape,omitempty"`
}

// rawReasoningOption tolerates null entries in effort values.
type rawReasoningOption struct {
	Type   string    `json:"type"`
	Values []*string `json:"values"`
	Min    *float64  `json:"min"`
	Max    *float64  `json:"max"`
}

// rawModel decodes numbers loosely; models.dev occasionally ships floats.
type rawModel struct {
	ModelsDevModel
	Limit *struct {
		Context float64 `json:"context"`
		Input   float64 `json:"input"`
		Output  float64 `json:"output"`
	} `json:"limit"`
	ReasoningOptions []json.RawMessage `json:"reasoning_options"`
}

// Catalog is a models.dev catalog read from OpenCode's local caches.
type Catalog struct {
	Providers map[string]ModelsDevProvider
	// Source is the kind of source: SourceModelsPath, SourceDB or
	// SourceModelsJSON.
	Source string
	// Path is the file the catalog was read from.
	Path string
	// UpdatedAt is when OpenCode cached the catalog (opencode.db), or the
	// file's modification time otherwise.
	UpdatedAt time.Time
}

// ParseCatalog decodes a models.dev api.json document. Providers or models
// that do not decode are skipped; an error is returned only when the
// document itself is not a JSON object.
func ParseCatalog(raw []byte) (map[string]ModelsDevProvider, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("parse models.dev catalog: %w", err)
	}
	if top == nil {
		return nil, errors.New("parse models.dev catalog: not an object")
	}
	out := make(map[string]ModelsDevProvider, len(top))
	for key, rawProvider := range top {
		var p struct {
			ModelsDevProvider
			Models map[string]json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(rawProvider, &p); err != nil {
			continue
		}
		provider := p.ModelsDevProvider
		if provider.ID == "" {
			provider.ID = key
		}
		provider.Models = make(map[string]ModelsDevModel, len(p.Models))
		for modelKey, rawM := range p.Models {
			m, ok := parseModel(rawM)
			if !ok {
				continue
			}
			if m.ID == "" {
				m.ID = modelKey
			}
			provider.Models[modelKey] = m
		}
		out[key] = provider
	}
	return out, nil
}

func parseModel(raw json.RawMessage) (ModelsDevModel, bool) {
	var r rawModel
	if err := json.Unmarshal(raw, &r); err != nil {
		return ModelsDevModel{}, false
	}
	m := r.ModelsDevModel
	if r.Limit != nil {
		m.Limit = ModelsDevLimit{
			Context: int64(r.Limit.Context),
			Input:   int64(r.Limit.Input),
			Output:  int64(r.Limit.Output),
		}
	}
	m.ReasoningOptions = nil
	for _, rawOpt := range r.ReasoningOptions {
		var o rawReasoningOption
		if err := json.Unmarshal(rawOpt, &o); err != nil {
			continue
		}
		opt := ModelsDevReasoningOption{Type: o.Type}
		for _, v := range o.Values {
			if v != nil && *v != "" && *v != "null" {
				opt.Values = append(opt.Values, *v)
			}
		}
		if o.Min != nil {
			opt.Min = toInt64(o.Min)
		}
		if o.Max != nil {
			opt.Max = toInt64(o.Max)
		}
		m.ReasoningOptions = append(m.ReasoningOptions, opt)
	}
	return m, true
}

// LoadCatalog finds the newest local models.dev catalog without touching
// the network. Sources, in priority order:
//
//  1. the file named by $OPENCODE_MODELS_PATH;
//  2. OpenCode V2's database ($OPENCODE_DB, else <data>/opencode.db, else
//     the most recently modified <data>/opencode-*.db), kv row
//     "models-dev:catalog";
//  3. OpenCode V1's <cache>/models.json.
//
// A source that is missing or unusable falls through to the next one; the
// errors of unusable sources are only returned when no source works. It
// returns (nil, nil) when no catalog exists. Catalogs OpenCode fetched from
// a custom $OPENCODE_MODELS_URL are cached under a hashed key and are not
// read.
func LoadCatalog(ctx context.Context, p Paths, getenv func(string) string) (*Catalog, error) {
	getenv = envOrEmpty(getenv)
	var errs []error

	if path := getenv("OPENCODE_MODELS_PATH"); path != "" {
		cat, err := catalogFromFile(path, SourceModelsPath)
		if err == nil && cat != nil {
			return cat, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}

	if dbPath := databasePath(p, getenv); dbPath != "" {
		cat, err := catalogFromDB(ctx, dbPath)
		if err == nil && cat != nil {
			return cat, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}

	if p.CacheDir != "" {
		cat, err := catalogFromFile(filepath.Join(p.CacheDir, "models.json"), SourceModelsJSON)
		if err == nil && cat != nil {
			return cat, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, nil
}

func catalogFromFile(path, kind string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	providers, err := ParseCatalog(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cat := &Catalog{Providers: providers, Source: kind, Path: path}
	if info, err := os.Stat(path); err == nil {
		cat.UpdatedAt = info.ModTime()
	}
	return cat, nil
}

// databasePath locates OpenCode V2's SQLite database the way
// packages/cli/src/database-path.ts names it. Channel builds use
// opencode-<channel>.db; when opencode.db is missing the most recently
// modified one is used. It returns "" when there is none.
func databasePath(p Paths, getenv func(string) string) string {
	if v := getenv("OPENCODE_DB"); v != "" {
		if v == ":memory:" {
			return ""
		}
		if !filepath.IsAbs(v) {
			if p.DataDir == "" {
				return ""
			}
			v = filepath.Join(p.DataDir, v)
		}
		return v
	}
	if p.DataDir == "" {
		return ""
	}
	main := filepath.Join(p.DataDir, "opencode.db")
	if fileExists(main) {
		return main
	}
	matches, _ := filepath.Glob(filepath.Join(p.DataDir, "opencode-*.db"))
	var (
		best     string
		bestTime time.Time
	)
	for _, m := range slices.Sorted(slices.Values(matches)) {
		info, err := os.Stat(m)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = m, info.ModTime()
		}
	}
	return best
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func catalogFromDB(ctx context.Context, path string) (*Catalog, error) {
	if !fileExists(path) {
		return nil, nil
	}
	var value string
	err := withReadOnlyDB(ctx, path, func(ctx context.Context, db *sql.DB) error {
		value = ""
		ok, err := hasTable(ctx, db, "kv")
		if err != nil || !ok {
			return err
		}
		err = db.QueryRowContext(ctx, "SELECT value FROM kv WHERE key = ?", catalogKey).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read models.dev cache from %s: %w", path, err)
	}
	if value == "" {
		return nil, nil
	}
	var cached struct {
		UpdatedAt float64         `json:"updatedAt"`
		Body      json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(value), &cached); err != nil {
		return nil, fmt.Errorf("decode models.dev cache from %s: %w", path, err)
	}
	// The body is api.json stored as a JSON string; accept a raw object
	// too.
	body := []byte(cached.Body)
	var text string
	if err := json.Unmarshal(cached.Body, &text); err == nil {
		body = []byte(text)
	}
	providers, err := ParseCatalog(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cat := &Catalog{Providers: providers, Source: SourceDB, Path: path}
	if cached.UpdatedAt > 0 {
		cat.UpdatedAt = time.UnixMilli(int64(cached.UpdatedAt))
	}
	return cat, nil
}

func hasTable(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n)
	return n > 0, err
}

// providerIDs returns the catalog's provider IDs in sorted order.
func providerIDs(md map[string]ModelsDevProvider) []string {
	return slices.Sorted(maps.Keys(md))
}
