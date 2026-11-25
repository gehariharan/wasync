// Package tailnet holds the owner's opt-in for opening the web app from their
// other computers through `tailscale serve`.
//
// wasync itself never listens beyond loopback. `tailscale serve` on the same
// computer accepts HTTPS on the machine's tailnet name and forwards each request
// to the loopback listener, adding the signed-in Tailscale user's login in the
// Tailscale-User-Login header (and removing any copy a client sent). The setting
// names which tailnet host names the web app answers on and which Tailscale
// logins count as the owner. With no setting, nothing changes: only the loopback
// address is served.
//
// Only a human changes it (`wasync tailnet allow` asks for a typed word and the
// system prompt); it is a private file in the data directory with no API route.
package tailnet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

const (
	// FileName is the private setting in the data directory.
	FileName = "tailnet.json"
	// LoginHeader is the header `tailscale serve` sets to the requesting user's
	// login name. Tagged devices and Funnel (public) requests do not get it.
	LoginHeader = "Tailscale-User-Login"
	// FunnelHeader marks a request that arrived from the public internet through
	// Tailscale Funnel. Such requests are always refused.
	FunnelHeader = "Tailscale-Funnel-Request"
	maxEntries   = 16
)

// Setting is the saved opt-in.
type Setting struct {
	Hosts  []string  `json:"hosts"`
	Logins []string  `json:"logins"`
	SetAt  time.Time `json:"set_at"`
}

// Validate checks a setting and returns it with host names in canonical form.
func (s Setting) Validate() (Setting, error) {
	if len(s.Hosts) == 0 || len(s.Logins) == 0 {
		return s, fmt.Errorf("name at least one host and one login")
	}
	if len(s.Hosts) > maxEntries || len(s.Logins) > maxEntries {
		return s, fmt.Errorf("name at most %d hosts and %d logins", maxEntries, maxEntries)
	}
	out := Setting{SetAt: s.SetAt}
	for _, h := range s.Hosts {
		c, err := CanonicalHost(h)
		if err != nil {
			return s, err
		}
		out.Hosts = append(out.Hosts, c)
	}
	for _, l := range s.Logins {
		if err := validLogin(l); err != nil {
			return s, err
		}
		out.Logins = append(out.Logins, l)
	}
	return out, nil
}

// CanonicalHost checks a tailnet host name such as machine.example.ts.net,
// optionally with a port, and returns it in lower case. Addresses, loopback and
// single-label names are refused: the web app is reached through the HTTPS
// name `tailscale serve` has a certificate for.
func CanonicalHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	bad := fmt.Errorf("host %q must be a full tailnet name such as machine.example.ts.net", h)
	name, port := h, ""
	if strings.Contains(h, ":") {
		var err error
		if name, port, err = net.SplitHostPort(h); err != nil {
			return "", bad
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", bad
		}
	}
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 || net.ParseIP(name) != nil || !strings.Contains(name, ".") || name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return "", bad
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", bad
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", bad
			}
		}
	}
	if port != "" {
		return name + ":" + port, nil
	}
	return name, nil
}

func validLogin(l string) error {
	if l == "" || len(l) > 254 || strings.TrimSpace(l) != l {
		return fmt.Errorf("login %q must be a Tailscale login name such as you@example.com", l)
	}
	for _, c := range l {
		if c <= ' ' || c == 0x7f || c == ',' {
			return fmt.Errorf("login %q must be a Tailscale login name such as you@example.com", l)
		}
	}
	return nil
}

// Host returns the configured host a request's Host header names, or "" when
// it names none. A configured name without a port also matches the default
// HTTPS port.
func (s Setting) Host(requestHost string) string {
	got := strings.ToLower(requestHost)
	for _, h := range s.Hosts {
		if got == h || !strings.Contains(h, ":") && got == h+":443" {
			return h
		}
	}
	return ""
}

// Allows reports whether login is one of the owner's Tailscale logins. Login
// names are compared exactly, as Tailscale reports them.
func (s Setting) Allows(login string) bool {
	if login == "" {
		return false
	}
	for _, l := range s.Logins {
		if l == login {
			return true
		}
	}
	return false
}

// Load returns the saved setting, or ok=false when there is none. A damaged or
// non-private file is an error; callers treat that as off.
func Load(dir string) (s Setting, ok bool, err error) {
	if dir == "" {
		return Setting{}, false, nil
	}
	path := filepath.Join(dir, FileName)
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return Setting{}, false, nil
	}
	if err = localfs.CheckPrivateFile(path); err != nil {
		return Setting{}, false, fmt.Errorf("%s must be a regular private file: %w", FileName, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return Setting{}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<10))
	if err != nil {
		return Setting{}, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&s); err != nil {
		return Setting{}, false, fmt.Errorf("invalid %s: %w", FileName, err)
	}
	if s, err = s.Validate(); err != nil {
		return Setting{}, false, fmt.Errorf("invalid %s: %w", FileName, err)
	}
	return s, true, nil
}

// Save writes the setting atomically as a private file.
func Save(dir string, s Setting) (result error) {
	s, err := s.Validate()
	if err != nil {
		return err
	}
	if err = localfs.EnsureDir(dir); err != nil {
		return err
	}
	s.SetAt = time.Now().UTC().Truncate(time.Second)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+FileName+".tmp")
	f, err := localfs.OpenFile(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			os.Remove(tmp)
		}
	}()
	if err = f.Truncate(0); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, FileName))
}

// Remove turns the opt-in off. Removing a setting that does not exist is not an error.
func Remove(dir string) error {
	err := os.Remove(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
