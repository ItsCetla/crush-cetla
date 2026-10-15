package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LoadCredentials returns the API keys OpenCode stores, keyed by provider
// ID (models.dev IDs mapped through [CatwalkID]). Only plain API keys are
// returned; OAuth tokens, external credentials and well-known entries are
// never read out.
//
// Sources are OpenCode V1's <data>/auth.json ({id: {type: "api", key}})
// and OpenCode V2's credential table in opencode.db ({type: "key", key}),
// where the active row of each integration wins, else the newest. V2 keys
// win over V1 keys for the same provider. Missing files yield an empty
// map.
func LoadCredentials(ctx context.Context, p Paths, getenv func(string) string) (map[string]string, error) {
	getenv = envOrEmpty(getenv)
	out := map[string]string{}
	var errs []error

	if p.DataDir != "" {
		v1, err := credentialsFromAuthJSON(filepath.Join(p.DataDir, "auth.json"))
		if err != nil {
			errs = append(errs, err)
		}
		for id, key := range v1 {
			out[CatwalkID(id)] = key
		}
	}

	if dbPath := databasePath(p, getenv); dbPath != "" {
		v2, present, err := credentialsFromDB(ctx, dbPath)
		if err != nil {
			errs = append(errs, err)
		}
		// A provider with any V2 credential is governed by V2, even when
		// its active credential is not an API key.
		for id := range present {
			delete(out, CatwalkID(id))
		}
		for id, key := range v2 {
			out[CatwalkID(id)] = key
		}
	}

	return out, errors.Join(errs...)
}

// credentialID reports whether id names a provider. Well-known
// integrations use their origin URL as ID and are skipped.
func credentialID(id string) (string, bool) {
	id = strings.TrimRight(id, "/")
	if id == "" || strings.ContainsAny(id, ":/") {
		return "", false
	}
	return id, true
}

func credentialsFromAuthJSON(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := map[string]string{}
	for rawID, raw := range entries {
		id, ok := credentialID(rawID)
		if !ok {
			continue
		}
		var entry struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		}
		if json.Unmarshal(raw, &entry) != nil || entry.Type != "api" || entry.Key == "" {
			continue
		}
		out[id] = entry.Key
	}
	return out, nil
}

// credentialsFromDB returns the API keys of the active credentials and the
// set of provider IDs that have any credential at all.
func credentialsFromDB(ctx context.Context, path string) (map[string]string, map[string]bool, error) {
	if !fileExists(path) {
		return nil, nil, nil
	}
	out := map[string]string{}
	present := map[string]bool{}
	err := withReadOnlyDB(ctx, path, func(ctx context.Context, db *sql.DB) error {
		clear(out)
		clear(present)
		ok, err := hasTable(ctx, db, "credential")
		if err != nil || !ok {
			return err
		}
		// Mirror Credential.active(): the active row per integration,
		// else the newest one.
		rows, err := db.QueryContext(ctx, `
			SELECT integration_id, value FROM credential
			WHERE integration_id IS NOT NULL
			ORDER BY integration_id, COALESCE(active, 0) DESC, time_created DESC, id DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var rawID, value string
			if err := rows.Scan(&rawID, &value); err != nil {
				return err
			}
			if seen[rawID] {
				continue
			}
			seen[rawID] = true
			id, ok := credentialID(rawID)
			if !ok {
				continue
			}
			present[id] = true
			var cred struct {
				Type string `json:"type"`
				Key  string `json:"key"`
			}
			if json.Unmarshal([]byte(value), &cred) != nil || cred.Type != "key" || cred.Key == "" {
				continue
			}
			out[id] = cred.Key
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read credentials from %s: %w", path, err)
	}
	return out, present, nil
}
