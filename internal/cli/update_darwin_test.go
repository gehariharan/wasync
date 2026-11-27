//go:build darwin

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/update"
)

type yesPrompt struct{ asked []string }

func (*yesPrompt) Name() string    { return "the test prompt" }
func (*yesPrompt) Available() bool { return true }
func (p *yesPrompt) Verify(_ context.Context, reason string) error {
	p.asked = append(p.asked, reason)
	return nil
}

// fakeBundle writes a minimal app whose Contents/v says which version it is.
func fakeBundle(t *testing.T, app, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"MacOS/wasync": string(exe),
		"Info.plist":   `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.example.wasync-update-test</string><key>CFBundleExecutable</key><string>wasync</string></dict></plist>`,
		"v":            version,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(app, "Contents", name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// trustingRunner runs the Mac's real tools, except that it reports the
// signature checks as passed: a test cannot be notarized by wasync's team.
// Everything else (the download, checksum, disk image, copy, swap, helper
// and owner hand-over) is real.
func trustingRunner(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, error) {
	switch {
	case name == "codesign" && len(args) > 0 && args[0] == "-dv":
		return nil, []byte("TeamIdentifier=ABCDE12345\n"), nil
	case name == "codesign", name == "spctl":
		return nil, nil, nil
	}
	return update.ExecRunner(ctx, name, args, env)
}

func TestOneClickUpdateEndToEnd(t *testing.T) {
	oldVersion := buildinfo.Version
	buildinfo.Version = "0.1.0-preview.1"
	t.Cleanup(func() { buildinfo.Version = oldVersion })

	base := t.TempDir()
	installed := filepath.Join(base, "Applications", "wasync.app")
	fakeBundle(t, installed, "old")
	src := filepath.Join(base, "image")
	fakeBundle(t, filepath.Join(src, "wasync.app"), "new")
	dmgName := "wasync_0.1.0-preview.2_darwin_" + runtime.GOARCH + ".dmg"
	dmg := filepath.Join(base, dmgName)
	if out, err := exec.Command("hdiutil", "create", "-quiet", "-srcfolder", src, "-volname", "wasync", "-format", "UDZO", dmg).CombinedOutput(); err != nil {
		t.Skipf("hdiutil cannot make disk images here: %v %s", err, out)
	}
	image, err := os.ReadFile(dmg)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	checksums := hex.EncodeToString(sum[:]) + "  ./" + dmgName + "\n"

	// GitHub, as far as wasync can tell: the release list and its files.
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v0.1.0-preview.2","html_url":"https://github.com/gehariharan/wasync/releases/tag/v0.1.0-preview.2","assets":[{"name":%q,"browser_download_url":"%s/dl/%s","size":%d},{"name":"checksums.txt","browser_download_url":"%s/dl/checksums.txt","size":%d}]}]`,
				dmgName, gh.URL, dmgName, len(image), gh.URL, len(checksums))
		case "/dl/" + dmgName:
			w.Write(image)
		case "/dl/checksums.txt":
			io.WriteString(w, checksums)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()

	dir := filepath.Join(base, "data")
	api := "127.0.0.1:" + freePort(t)
	platform := update.Mac{Run: trustingRunner}
	helperDone := make(chan error, 1)
	prompt := &yesPrompt{}
	owner := &App{
		Out: io.Discard, Err: io.Discard, Human: humancheck.NewGate(prompt),
		Executable:     func() (string, error) { return filepath.Join(installed, "Contents", "MacOS", "wasync"), nil },
		UpdateSource:   update.GitHub{ListURL: gh.URL + "/releases"},
		UpdatePlatform: platform,
		// The helper, started as wasync starts it, but in this process.
		UpdateLaunch: func(dataDir string) error {
			helper := &App{Out: io.Discard, Err: io.Discard, UpdatePlatform: platform}
			go func() {
				helperDone <- helper.Run(context.Background(), []string{"--data-dir", dataDir, "update-apply"})
			}()
			return nil
		},
	}
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- owner.Run(context.Background(), []string{"--data-dir", dir, "--api-addr", api, "serve", "--offline"})
	}()
	waitForAPI(t, api, ownerDone)

	// Sign in to the web app the way `wasync web` does.
	secret, err := approvecred.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := commandRequest(context.Background(), config.Config{APIAddr: api}, "POST", "/api/ui/link", []byte(`{"page":"setup"}`), []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	var link struct{ URL string }
	json.NewDecoder(res.Body).Decode(&link)
	res.Body.Close()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	page := get(t, browser, link.URL)
	i := strings.Index(page, `name="csrf" value="`)
	if i < 0 || !strings.Contains(page, `id="update"`) {
		t.Fatalf("setup page:\n%s", page)
	}
	csrf := page[i+len(`name="csrf" value="`):]
	csrf = csrf[:strings.Index(csrf, `"`)]

	var status update.Status
	post(t, browser, "http://"+api+"/ui/update/check", csrf, &status)
	if status.State != update.StateAvailable || !status.CanInstall || status.Latest.Asset != dmgName {
		t.Fatalf("check: %+v", status)
	}
	post(t, browser, "http://"+api+"/ui/update/start", csrf, &status)
	if len(prompt.asked) != 1 || !strings.Contains(prompt.asked[0], "from 0.1.0-preview.1 to 0.1.0-preview.2") {
		t.Fatalf("prompts %v", prompt.asked)
	}

	select {
	case err := <-ownerDone:
		if err != nil {
			t.Fatalf("wasync did not stop cleanly for the update: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("wasync did not stop for the update")
	}
	select {
	case err := <-helperDone:
		if err != nil {
			t.Fatalf("helper: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the helper did not finish")
	}
	if v, _ := os.ReadFile(filepath.Join(installed, "Contents", "v")); string(v) != "new" {
		t.Fatalf("installed app is %q", v)
	}
	if entries, _ := os.ReadDir(filepath.Dir(installed)); len(entries) != 1 {
		t.Fatalf("left beside the app: %v", entries)
	}
	result, err := update.LastResult(dir)
	if err != nil || result == nil || !result.OK || result.To != "0.1.0-preview.2" || !strings.Contains(result.Restart, "background") {
		t.Fatalf("result %+v %v", result, err)
	}
	entries, _ := os.ReadDir(update.Folder(dir))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "staging-") || e.Name() == "plan.json" {
			t.Fatalf("left behind %s", e.Name())
		}
	}
}

func waitForAPI(t *testing.T, addr string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("wasync exited: %v", err)
		default:
		}
		if res, err := http.Get("http://" + addr + "/health"); err == nil {
			res.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the API did not start")
}

func get(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func post(t *testing.T, c *http.Client, url, csrf string, out any) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, nil)
	req.Header.Set("X-Wasync-CSRF", csrf)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		t.Fatalf("POST %s: %d %s", url, res.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("POST %s: %v %s", url, err, b)
	}
}
