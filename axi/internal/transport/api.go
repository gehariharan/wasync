package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/installhint"
)

// ValidateAddr accepts only literal loopback host:port addresses, the same
// rule wasync applies to its own listener.
func ValidateAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	number, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || portErr != nil || number <= 0 || number > 65535 || ip == nil || !ip.IsLoopback() {
		return contract.Errorf(contract.CodeInvalidArgument, "set "+config.EnvAPIAddr+" to a loopback address such as 127.0.0.1:8787",
			"API address %q is not a literal loopback address", addr)
	}
	return nil
}

// APIConfig configures the HTTP transport.
type APIConfig struct {
	Addr       string
	Credential *config.Credential // nil: reads that need auth fail with credential_missing
	Timeout    time.Duration      // per request; default 10s
}

// API reads wasync's loopback /api/v1.
type API struct {
	base   string
	cred   *config.Credential
	client *http.Client
}

// NewAPI validates the address and builds the transport.
func NewAPI(cfg APIConfig) (*API, error) {
	if err := ValidateAddr(cfg.Addr); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	rt := &http.Transport{
		Proxy:                 nil, // loopback only: never route through a proxy
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		ResponseHeaderTimeout: cfg.Timeout,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    true,
	}
	return &API{
		base: "http://" + cfg.Addr + "/api/v1",
		cred: cfg.Credential,
		client: &http.Client{
			Transport: rt,
			Timeout:   cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse // never follow redirects
			},
		},
	}, nil
}

// Name implements Transport.
func (a *API) Name() string { return NameAPI }

// Version implements Transport. The version endpoint needs no credential,
// so none is sent.
func (a *API) Version(ctx context.Context) (contract.VersionInfo, error) {
	var v contract.VersionInfo
	err := a.do(ctx, "/version", nil, false, &v)
	return v, err
}

// Get implements Transport.
func (a *API) Get(ctx context.Context, path string, query url.Values, out any) error {
	if err := validatePath(path); err != nil {
		return err
	}
	if a.cred == nil {
		return &contract.Error{Code: contract.CodeCredentialAbsent, Message: "no wasync-axi agent credential is configured", Help: config.GrantHelp, Transport: NameAPI}
	}
	return a.do(ctx, path, query, true, out)
}

func (a *API) do(ctx context.Context, path string, query url.Values, auth bool, out any) error {
	return a.send(ctx, http.MethodGet, path, query, nil, auth, out)
}

// send performs one request; payload, when not nil, is sent as JSON.
func (a *API) send(ctx context.Context, method, path string, query url.Values, payload []byte, auth bool, out any) error {
	u := a.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return &contract.Error{Code: contract.CodeTransport, Message: "cannot build API request", Transport: NameAPI}
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+a.cred.Secret)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return &contract.Error{Code: contract.CodeOwnerOffline, Message: "wasync API is not reachable: " + describeNetErr(err), Help: "start wasync (TUI or `wasync serve`) for live reads, or keep using offline reads", Transport: NameAPI}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return &contract.Error{Code: contract.CodeTransport, Message: fmt.Sprintf("wasync API answered with a redirect (%d); redirects are never followed", resp.StatusCode), Help: "check that " + config.EnvAPIAddr + " points at wasync", Transport: NameAPI}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return &contract.Error{Code: contract.CodeTransport, Message: "reading the wasync API response failed: " + describeNetErr(err), Transport: NameAPI}
	}
	if len(body) > maxBody {
		return &contract.Error{Code: contract.CodeInvalidResponse, Message: "wasync API response is too large", Transport: NameAPI}
	}
	err = decodeEnvelope(body, NameAPI, out)
	var ce *contract.Error
	if errors.As(err, &ce) && ce.Code == contract.CodeInvalidResponse && resp.StatusCode == http.StatusNotFound && path == "/version" {
		// A wasync without /api/v1 answers 404 without an envelope.
		return &contract.Error{Code: contract.CodeWasyncTooOld, Message: "wasync API has no /api/v1", Help: "upgrade wasync: " + installhint.UpgradeCore(), Transport: NameAPI}
	}
	if err == nil && resp.StatusCode >= 400 {
		return &contract.Error{Code: contract.CodeInvalidResponse, Message: fmt.Sprintf("wasync API answered HTTP %d without an error envelope", resp.StatusCode), Transport: NameAPI}
	}
	return err
}

func describeNetErr(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return "connection failed"
	}
	return "request failed"
}
