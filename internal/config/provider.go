package config

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
)

// defaultCatwalkURL is where `tada update-providers` fetches the Catwalk
// catalog from when no source is given. Nothing else contacts it.
const defaultCatwalkURL = "https://catwalk.charm.land"

var (
	providerOnce sync.Once
	providerList []catwalk.Provider
	providerErr  error
)

// file to cache provider data. It resolves through GlobalConfigData so the
// catalog follows TADA_GLOBAL_DATA like the rest of the data directory.
func cachePathFor(name string) string {
	return filepath.Join(filepath.Dir(GlobalConfigData()), name+".json")
}

// UpdateProviders updates the Catwalk providers list from a specified source
// and saves it where [Providers] picks it up. It backs the explicit
// `tada update-providers` command, which is the only way the catalog is
// ever fetched over the network.
func UpdateProviders(pathOrURL string) error {
	var providers []catwalk.Provider
	pathOrURL = cmp.Or(pathOrURL, os.Getenv("CATWALK_URL"), defaultCatwalkURL)

	switch {
	case pathOrURL == "embedded":
		providers = embedded.GetAll()
	case strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://"):
		var err error
		providers, err = catwalk.NewWithURL(pathOrURL).GetProviders(context.Background(), "")
		if err != nil {
			return fmt.Errorf("failed to fetch providers from Catwalk: %w", err)
		}
	default:
		content, err := os.ReadFile(pathOrURL)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
		if err := json.Unmarshal(content, &providers); err != nil {
			return fmt.Errorf("failed to unmarshal provider data: %w", err)
		}
		if len(providers) == 0 {
			return fmt.Errorf("no providers found in the provided source")
		}
	}

	path := cachePathFor("providers")
	if err := newCache[[]catwalk.Provider](path).Store(providers); err != nil {
		return fmt.Errorf("failed to save providers to cache: %w", err)
	}

	slog.Info("Providers updated successfully", "count", len(providers), "from", pathOrURL, "to", path)
	return nil
}

// catalogSource is one local source of provider definitions for
// [Providers]. Load must not perform network I/O: the catalog is built on
// every config load, including startup.
//
// Like the error Providers returns, an error from Load is advisory. A
// source that runs into trouble should still return the best providers it
// has (possibly none) and describe the problem in the error.
type catalogSource struct {
	name string
	load func() ([]catwalk.Provider, error)
	// augment adds this source's models to a provider an earlier source
	// already defined (same ID and type) instead of dropping the
	// duplicate. The earlier definition still wins for everything else.
	augment bool
	// include, when set, filters the providers this source adds that no
	// earlier source defined.
	include func(catwalk.Provider) bool
}

// catalogSources are the sources [Providers] merges, in precedence order:
// when several sources define the same provider ID, the definition from
// the earliest source wins and the later ones are dropped. Catwalk comes
// first so that its curated definitions always take precedence. To add a
// source, append it here.
//
// A package variable so tests can substitute their own sources.
var catalogSources = []catalogSource{
	{
		name: "catwalk",
		load: func() ([]catwalk.Provider, error) {
			return loadCatwalkCatalog(cachePathFor("providers")), nil
		},
	},
}

// Providers returns the provider catalog: the known providers and their
// models. It is built once per process, from local data only, by merging
// [catalogSources]. It never touches the network; the Catwalk part of the
// catalog is refreshed only by an explicit `tada update-providers`.
//
// A returned error is advisory: it reports that a source could not be read
// in full. It never means that no providers are available, so callers
// should surface it as a warning and keep using the returned list.
func Providers(cfg *Config) ([]catwalk.Provider, error) {
	providerOnce.Do(func() {
		if cfg != nil && cfg.Options != nil && cfg.Options.DisableDefaultProviders {
			return
		}
		sources := catalogSources
		if openCodeSettingsFor(cfgOptions(cfg)).models {
			sources = append(slices.Clone(sources), openCodeCatalogSource(cfg))
		}
		providerList, providerErr = mergeCatalogs(sources)
	})
	return providerList, providerErr
}

func cfgOptions(cfg *Config) *Options {
	if cfg == nil {
		return nil
	}
	return cfg.Options
}

// mergeCatalogs loads every source in order and concatenates their
// providers, keeping only the first definition of each provider ID. An
// augmenting source contributes the models the first definition lacks.
func mergeCatalogs(sources []catalogSource) ([]catwalk.Provider, error) {
	var (
		merged []catwalk.Provider
		errs   []error
		index  = make(map[catwalk.InferenceProvider]int)
		owner  = make(map[catwalk.InferenceProvider]string)
	)
	for _, src := range sources {
		providers, err := src.load()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s provider catalog: %w", src.name, err))
		}
		for _, p := range providers {
			if i, ok := index[p.ID]; ok {
				if src.augment {
					addMissingModels(&merged[i], p)
				} else {
					slog.Debug("Ignoring duplicate catalog provider", "provider", p.ID, "source", src.name, "kept_from", owner[p.ID])
				}
				continue
			}
			if src.include != nil && !src.include(p) {
				continue
			}
			index[p.ID] = len(merged)
			owner[p.ID] = src.name
			merged = append(merged, p)
		}
	}
	return merged, errors.Join(errs...)
}

// addMissingModels appends the models of src that dst lacks. Providers of
// different types disagree on model IDs, and so do Bedrock catalogs
// (Catwalk uses inference-profile IDs), so those are left alone.
func addMissingModels(dst *catwalk.Provider, src catwalk.Provider) {
	if dst.Type != src.Type || dst.Type == catwalk.TypeBedrock {
		return
	}
	have := make(map[string]bool, len(dst.Models))
	for _, m := range dst.Models {
		have[m.ID] = true
	}
	// Clip so appending never writes into an array another catalog copy
	// shares.
	models := slices.Clip(dst.Models)
	for _, m := range src.Models {
		if !have[m.ID] {
			models = append(models, m)
		}
	}
	dst.Models = models
}

type cache[T any] struct {
	path string
}

func newCache[T any](path string) cache[T] {
	return cache[T]{path: path}
}

func (c cache[T]) Get() (T, error) {
	var v T
	data, err := readFile(c.path)
	if err != nil {
		return v, fmt.Errorf("failed to read provider cache file: %w", err)
	}

	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("failed to unmarshal provider data from cache: %w", err)
	}

	return v, nil
}

func (c cache[T]) Store(v T) error {
	slog.Info("Saving provider data to disk", "path", c.path)
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for provider cache: %w", err)
	}

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal provider data: %w", err)
	}

	// Written through a temporary file and renamed into place. A Ta-Da
	// instance may be reading the catalog while `tada update-providers`
	// replaces it, and a truncating write would let that reader see a
	// half-written catalog and silently fall back to the bundled copy.
	if err := atomicWriteFile(c.path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write provider data to cache: %w", err)
	}
	return nil
}
