package webhook

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "::1", "0.0.0.0", "::",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", "fd00::1", "fc00::1",
		"169.254.169.254", "169.254.0.1", "fe80::1", "fd00:ec2::254",
		"100.64.0.1", "100.127.255.255",
		"192.0.0.8", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "192.88.99.1",
		"224.0.0.1", "239.255.255.255", "ff02::1", "255.255.255.255", "240.0.0.1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::7f00:1", "2002:7f00:1::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001:db8::1", "100::1",
		"fe80::1%eth0",
	}
	for _, s := range blocked {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if !blockedIP(ip) {
			t.Errorf("%s was not blocked", s)
		}
	}
	for _, s := range []string{"93.184.216.34", "8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "172.15.0.1", "172.32.0.1", "2606:2800:220:1:248:1893:25c8:1946", "2001:4860:4860::8888", "::ffff:8.8.8.8"} {
		if blockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s was blocked", s)
		}
	}
}

func TestValidateURL(t *testing.T) {
	for _, ok := range []string{"https://example.com/hook", "https://hooks.example.com:8443/a/b?x=1", "https://93.184.216.34/hook", "https://[2606:2800:220:1:248:1893:25c8:1946]/h"} {
		if _, err := ValidateURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	bad := map[string]string{
		"http://example.com/hook":                          "https",
		"ftp://example.com/":                               "https",
		"//example.com/hook":                               "https",
		"https:///hook":                                    "host",
		"https://user:pw@example.com/":                     "credentials",
		"https://example.com/#frag":                        "fragment",
		"https://localhost/hook":                           "local name",
		"https://api.localhost/hook":                       "local name",
		"https://printer.local/hook":                       "local name",
		"https://metadata.internal/hook":                   "local name",
		"https://intranet/hook":                            "local name",
		"https://127.0.0.1/hook":                           "public",
		"https://[::1]/hook":                               "public",
		"https://10.1.2.3/hook":                            "public",
		"https://169.254.169.254/latest":                   "public",
		"https://100.64.0.1/hook":                          "public",
		"https://[::ffff:127.0.0.1]/hook":                  "public",
		"https://example.com:0/hook":                       "port",
		"https://example.com:99999/hook":                   "port",
		"https://exämple.com/hook":                         "ASCII",
		"https://example.com/a b":                          "ASCII",
		"":                                                 "characters",
		"https://example.com/" + strings.Repeat("a", 2100): "characters",
	}
	for raw, want := range bad {
		_, err := ValidateURL(raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			short := raw
			if len(short) > 60 {
				short = short[:60]
			}
			t.Errorf("%q: error %v, want mention of %q", short, err, want)
		}
	}
}

// fakeResolver answers lookups from a script and counts them.
type fakeResolver struct {
	mu      sync.Mutex
	answers [][]netip.Addr // answer for the nth lookup; the last repeats
	calls   int
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	f.calls++
	return f.answers[i], nil
}

func (f *fakeResolver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// recorder is a Dial hook that notes every address the guard asked for and
// connects to the test server instead.
type recorder struct {
	mu     sync.Mutex
	server string
	dialed []string
}

func (r *recorder) dial(ctx context.Context, network, address string) (net.Conn, error) {
	r.mu.Lock()
	r.dialed = append(r.dialed, address)
	r.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, network, r.server)
}

func (r *recorder) addresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dialed...)
}

