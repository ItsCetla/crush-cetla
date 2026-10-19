package update

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrNotWritable means the executable's folder can't be written by this
// user, so the update can't be put in place.
type ErrNotWritable struct {
	Dir string
	Err error
}

func (e *ErrNotWritable) Error() string {
	return fmt.Sprintf("can't write to %s (%v). If %s was installed there by an administrator, rerun the update with sudo (or as an administrator), or download the new version from %s",
		e.Dir, e.Err, AppName, ReleasesPage)
}

func (e *ErrNotWritable) Unwrap() error { return e.Err }

// oldSuffix is appended to a replaced Windows executable, which can't be
// deleted while it runs; the next start removes it.
const oldSuffix = ".old"

// ReplaceExecutable puts the file at newBin in place of target. On Unix it
// writes a temporary copy next to target and renames it over target, which
// is atomic and safe while target is running. On Windows (goos "windows")
// the running file can't be overwritten, so target is first renamed to
// target.old, then the new file is moved in; CleanupOld removes the .old
// file on the next start.
func ReplaceExecutable(target, newBin, goos string) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".update-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return &ErrNotWritable{Dir: dir, Err: err}
		}
		return fmt.Errorf("preparing the update: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	src, err := os.Open(newBin)
	if err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	_, err = io.Copy(tmp, src)
	src.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmpPath, 0o755)
	}
	if err != nil {
		cleanup()
		return fmt.Errorf("preparing the update: %w", err)
	}

	if goos != "windows" {
		if err := os.Rename(tmpPath, target); err != nil {
			cleanup()
			if errors.Is(err, fs.ErrPermission) {
				return &ErrNotWritable{Dir: dir, Err: err}
			}
			return fmt.Errorf("installing the update: %w", err)
		}
		return nil
	}

	old := target + oldSuffix
	_ = os.Remove(old) // a leftover from an earlier update
	if err := os.Rename(target, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		cleanup()
		if errors.Is(err, fs.ErrPermission) {
			return &ErrNotWritable{Dir: dir, Err: err}
		}
		return fmt.Errorf("moving the old version aside: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Rename(old, target) // put the old version back
		cleanup()
		return fmt.Errorf("installing the update: %w", err)
	}
	return nil
}

// CleanupOld removes the exe.old file a Windows update leaves next to exe.
// Errors are ignored: the file may still be in use or already gone.
func CleanupOld(exe string) {
	if exe == "" {
		return
	}
	_ = os.Remove(exe + oldSuffix)
}
