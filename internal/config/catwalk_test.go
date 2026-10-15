package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
	"github.com/stretchr/testify/require"
)

func TestLoadCatwalkCatalog_UsesSavedCatalog(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "providers.json")
	saved := []catwalk.Provider{{Name: "Saved Provider", ID: "saved"}}
	data, err := json.Marshal(saved)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	providers := loadCatwalkCatalog(path)
	require.Len(t, providers, 1)
	require.Equal(t, "Saved Provider", providers[0].Name)
}

func TestLoadCatwalkCatalog_FallsBackToEmbedded(t *testing.T) {
	t.Parallel()

	embeddedCount := len(embedded.GetAll())
	require.Greater(t, embeddedCount, 5, "the release embeds a provider catalog")

	t.Run("no saved catalog", func(t *testing.T) {
		t.Parallel()

		providers := loadCatwalkCatalog(filepath.Join(t.TempDir(), "providers.json"))
		require.Len(t, providers, embeddedCount)
	})

	t.Run("empty saved catalog", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "providers.json")
		require.NoError(t, os.WriteFile(path, []byte("[]"), 0o644))

		providers := loadCatwalkCatalog(path)
		require.Len(t, providers, embeddedCount)
	})

	t.Run("unreadable saved catalog", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "providers.json")
		require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

		providers := loadCatwalkCatalog(path)
		require.Len(t, providers, embeddedCount)
	})
}
