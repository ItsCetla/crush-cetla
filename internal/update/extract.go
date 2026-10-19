package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// maxBinary caps the size of the extracted executable, guarding against
// decompression bombs.
const maxBinary = 256 << 20

// ErrBinaryNotFound means the archive has no executable to install.
var ErrBinaryNotFound = errors.New("the release archive has no " + BinaryName + " executable")

// safeEntryName reports whether an archive entry name stays inside the
// archive: relative, no "..", no drive letters or backslash tricks.
func safeEntryName(name string) bool {
	if name == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") {
		return false
	}
	if path.IsAbs(name) {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// isBinaryEntry reports whether an entry is the executable: bin at the top
// level or inside the archive's single wrapping directory.
func isBinaryEntry(name, bin string) bool {
	clean := path.Clean(name)
	if path.Base(clean) != bin {
		return false
	}
	return strings.Count(clean, "/") <= 1
}

// ExtractBinary copies the executable bin (e.g. "tada" or "tada.exe") out of
// a .tar.gz or .zip release archive into dest, mode 0755. The whole archive
// is refused when any entry tries to escape it.
func ExtractBinary(archive, bin, dest string) error {
	if strings.HasSuffix(strings.ToLower(archive), ".zip") {
		return extractZip(archive, bin, dest)
	}
	return extractTarGz(archive, bin, dest)
}

func extractTarGz(archive, bin, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading the release archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the release archive: %w", err)
		}
		if !safeEntryName(hdr.Name) || (hdr.Linkname != "" && !safeEntryName(hdr.Linkname)) {
			return fmt.Errorf("the release archive has an unsafe entry %q", hdr.Name)
		}
		if found || hdr.Typeflag != tar.TypeReg || !isBinaryEntry(hdr.Name, bin) {
			continue
		}
		if hdr.Size > maxBinary {
			return fmt.Errorf("the executable in the release archive is too large (%d bytes)", hdr.Size)
		}
		if err := writeBinary(tr, dest); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return ErrBinaryNotFound
	}
	return nil
}

func extractZip(archive, bin, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("reading the release archive: %w", err)
	}
	defer zr.Close()
	var entry *zip.File
	for _, zf := range zr.File {
		if !safeEntryName(zf.Name) {
			return fmt.Errorf("the release archive has an unsafe entry %q", zf.Name)
		}
		if entry == nil && zf.Mode().IsRegular() && isBinaryEntry(zf.Name, bin) {
			entry = zf
		}
	}
	if entry == nil {
		return ErrBinaryNotFound
	}
	if entry.UncompressedSize64 > maxBinary {
		return fmt.Errorf("the executable in the release archive is too large (%d bytes)", entry.UncompressedSize64)
	}
	rc, err := entry.Open()
	if err != nil {
		return fmt.Errorf("reading the release archive: %w", err)
	}
	defer rc.Close()
	return writeBinary(rc, dest)
}

// writeBinary writes at most maxBinary bytes from r to dest.
func writeBinary(r io.Reader, dest string) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxBinary+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxBinary {
		err = errors.New("the executable in the release archive is too large")
	}
	if err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("extracting the executable: %w", err)
	}
	return os.Chmod(dest, 0o755)
}
