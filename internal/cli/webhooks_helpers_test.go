package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/gehariharan/wasync/internal/webhook"
)

type publicResolver struct{}

func (publicResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

// testSender reaches srv through the real SSRF guard, with a fake resolver
// that answers a public address and a dialer that connects to srv.
func testSender(t *testing.T, srv *httptest.Server) *webhook.Sender {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return webhook.NewSender(webhook.NewClient(webhook.ClientOptions{
		Resolver: publicResolver{},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
		TLS: &tls.Config{RootCAs: pool},
	}))
}
