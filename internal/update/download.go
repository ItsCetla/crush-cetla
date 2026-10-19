package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SumsFile is the checksum file GoReleaser attaches to each release.
const SumsFile = "SHA256SUMS.txt"

var hexSHA256Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ErrNoChecksum means the release lists no SHA-256 for the file, so it is
// not installed.
var ErrNoChecksum = errors.New("the release lists no checksum for this file, so it won't be installed")

// ErrChecksumMismatch means the download doesn't match its checksum.
var ErrChecksumMismatch = errors.New("the download is damaged (its SHA-256 doesn't match the release); please try again")

// digestSHA256 returns the hex SHA-256 from a GitHub asset digest
// ("sha256:<hex>"), or "".
func digestSHA256(digest string) string {
	algo, sum, ok := strings.Cut(strings.TrimSpace(digest), ":")
	if !ok || !strings.EqualFold(algo, "sha256") || !hexSHA256Re.MatchString(sum) {
		return ""
	}
	return strings.ToLower(sum)
}

// ParseSums finds name in a SHA256SUMS.txt ("<hex>  <name>" lines, with an
// optional "*" before binary file names) and returns its hex SHA-256, or "".
func ParseSums(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		hash, file := fields[0], strings.TrimPrefix(fields[1], "*")
		if file == name && hexSHA256Re.MatchString(hash) {
			return strings.ToLower(hash)
		}
	}
	return ""
}

// ExpectedSHA256 returns the SHA-256 the release lists for asset: GitHub's
// own digest when present, else the line for it in the release's
// SHA256SUMS.txt. It returns ErrNoChecksum when there is neither.
func (u *Updater) ExpectedSHA256(ctx context.Context, rel *Release, asset *Asset) (string, error) {
	if sum := digestSHA256(asset.Digest); sum != "" {
		return sum, nil
	}
	var sumsAsset *Asset
	for i := range rel.Assets {
		if rel.Assets[i].Name == SumsFile {
			sumsAsset = &rel.Assets[i]
			break
		}
	}
	if sumsAsset == nil {
		return "", ErrNoChecksum
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	res, err := u.get(ctx, sumsAsset.BrowserDownloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", SumsFile, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", SumsFile, res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxSumsFile))
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", SumsFile, err)
	}
	if sum := ParseSums(string(body), asset.Name); sum != "" {
		return sum, nil
	}
	return "", ErrNoChecksum
}

// Download fetches asset into dir and verifies it against the release's
// checksum. It returns the file's path. A file that doesn't match is
// deleted.
func (u *Updater) Download(ctx context.Context, rel *Release, asset *Asset, dir string) (string, error) {
	expected, err := u.ExpectedSHA256(ctx, rel, asset)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	res, err := u.get(ctx, asset.BrowserDownloadURL, http.Header{"Accept": {"application/octet-stream"}})
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", asset.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", asset.Name, res.Status)
	}

	path := filepath.Join(dir, filepath.Base(asset.Name))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDownload {
		err = fmt.Errorf("downloading %s: the file is larger than %d MB", asset.Name, maxDownload>>20)
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		_ = os.Remove(path)
		return "", ErrChecksumMismatch
	}
	return path, nil
}
