package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// InstallerArgs are passed to the Windows Setup to update in place without
// asking anything.
var InstallerArgs = []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/CLOSEAPPLICATIONS"}

// ErrDevBuild means the running copy is a development build, which never
// updates itself.
var ErrDevBuild = errors.New("this is a development build of " + AppName + ", which doesn't update itself; download a release from " + ReleasesPage + " or rebuild from source")

// Result describes a finished install.
type Result struct {
	Kind    Kind
	Version string
	// Path is the replaced executable (archive) or the started installer
	// (windows-installer).
	Path string
	// Background is true when the install finishes in another process
	// after this one exits (windows-installer).
	Background bool
}

// Install downloads, verifies and installs rel for inst.
func (u *Updater) Install(ctx context.Context, rel *Release, inst Install) (Result, error) {
	res := Result{Kind: inst.Kind}
	if inst.Kind == KindDev {
		return res, ErrDevBuild
	}
	if rel == nil {
		return res, errors.New("there is no release to install")
	}
	res.Version = rel.Version()
	asset := PickAsset(rel, inst.GOOS, inst.GOARCH, inst.Kind)
	if asset == nil {
		return res, fmt.Errorf("release %s has no download for %s/%s; see %s", res.Version, inst.GOOS, inst.GOARCH, rel.PageURL())
	}
	if inst.Kind == KindArchive && inst.Exe == "" {
		return res, errors.New("can't find the running executable to replace")
	}

	work, err := os.MkdirTemp("", BinaryName+"-update-")
	if err != nil {
		return res, err
	}
	file, err := u.Download(ctx, rel, asset, work)
	if err != nil {
		_ = os.RemoveAll(work)
		return res, err
	}

	switch inst.Kind {
	case KindWindowsInstaller:
		// The installer keeps running after we exit, so its folder stays.
		start := u.StartDetached
		if start == nil {
			start = startDetachedProcess
		}
		if err := start(file, InstallerArgs); err != nil {
			_ = os.RemoveAll(work)
			return res, fmt.Errorf("starting the installer: %w", err)
		}
		res.Path = file
		res.Background = true
		return res, nil
	default:
		defer os.RemoveAll(work)
		bin := filepath.Join(work, "new-"+binaryName(inst.GOOS))
		if err := ExtractBinary(file, binaryName(inst.GOOS), bin); err != nil {
			return res, err
		}
		if err := ReplaceExecutable(inst.Exe, bin, inst.GOOS); err != nil {
			return res, err
		}
		res.Path = inst.Exe
		return res, nil
	}
}
