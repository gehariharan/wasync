package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// releasesURL lists the newest releases. /releases/latest skips pre-releases,
// and every current wasync release is one, so the list is read instead (as
// scripts/install-windows.ps1 does).
const releasesURL = "https://api.github.com/repos/gehariharan/wasync/releases?per_page=10"

const (
	// ChecksumsAsset is the release's SHA-256 list (see .github/workflows/release.yml).
	ChecksumsAsset = "checksums.txt"
	maxListBytes   = 4 << 20
	maxSumsBytes   = 1 << 20
	// maxAssetBytes bounds a download; installers are tens of megabytes.
	maxAssetBytes = 1 << 30
	userAgent     = "wasync-update"
)

// Release is one published wasync release.
type Release struct {
	Tag         string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PageURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Version is the release's version without the leading "v".
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
	// Digest is GitHub's own "sha256:HEX" for the file, when it reports one.
	Digest string `json:"digest"`
}

// Find returns the asset called name.
func (r Release) Find(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Source reads published releases and their files.
type Source interface {
	// Latest returns the newest published release, or nil when there is none.
	Latest(ctx context.Context) (*Release, error)
	// Fetch writes the file at rawURL to w, at most limit bytes, reporting
	// how many bytes have arrived so far.
	Fetch(ctx context.Context, rawURL string, w io.Writer, limit int64, progress func(int64)) error
}

// GitHub reads releases from GitHub's public API. It sends no credentials and
// nothing about the person: one request lists releases, the others download.
type GitHub struct {
	// HTTP is the client to use; nil uses one with sensible timeouts.
	HTTP *http.Client
	// ListURL is the release list to read; "" uses the published one.
	ListURL string
}

func (g GitHub) listURL() string {
	if g.ListURL != "" {
		return g.ListURL
	}
	return releasesURL
}

// loopback reports whether u is plain HTTP to a literal loopback address,
// which only a test server uses.
func loopback(u *url.URL) bool {
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && ip != nil && ip.IsLoopback()
}

// allowed says whether wasync may download from u. Release files live on
// github.com and redirect to GitHub's download hosts; a local test server may
// serve its own files only.
func (g GitHub) allowed(u *url.URL) bool {
	list, err := url.Parse(g.listURL())
	if err != nil {
		return false
	}
	if loopback(list) {
		return loopback(u) && u.Host == list.Host
	}
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "github.com" || host == "api.github.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

func (g GitHub) client() *http.Client {
	c := g.HTTP
	if c == nil {
		c = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		}}
	}
	copied := *c
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if !g.allowed(req.URL) {
			return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Host)
		}
		return nil
	}
	return &copied
}

func (g GitHub) get(ctx context.Context, rawURL, accept string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !g.allowed(u) {
		return nil, fmt.Errorf("refusing to download from %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", u.Host, resp.Status)
	}
	return resp, nil
}

// Latest returns the newest published (not draft) release whose tag is a
// wasync version, or nil when there is none.
func (g GitHub) Latest(ctx context.Context) (*Release, error) {
	resp, err := g.get(ctx, g.listURL(), "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("could not reach GitHub to check for updates: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not read the release list: %w", err)
	}
	if len(body) > maxListBytes {
		return nil, errors.New("the release list is unexpectedly large")
	}
	var list []Release
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("could not read the release list: %w", err)
	}
	return newest(list), nil
}

// newest picks the latest published release with a "vVERSION" tag. Other tags
// in the same repository (such as axi/vVERSION) are not wasync releases.
func newest(list []Release) *Release {
	var best *Release
	for i := range list {
		r := &list[i]
		if r.Draft || !strings.HasPrefix(r.Tag, "v") || !ValidVersion(r.Tag) {
			continue
		}
		if best == nil || Newer(r.Version(), best.Version()) {
			best = r
		}
	}
	return best
}

// Fetch downloads rawURL into w.
func (g GitHub) Fetch(ctx context.Context, rawURL string, w io.Writer, limit int64, progress func(int64)) error {
	resp, err := g.get(ctx, rawURL, "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength > limit {
		return fmt.Errorf("the download is larger than expected (%d bytes)", resp.ContentLength)
	}
	var done int64
	buf := make([]byte, 64<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > limit {
				return errors.New("the download is larger than expected")
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if progress != nil {
				progress(done)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// fetchChecksums downloads and reads a release's checksums.txt.
func fetchChecksums(ctx context.Context, src Source, r Release) (map[string]string, error) {
	a, ok := r.Find(ChecksumsAsset)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s, so a download cannot be checked", r.Tag, ChecksumsAsset)
	}
	var buf bytes.Buffer
	if err := src.Fetch(ctx, a.URL, &buf, maxSumsBytes, nil); err != nil {
		return nil, fmt.Errorf("could not download %s: %w", ChecksumsAsset, err)
	}
	return parseChecksums(buf.Bytes()), nil
}

// parseChecksums reads `sha256sum` output: "HEX  NAME" or "HEX *NAME", where
// the release workflow writes names as ./NAME.
func parseChecksums(b []byte) map[string]string {
	sums := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		sum := strings.ToLower(fields[0])
		if raw, err := hex.DecodeString(sum); err != nil || len(raw) != sha256.Size {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")
		sums[name] = sum
	}
	return sums
}

// checkDigest compares a downloaded file's SHA-256 with the release's list and,
// when GitHub reports one, GitHub's own digest of the asset.
func checkDigest(got string, a Asset, sums map[string]string) error {
	want, ok := sums[a.Name]
	if !ok {
		return fmt.Errorf("%s does not list %s, so it cannot be checked", ChecksumsAsset, a.Name)
	}
	if got != want {
		return fmt.Errorf("the download does not match its published checksum (expected %s, got %s)", want, got)
	}
	if d, ok := strings.CutPrefix(strings.ToLower(a.Digest), "sha256:"); ok && d != got {
		return fmt.Errorf("the download does not match GitHub's digest for it (expected %s, got %s)", d, got)
	}
	return nil
}
