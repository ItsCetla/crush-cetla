package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// catalogJSON is a minimal api.json with one provider named after tag.
func catalogJSON(tag string) string {
	return `{"` + tag + `": {"id": "` + tag + `", "name": "` + tag + `", "env": ["X_API_KEY"], "npm": "@ai-sdk/openai-compatible", "api": "https://x.example.com/v1",
		"models": {"m": {"id": "m", "name": "M", "tool_call": true, "reasoning": false, "attachment": false,
			"modalities": {"input": ["text"], "output": ["text"]}, "limit": {"context": 1000, "output": 100}}}}}`
}

// newOpenCodeDB creates an OpenCode-like V2 database in WAL mode with the
// kv and credential tables, runs setup statements, and returns an open
// handle the test may keep as a concurrent writer.
func newOpenCodeDB(t *testing.T, path string, stmts ...string) *sql.DB {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := openSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	base := []string{
		`PRAGMA journal_mode = WAL`,
		"CREATE TABLE `kv` (`key` text PRIMARY KEY, `value` text NOT NULL, `time_created` integer NOT NULL, `time_updated` integer NOT NULL)",
		"CREATE TABLE `credential` (`id` text PRIMARY KEY, `integration_id` text, `label` text NOT NULL, `value` text NOT NULL, `connector_id` text, `method_id` text, `active` integer, `time_created` integer NOT NULL, `time_updated` integer NOT NULL)",
	}
	for _, stmt := range append(base, stmts...) {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

func putCatalog(t *testing.T, db *sql.DB, body string, updatedAt int64) {
	t.Helper()
	value, err := json.Marshal(map[string]any{"updatedAt": updatedAt, "digest": "abc", "body": body})
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO kv (key, value, time_created, time_updated) VALUES (?, ?, 0, 0)`, catalogKey, string(value))
	require.NoError(t, err)
}

func TestLoadCatalogSources(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := Paths{
		DataDir:  filepath.Join(dir, "data"),
		CacheDir: filepath.Join(dir, "cache"),
	}
	modelsPath := filepath.Join(dir, "api.json")
	writeFile(t, modelsPath, catalogJSON("from-env"))
	writeFile(t, filepath.Join(p.CacheDir, "models.json"), catalogJSON("from-v1"))
	dbPath := filepath.Join(p.DataDir, "opencode.db")
	db := newOpenCodeDB(t, dbPath)
	putCatalog(t, db, catalogJSON("from-db"), 1_760_000_000_123)
	require.NoError(t, db.Close())

	ctx := context.Background()

	cat, err := LoadCatalog(ctx, p, mapEnv(map[string]string{"OPENCODE_MODELS_PATH": modelsPath}))
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "from-env")
	require.Equal(t, SourceModelsPath, cat.Source)
	require.Equal(t, modelsPath, cat.Path)
	require.False(t, cat.UpdatedAt.IsZero())

	cat, err = LoadCatalog(ctx, p, nil)
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "from-db")
	require.Equal(t, SourceDB, cat.Source)
	require.Equal(t, dbPath, cat.Path)
	require.Equal(t, time.UnixMilli(1_760_000_000_123), cat.UpdatedAt)
	m := cat.Providers["from-db"].Models["m"]
	require.Equal(t, int64(1000), m.Limit.Context)
	require.True(t, m.ToolCall)

	// A missing OPENCODE_MODELS_PATH falls through to the database.
	cat, err = LoadCatalog(ctx, p, mapEnv(map[string]string{"OPENCODE_MODELS_PATH": filepath.Join(dir, "missing.json")}))
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "from-db")

	// Without the database row, the V1 cache is used.
	require.NoError(t, os.Remove(dbPath))
	cat, err = LoadCatalog(ctx, p, nil)
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "from-v1")
	require.Equal(t, SourceModelsJSON, cat.Source)
	require.Equal(t, filepath.Join(p.CacheDir, "models.json"), cat.Path)

	// Nothing at all is not an error.
	cat, err = LoadCatalog(ctx, Paths{DataDir: filepath.Join(dir, "x"), CacheDir: filepath.Join(dir, "y")}, nil)
	require.NoError(t, err)
	require.Nil(t, cat)
}

func TestLoadCatalogDBWithoutRowFallsThrough(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := Paths{DataDir: filepath.Join(dir, "data"), CacheDir: filepath.Join(dir, "cache")}
	db := newOpenCodeDB(t, filepath.Join(p.DataDir, "opencode.db"))
	require.NoError(t, db.Close())
	writeFile(t, filepath.Join(p.CacheDir, "models.json"), catalogJSON("from-v1"))

	cat, err := LoadCatalog(context.Background(), p, nil)
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "from-v1")
}

func TestLoadCatalogChannelAndEnvDB(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := Paths{DataDir: dir}
	older := filepath.Join(dir, "opencode-beta.db")
	newer := filepath.Join(dir, "opencode-dev.db")
	db := newOpenCodeDB(t, older)
	putCatalog(t, db, catalogJSON("beta"), 1)
	require.NoError(t, db.Close())
	db = newOpenCodeDB(t, newer)
	putCatalog(t, db, catalogJSON("dev"), 2)
	require.NoError(t, db.Close())
	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(older, past, past))

	cat, err := LoadCatalog(context.Background(), p, nil)
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "dev")

	// OPENCODE_DB is resolved against the data directory.
	cat, err = LoadCatalog(context.Background(), p, mapEnv(map[string]string{"OPENCODE_DB": "opencode-beta.db"}))
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "beta")

	// opencode.db wins over channel databases.
	db = newOpenCodeDB(t, filepath.Join(dir, "opencode.db"))
	putCatalog(t, db, catalogJSON("main"), 3)
	require.NoError(t, db.Close())
	cat, err = LoadCatalog(context.Background(), p, nil)
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "main")
}

func TestLoadCatalogReadsLiveWALDatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := Paths{DataDir: dir}
	// Keep the writer open, like a running OpenCode, with uncheckpointed
	// data in the WAL.
	db := newOpenCodeDB(t, filepath.Join(dir, "opencode.db"), `PRAGMA wal_autocheckpoint = 0`)
	putCatalog(t, db, catalogJSON("live"), 5)

	cat, err := LoadCatalog(context.Background(), p, nil)
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "live")

	// The WAL path (mode=ro) is read-only too.
	err = withReadOnlyDB(context.Background(), filepath.Join(dir, "opencode.db"), func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `DELETE FROM kv`)
		return err
	})
	require.Error(t, err)
	var n int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM kv`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestLoadCatalogDoesNotWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	db := newOpenCodeDB(t, dbPath)
	putCatalog(t, db, catalogJSON("quiet"), 5)
	require.NoError(t, db.Close())

	before := dirState(t, dir)
	cat, err := LoadCatalog(context.Background(), Paths{DataDir: dir}, nil)
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "quiet")
	_, err = LoadCredentials(context.Background(), Paths{DataDir: dir}, nil)
	require.NoError(t, err)
	require.Equal(t, before, dirState(t, dir))
}

func dirState(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	state := map[string]time.Time{}
	for _, e := range entries {
		info, err := e.Info()
		require.NoError(t, err)
		state[e.Name()] = info.ModTime()
	}
	return state
}

func TestLoadCatalogErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, `not json`)

	_, err := LoadCatalog(context.Background(), Paths{}, mapEnv(map[string]string{"OPENCODE_MODELS_PATH": bad}))
	require.Error(t, err)

	// A broken source does not hide a good one further down.
	writeFile(t, filepath.Join(dir, "cache", "models.json"), catalogJSON("ok"))
	cat, err := LoadCatalog(context.Background(), Paths{CacheDir: filepath.Join(dir, "cache")}, mapEnv(map[string]string{"OPENCODE_MODELS_PATH": bad}))
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "ok")
}

