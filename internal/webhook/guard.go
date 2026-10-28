package webhook

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrBlockedAddress reports a destination the SSRF guard refuses: a loopback,
// private, link-local, carrier-grade NAT, metadata, multicast or otherwise
// non-public address.
var ErrBlockedAddress = errors.New("destination address is not a public internet address")

const (
	maxURLLen      = 2048
	dialTimeout    = 5 * time.Second
	requestTimeout = 10 * time.Second
)

// blockedPrefixes are non-public ranges that the netip predicates do not
// cover. Loopback, private (RFC 1918 and fc00::/7), link-local (which holds
// the 169.254.169.254 and fd00:ec2::254 cloud metadata addresses), multicast
// and unspecified are rejected by predicate in blockedIP.
var blockedPrefixes = parsePrefixes(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // carrier-grade NAT
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"192.88.99.0/24",  // 6to4 relay anycast
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved and broadcast
	"64:ff9b::/96",    // NAT64: embeds an IPv4 address the guard cannot see
	"100::/64",        // discard-only
	"2001::/32",       // Teredo tunnelling
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4: embeds an IPv4 address
)

func parsePrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// blockedIP reports whether ip is not a public unicast address. IPv4-mapped
// IPv6 addresses are judged as the IPv4 address they embed.
func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateURL checks that raw is an acceptable webhook destination without
// resolving it: absolute https, no credentials or fragment, an ASCII host
// that is not a local name, and, for an IP literal, a public address. The
// resolved addresses are checked again at connection time.
func ValidateURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > maxURLLen {
		return nil, fmt.Errorf("webhook URL must be 1-%d characters", maxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook URL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("webhook URL must use https")
	}
	host := u.Hostname()
	switch {
	case host == "":
		return nil, fmt.Errorf("webhook URL needs a host")
	case u.User != nil:
		return nil, fmt.Errorf("webhook URL must not contain credentials")
	case u.Fragment != "" || u.Opaque != "":
		return nil, fmt.Errorf("webhook URL must not contain a fragment")
	}
	for _, r := range raw {
		if r <= ' ' || r > '~' {
			return nil, fmt.Errorf("webhook URL must be ASCII without spaces or control characters")
		}
	}
	if port := u.Port(); port != "" {
		if n, convErr := strconv.Atoi(port); convErr != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid port in webhook URL")
		}
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if ip, parseErr := netip.ParseAddr(strings.Trim(lower, "[]")); parseErr == nil {
		if blockedIP(ip) {
			return nil, fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
		}
		return u, nil
	}
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") || !strings.Contains(lower, ".") {
		return nil, fmt.Errorf("webhook host %q is a local name; use a public hostname", host)
	}
	return u, nil
}

// Resolver looks up the addresses of a host. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ClientOptions overrides the production network behaviour; tests use it to
// supply a fake resolver, a fake dialer and the test server's certificate.
type ClientOptions struct {
	Resolver Resolver
	Dial     func(ctx context.Context, network, address string) (net.Conn, error)
	TLS      *tls.Config
	Timeout  time.Duration
}

// NewClient returns an HTTP client that can reach only public addresses.
//
// The guard runs inside the dialer: the host is resolved once, every answer
// must be public (a mixed answer is refused), and the connection is made to
// the vetted address itself, so DNS cannot change between the check and the
// connection (DNS rebinding). Redirects are never followed and environment
// proxies are ignored, because either could lead past the guard.
func NewClient(opts ClientOptions) *http.Client {
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	dial := opts.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: dialTimeout}
		dial = dialer.DialContext
	}
	if opts.Timeout == 0 {
		opts.Timeout = requestTimeout
	}
	tlsConfig := opts.TLS
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.MinVersion = tls.VersionTLS12
	transport := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: opts.Timeout,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addrs, err := lookup(ctx, opts.Resolver, host)
			if err != nil {
				return nil, err
			}
			var last error
			for _, ip := range addrs {
				conn, dialErr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				last = dialErr
			}
			return nil, last
		},
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       opts.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// lookup resolves host and refuses the whole answer when any address in it is
// not public.
func lookup(ctx context.Context, r Resolver, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if blockedIP(ip) {
			return nil, fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, ip := range addrs {
		if blockedIP(ip) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, ip.Unmap())
		}
		out = append(out, ip.Unmap())
	}
	return out, nil
}
