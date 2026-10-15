package config

import (
	"errors"
	"io/fs"
	"log/slog"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
)

// loadCatwalkCatalog returns the Catwalk provider catalog saved at path by
// `crush update-providers`, or the catalog embedded in this release when
// there is no usable saved copy. It never fetches anything.
//
// An unreadable saved catalog is logged and replaced by the embedded one
// rather than reported: the embedded catalog is a sound answer, and the user
// can rerun `crush update-providers` to fix the file.
func loadCatwalkCatalog(path string) []catwalk.Provider {
	saved, err := newCache[[]catwalk.Provider](path).Get()
	switch {
	case err == nil && len(saved) > 0:
		slog.Debug("Using saved Catwalk providers", "path", path)
		return saved
	case err == nil:
		slog.Warn("Saved Catwalk provider catalog is empty, using the embedded one", "path", path)
	case !errors.Is(err, fs.ErrNotExist):
		slog.Warn("Could not read the saved Catwalk provider catalog, using the embedded one", "path", path, "error", err)
	}
	slog.Debug("Using embedded Catwalk providers")
	return embedded.GetAll()
}