func TestParseCatalog(t *testing.T) {
	t.Parallel()

	raw := `{
		"acme": {
			"id": "acme", "name": "Acme", "env": ["ACME_API_KEY"], "npm": "@ai-sdk/openai-compatible",
			"api": "https://api.acme.dev/v1", "doc": "https://docs.acme.dev",
			"models": {
				"big": {
					"id": "big", "name": "Big", "family": "acme", "attachment": true, "reasoning": true,
					"tool_call": true, "temperature": false, "release_date": "2026-05-01", "last_updated": "2026-06-01",
					"modalities": {"input": ["text", "image"], "output": ["text"]}, "open_weights": false,
					"cost": {"input": 1.5, "output": 6, "cache_read": 0.15, "cache_write": 1.875, "tiers": [{"input": 3, "output": 9, "tier": {"type": "context", "size": 200000}}]},
					"limit": {"context": 200000, "input": 180000.0, "output": 32000},
					"reasoning_options": [{"type": "toggle"}, {"type": "effort", "values": [null, "low", "null", "high"]}, {"type": "budget_tokens", "min": 1024, "max": 32768}],
					"status": "beta", "interleaved": {"field": "reasoning_content"},
					"provider": {"npm": "@ai-sdk/anthropic", "api": "https://api.acme.dev/anthropic", "shape": "completions"}
				},
				"broken": {"id": "broken", "limit": "nope"}
			}
		},
		"broken-provider": ["not", "an", "object"]
	}`
	md, err := ParseCatalog([]byte(raw))
	require.NoError(t, err)
	require.Equal(t, []string{"acme"}, providerIDs(md))

	acme := md["acme"]
	require.Equal(t, "https://api.acme.dev/v1", acme.API)
	require.Equal(t, []string{"big"}, slices.Sorted(func(yield func(string) bool) {
		for k := range acme.Models {
			if !yield(k) {
				return
			}
		}
	}))
	f := false
	minTokens, maxTokens := int64(1024), int64(32768)
	require.Equal(t, ModelsDevModel{
		ID: "big", Name: "Big", Family: "acme", Attachment: true, Reasoning: true, ToolCall: true,
		Temperature: &f, ReleaseDate: "2026-05-01", LastUpdated: "2026-06-01",
		Modalities: ModelsDevModalities{Input: []string{"text", "image"}, Output: []string{"text"}},
		Cost:       &ModelsDevCost{Input: 1.5, Output: 6, CacheRead: 0.15, CacheWrite: 1.875},
		Limit:      ModelsDevLimit{Context: 200000, Input: 180000, Output: 32000},
		ReasoningOptions: []ModelsDevReasoningOption{
			{Type: "toggle"},
			{Type: "effort", Values: []string{"low", "high"}},
			{Type: "budget_tokens", Min: &minTokens, Max: &maxTokens},
		},
		Status:   "beta",
		Provider: &ModelsDevModelProvider{NPM: "@ai-sdk/anthropic", API: "https://api.acme.dev/anthropic", Shape: "completions"},
	}, acme.Models["big"])

	_, err = ParseCatalog([]byte(`[1, 2]`))
	require.Error(t, err)
	_, err = ParseCatalog([]byte(`null`))
	require.Error(t, err)
}

