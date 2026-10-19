package update

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// CheckTimeout bounds a release check.
	CheckTimeout = 20 * time.Second
	// DownloadTimeout bounds a whole asset download.
	DownloadTimeout = 15 * time.Minute

	maxReleaseJSON = 4 << 20   // the release JSON
	maxSumsFile    = 1 << 20   // SHA256SUMS.txt
	maxDownload    = 512 << 20 // any downloaded asset
)

// Release is the part of a GitHub release this package uses.
type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	HTMLURL     string  `json:"html_url"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
}

// Version is the release's version number without a leading "v".
func (r *Release) Version() string { return trimV(r.TagName) }

// PageURL is the release's web page, or the releases page.
func (r *Release) PageURL() string {
	if r == nil {
		return ReleasesPage
	}
	return cmp.Or(r.HTMLURL, ReleasesPage)
}

// Asset is a file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	// Digest is GitHub's checksum of the file, "sha256:<hex>", when known.
	Digest string `json:"digest"`
}

// CheckResult is the outcome of a release check.
type CheckResult struct {
	Current string
	// Latest is the latest release's version, or "" when there is no
	// release yet.
	Latest string
	// URL is the latest release's page.
	URL string
	// Newer reports whether Latest is newer than Current.
	Newer bool
	// Release is the latest release, or nil when there is none.
	Release *Release
}

// Updater checks for and installs releases. The zero value is not usable;
// create one with New.
type Updater struct {
	// Current is the running version.
	Current string
	// HTTP is the client used for every request.
	HTTP *http.Client
	// APIURL is the latest-release endpoint.
	APIURL string
	// AllowHost decides which hosts may be contacted (also on redirects).
	// nil allows any host; tests use that with httptest servers.
	AllowHost func(host string) bool
	// StartDetached starts a program that outlives this process (the
	// Windows installer). nil uses the real thing; tests stub it.
	StartDetached func(name string, args []string) error
}

// New returns an Updater for the running version that only talks to GitHub.
func New(current string) *Updater {
	u := &Updater{
		Current:   current,
		APIURL:    LatestReleaseURL,
		AllowHost: GitHubHost,
	}
	u.HTTP = &http.Client{CheckRedirect: u.checkRedirect}
	return u
}

// GitHubHost reports whether host is one of the GitHub hosts updates use:
// api.github.com, github.com and GitHub's release asset storage on
// *.githubusercontent.com.
func GitHubHost(host string) bool {
	host = strings.ToLower(host)
	return host == "api.github.com" || host == "github.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
}

func (u *Updater) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	return u.allowed(req.URL)
}

func (u *Updater) allowed(target *url.URL) error {
	if u.AllowHost == nil {
		return nil
	}
	if target.Scheme != "https" || !u.AllowHost(target.Hostname()) {
		return fmt.Errorf("refusing to contact %s: updates only talk to GitHub", target.Host)
	}
	return nil
}

func (u *Updater) userAgent() string {
	return "Tada/" + cmp.Or(u.Current, "unknown")
}

func (u *Updater) get(ctx context.Context, rawURL string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := u.allowed(req.URL); err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", u.userAgent())
	client := u.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

// Latest fetches the latest published release. It returns nil and no error
// when the repository has no release yet (GitHub answers 404).
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	res, err := u.get(ctx, u.APIURL, http.Header{
		"Accept":               {"application/vnd.github+json"},
		"X-Github-Api-Version": {"2022-11-28"},
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("the update check timed out")
		}
		return nil, fmt.Errorf("checking for updates: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checking for updates: GitHub answered %s", res.Status)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(res.Body, maxReleaseJSON)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("reading the latest release: %w", err)
	}
	if !ValidVersion(rel.Version()) {
		return nil, fmt.Errorf("the latest release (%q) has no version number", rel.TagName)
	}
	return &rel, nil
}

// Check fetches the latest release and compares it with the running version.
func (u *Updater) Check(ctx context.Context) (CheckResult, error) {
	res := CheckResult{Current: u.Current, URL: ReleasesPage}
	rel, err := u.Latest(ctx)
	if err != nil || rel == nil {
		return res, err
	}
	res.Release = rel
	res.Latest = rel.Version()
	res.URL = rel.PageURL()
	res.Newer = CompareVersions(res.Latest, u.Current) > 0
	return res, nil
}
