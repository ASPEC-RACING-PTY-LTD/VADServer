// Package release reads VaultDock releases published on GitHub. The server
// uses it for update notices and the desktop app for automatic updates.
package release

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultRepo is the official repository.
const DefaultRepo = "ASPEC-RACING-PTY-LTD/VaultDock"

// ChecksumsAsset is the checksum file attached to every release.
const ChecksumsAsset = "SHA256SUMS"

// ErrNoRelease means the repository has no published release yet.
var ErrNoRelease = errors.New("no release has been published yet")

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Asset is a file attached to a release.
type Asset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"browser_download_url"`
	// Digest is "sha256:<hex>" when GitHub has computed it.
	Digest string `json:"digest"`
}

// Release is the subset of the GitHub release object VaultDock uses.
type Release struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Version is the tag without its leading "v".
func (r *Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Find returns the first asset whose name matches re.
func (r *Release) Find(re *regexp.Regexp) *Asset {
	for i := range r.Assets {
		if re.MatchString(r.Assets[i].Name) {
			return &r.Assets[i]
		}
	}
	return nil
}

// Client talks to the GitHub API.
type Client struct {
	HTTP      *http.Client
	APIBase   string // default https://api.github.com
	UserAgent string
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Latest returns the latest published (non-draft, non-prerelease) release.
func (c *Client) Latest(ctx context.Context, repo string) (*Release, error) {
	if !repoRe.MatchString(repo) {
		return nil, fmt.Errorf("invalid repository %q (expected owner/name)", repo)
	}
	base := c.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNoRelease
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub rate limit reached; will retry later (HTTP %d)", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("release check failed (HTTP %d)", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("release check: %w", err)
	}
	if rel.Tag == "" || rel.Draft {
		return nil, ErrNoRelease
	}
	return &rel, nil
}

// SHA256 returns the expected SHA-256 (lower-case hex) of asset: GitHub's own
// digest when present, otherwise the entry in the release's SHA256SUMS file.
// An asset without a checksum is never trusted.
func (c *Client) SHA256(ctx context.Context, rel *Release, asset *Asset) (string, error) {
	if d, ok := strings.CutPrefix(asset.Digest, "sha256:"); ok && validHex(d) {
		return strings.ToLower(d), nil
	}
	var sums *Asset
	for i := range rel.Assets {
		if rel.Assets[i].Name == ChecksumsAsset {
			sums = &rel.Assets[i]
		}
	}
	if sums == nil {
		return "", fmt.Errorf("release %s publishes no checksum for %s", rel.Tag, asset.Name)
	}
	if err := TrustedDownloadURL(sums.DownloadURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sums.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", ChecksumsAsset, resp.StatusCode)
	}
	sum, err := ParseChecksums(io.LimitReader(resp.Body, 1<<20), asset.Name)
	if err != nil {
		return "", fmt.Errorf("release %s: %w", rel.Tag, err)
	}
	return sum, nil
}

// ParseChecksums finds name in sha256sum output ("<hex>  <name>" or
// "<hex> *<name>").
func ParseChecksums(r io.Reader, name string) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && validHex(f[0]) {
			return strings.ToLower(f[0]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no checksum listed for %s", name)
}

func validHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// TrustedDownloadURL accepts only HTTPS release downloads from GitHub.
func TrustedDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "github.com" || !strings.Contains(u.Path, "/releases/download/") {
		return fmt.Errorf("refusing download from untrusted location %q", raw)
	}
	return nil
}

// TrustedRedirect limits redirects of a release download to GitHub's own
// HTTPS asset hosts. Use it as http.Client.CheckRedirect.
func TrustedRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	h := req.URL.Hostname()
	if req.URL.Scheme != "https" || !(h == "github.com" || strings.HasSuffix(h, ".githubusercontent.com")) {
		return fmt.Errorf("refusing redirect to %s", req.URL.Redacted())
	}
	return nil
}