func TestReadOnlyDSN(t *testing.T) {
	t.Parallel()

	// Create the database under a plain name (the test helper passes the
	// path straight to the driver), then move it somewhere URI-hostile.
	root := t.TempDir()
	plain := filepath.Join(root, "plain")
	db := newOpenCodeDB(t, filepath.Join(plain, "opencode.db"))
	putCatalog(t, db, catalogJSON("odd"), 1)
	require.NoError(t, db.Close())
	dir := filepath.Join(root, "we?ird #dir %20")
	require.NoError(t, os.Rename(plain, dir))
	dbPath := filepath.Join(dir, "opencode.db")

	cat, err := LoadCatalog(context.Background(), Paths{DataDir: dir}, nil)
	require.NoError(t, err)
	require.Contains(t, cat.Providers, "odd")

	// The connection really is read-only.
	err = withReadOnlyDB(context.Background(), dbPath, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `DELETE FROM kv`)
		return err
	})
	require.Error(t, err)
}

func TestCatalogLocations(t *testing.T) {
	t.Parallel()

	data := t.TempDir()
	p := Paths{DataDir: data, CacheDir: "/cache"}
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}

	require.Equal(t, []string{
		filepath.Join(data, "opencode.db"),
		filepath.Join("/cache", "models.json"),
	}, CatalogLocations(p, nil))

	require.Equal(t, []string{
		"/override/api.json",
		filepath.Join(data, "custom.db"),
		filepath.Join("/cache", "models.json"),
	}, CatalogLocations(p, env(map[string]string{
		"OPENCODE_MODELS_PATH": "/override/api.json",
		"OPENCODE_DB":          "custom.db",
	})))

	require.Equal(t, []string{
		filepath.Join(data, "opencode.db"),
	}, CatalogLocations(Paths{DataDir: data}, env(map[string]string{
		"OPENCODE_DB": ":memory:",
	})))

	require.Empty(t, CatalogLocations(Paths{}, nil))
}
