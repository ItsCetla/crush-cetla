// Package workdir carries a session's working folder through a context,
// so tools and permission checks can work in the folder of the tab that
// started the call instead of the folder Ta-Da was launched in.
package workdir

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type ctxKey struct{}

// With returns a context that carries dir as the working folder. An
// empty dir leaves ctx as it is.
func With(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, filepath.Clean(dir))
}

// From returns the working folder carried by ctx, or fallback when ctx
// carries none.
func From(ctx context.Context, fallback string) string {
	if ctx != nil {
		if dir, ok := ctx.Value(ctxKey{}).(string); ok && dir != "" {
			return dir
		}
	}
	return fallback
}

// Has reports whether ctx carries a working folder.
func Has(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	dir, ok := ctx.Value(ctxKey{}).(string)
	return ok && dir != ""
}

// ErrInvalid is returned for a folder a session cannot work in.
var ErrInvalid = errors.New("invalid working folder")

// Resolve turns what the user typed after /cd into an absolute, clean
// folder path. A leading ~ is the home folder and a relative path is
// taken from base. The folder must exist.
func Resolve(base, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", fmt.Errorf("%w: no folder given", ErrInvalid)
	}
	if arg == "~" || strings.HasPrefix(arg, "~/") || strings.HasPrefix(arg, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("%w: no home folder: %v", ErrInvalid, err)
		}
		arg = filepath.Join(home, arg[1:])
	}
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(base, arg)
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := Check(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// Check reports whether dir can be stored as a session's folder: empty
// (the launch folder) or an absolute path to an existing folder.
func Check(dir string) error {
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%w: %s is not an absolute path", ErrInvalid, dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s does not exist", ErrInvalid, dir)
		}
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a folder", ErrInvalid, dir)
	}
	return nil
}