func addrs(list ...string) []netip.Addr {
	out := make([]netip.Addr, len(list))
	for i, s := range list {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

// guardedClient returns a client wired to srv through the guard: the TLS
// server's certificate is valid for example.com, which is what the URLs use.
func guardedClient(t *testing.T, srv *httptest.Server, res Resolver) (*http.Client, *recorder) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	rec := &recorder{server: srv.Listener.Addr().String()}
	return NewClient(ClientOptions{Resolver: res, Dial: rec.dial, TLS: &tls.Config{RootCAs: pool}, Timeout: 5 * time.Second}), rec
}

func tlsServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func get(client *http.Client, url string) (*http.Response, error) {
	res, err := client.Get(url)
	if err == nil {
		res.Body.Close()
	}
	return res, err
}

func TestClientDialsOnlyTheVettedPublicAddress(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	res := &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34")}}
	client, rec := guardedClient(t, srv, res)
	r, err := get(client, "https://example.com:8443/hook")
	if err != nil || r.StatusCode != 204 {
		t.Fatalf("%v %v", r, err)
	}
	if got := rec.addresses(); len(got) != 1 || got[0] != "93.184.216.34:8443" {
		t.Fatalf("dialed %v, want the resolved public address and port", got)
	}
}

func TestClientRefusesPrivateResolutions(t *testing.T) {
	for _, answer := range [][]string{
		{"127.0.0.1"}, {"10.0.0.5"}, {"169.254.169.254"}, {"100.64.0.9"}, {"::1"}, {"fd00::1"},
		{"::ffff:192.168.0.1"},
		{"93.184.216.34", "10.0.0.5"}, // one private answer poisons the set
	} {
		srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { t.Error("request reached the server") })
		client, rec := guardedClient(t, srv, &fakeResolver{answers: [][]netip.Addr{addrs(answer...)}})
		_, err := get(client, "https://example.com/hook")
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%v: error %v, want ErrBlockedAddress", answer, err)
		}
		if got := rec.addresses(); len(got) != 0 {
			t.Errorf("%v: dialed %v", answer, got)
		}
	}
}

func TestClientRefusesPrivateIPLiteralWithoutResolving(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { t.Error("request reached the server") })
	res := &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34")}}
	client, rec := guardedClient(t, srv, res)
	for _, target := range []string{"https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/latest/meta-data"} {
		if _, err := get(client, target); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: %v", target, err)
		}
	}
	if res.count() != 0 || len(rec.addresses()) != 0 {
		t.Fatalf("resolver calls %d, dials %v", res.count(), rec.addresses())
	}
}

// DNS rebinding: a name that answers with a public address first and a
// loopback address afterwards must never reach loopback. The guard resolves
// once per connection and connects to the address it vetted, so the second
// answer is never used for the first connection and is refused for the next.
func TestClientIsNotFooledByDNSRebinding(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	res := &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34"), addrs("127.0.0.1")}}
	client, rec := guardedClient(t, srv, res)
	if r, err := get(client, "https://example.com/hook"); err != nil || r.StatusCode != 200 {
		t.Fatalf("first request: %v %v", r, err)
	}
	if _, err := get(client, "https://example.com/hook"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("second request: %v, want ErrBlockedAddress", err)
	}
	for _, a := range rec.addresses() {
		if strings.HasPrefix(a, "127.") {
			t.Fatalf("dialed loopback: %v", rec.addresses())
		}
	}
	if res.count() != 2 {
		t.Fatalf("resolver called %d times, want once per connection", res.count())
	}
}

func TestClientNeverFollowsRedirects(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Redirect(w, r, "https://127.0.0.1/admin", http.StatusFound)
	})
	res := &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34")}}
	client, rec := guardedClient(t, srv, res)
	r, err := get(client, "https://example.com/hook")
	if err != nil || r.StatusCode != http.StatusFound {
		t.Fatalf("%v %v", r, err)
	}
	if hits != 1 || res.count() != 1 || len(rec.addresses()) != 1 {
		t.Fatalf("hits %d, resolutions %d, dials %v: the redirect was followed", hits, res.count(), rec.addresses())
	}
}

func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	client, rec := guardedClient(t, srv, &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34")}})
	if r, err := get(client, "https://example.com/hook"); err != nil || r.StatusCode != 200 {
		t.Fatalf("%v %v", r, err)
	}
	if got := rec.addresses(); len(got) != 1 || got[0] != "93.184.216.34:443" {
		t.Fatalf("connected via %v, not directly to the vetted address", got)
	}
}

func TestClientRequiresValidCertificate(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	rec := &recorder{server: srv.Listener.Addr().String()}
	// No test root: the self-signed certificate must be rejected.
	client := NewClient(ClientOptions{Resolver: &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34")}}, Dial: rec.dial})
	if _, err := get(client, "https://example.com/hook"); err == nil {
		t.Fatal("accepted an untrusted certificate")
	}
}
