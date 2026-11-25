package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/installhint"
)

// InstallHelp tells a human how to install wasync.
var InstallHelp = "install wasync (" + installhint.InstallCore() + ") or set " + config.EnvBin + " to its path"

// QueryRoute maps one read-only /api/v1 path pattern to a `wasync query`
// command. Patterns use {name} for one path segment, like wasync's router.
type QueryRoute struct {
	// Command is the argument list after `query`, e.g. ["version"].
	Command []string
	// PathParams maps {name} segments of the pattern to wasync query flags.
	PathParams map[string]string
	// Params maps accepted API query parameters to wasync query flags.
	// Parameters not listed are rejected rather than silently dropped.
	Params map[string]string
	// Positional names the API query parameter passed as the trailing
	// positional argument, after "--" so a value starting with "-" is never
	// read as a flag.
	Positional string
}

// Search, message-page and thread parameters shared by several routes.
var pageParams = map[string]string{"limit": "limit", "cursor": "cursor", "from": "since", "to": "until", "sender_id": "sender"}

func withParams(base map[string]string, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// queryRoutes is the API path to offline command table. Only reads belong
// here; every v1 read endpoint with a `wasync query` equivalent is listed.
// GET /chats/{chat} has none: wasync-axi resolves chats with ?match=.
var queryRoutes = map[string]QueryRoute{
	"/version":               {Command: []string{"version"}},
	"/diagnostics":           {Command: []string{"doctor"}},
	"/chats":                 {Command: []string{"chats"}, Params: map[string]string{"match": "match", "enabled": "enabled"}},
	"/chats/{chat}/messages": {Command: []string{"messages"}, PathParams: map[string]string{"chat": "chat"}, Params: pageParams},
	"/messages/search":       {Command: []string{"search"}, Params: withParams(pageParams, map[string]string{"chat": "chat", "context": "context"}), Positional: "q"},
	"/messages/{ref}":        {Command: []string{"message"}, PathParams: map[string]string{"ref": "ref"}},
	"/messages/{ref}/thread": {Command: []string{"thread"}, PathParams: map[string]string{"ref": "ref"}, Params: map[string]string{"before": "before", "after": "after"}},
}

// QueryRoutes returns the mapped API path patterns, sorted.
func QueryRoutes() []string {
	out := make([]string, 0, len(queryRoutes))
	for p := range queryRoutes {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// matchRoute finds the route for an escaped API path and returns the
// unescaped values of its {name} segments. A literal segment beats a
// wildcard, so /messages/search never matches /messages/{ref}.
func matchRoute(path string) (QueryRoute, map[string]string, bool) {
	if r, ok := queryRoutes[path]; ok {
		return r, nil, true
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	best, bestWild := "", -1
	var bestVals map[string]string
	for _, pattern := range QueryRoutes() {
		psegs := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
		if len(psegs) != len(segs) {
			continue
		}
		vals, wild, ok := map[string]string{}, 0, true
		for i, p := range psegs {
			if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
				v, err := url.PathUnescape(segs[i])
				if err != nil || v == "" {
					ok = false
					break
				}
				vals[p[1:len(p)-1]] = v
				wild++
			} else if p != segs[i] {
				ok = false
				break
			}
		}
		if ok && (bestWild < 0 || wild < bestWild) {
			best, bestWild, bestVals = pattern, wild, vals
		}
	}
	if bestWild < 0 {
		return QueryRoute{}, nil, false
	}
	return queryRoutes[best], bestVals, true
}

// QueryArgs builds the full wasync argument list for path and query. Global
// flags (--data-dir) precede the `query` subcommand, as wasync expects.
// Values are passed as --flag=value so none can be read as another flag.
func QueryArgs(dataDir, path string, query url.Values) ([]string, error) {
	route, pathVals, ok := matchRoute(path)
	if !ok {
		return nil, &contract.Error{Code: contract.CodeOwnerOffline, Message: "this read needs the running wasync owner; it has no offline equivalent", Help: "start wasync (TUI or `wasync serve`) and retry", Transport: NameQuery}
	}
	var args []string
	if dataDir != "" {
		args = append(args, "--data-dir", dataDir)
	}
	args = append(args, "query")
	args = append(args, route.Command...)
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var positional []string
	for _, k := range keys {
		if k == route.Positional && k != "" {
			positional = query[k]
			continue
		}
		flag, ok := route.Params[k]
		if !ok {
			return nil, contract.Errorf(contract.CodeInvalidArgument, "", "parameter %q is not supported offline for %s", k, path)
		}
		for _, v := range query[k] {
			args = append(args, "--"+flag+"="+v)
		}
	}
	names := make([]string, 0, len(pathVals))
	for k := range pathVals {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		args = append(args, "--"+route.PathParams[k]+"="+pathVals[k])
	}
	args = append(args, "--v1", "--json")
	if len(positional) > 1 {
		return nil, contract.Errorf(contract.CodeInvalidArgument, "", "parameter %q given more than once", route.Positional)
	}
	if len(positional) == 1 {
		args = append(args, "--", positional[0])
	}
	return args, nil
}

// ResolveBin finds wasync: bin when set, otherwise `wasync` on PATH.
func ResolveBin(bin string) (string, error) {
	if bin != "" {
		info, err := os.Stat(bin)
		if err != nil || info.IsDir() {
			return "", contract.Errorf(contract.CodeWasyncNotFound, InstallHelp, "%s=%s does not point at a wasync binary", config.EnvBin, bin)
		}
		return bin, nil
	}
	p, err := exec.LookPath("wasync")
	if err != nil {
		return "", contract.Errorf(contract.CodeWasyncNotFound, InstallHelp, "wasync is not on PATH")
	}
	return p, nil
}

// QueryConfig configures the subprocess transport.
type QueryConfig struct {
	Bin     string // resolved wasync path
	DataDir string // passed as --data-dir when set
	Timeout time.Duration
}

// Query reads the archive offline through `wasync query --v1 --json`.
type Query struct{ cfg QueryConfig }

// NewQuery builds the subprocess transport.
func NewQuery(cfg QueryConfig) *Query {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Query{cfg: cfg}
}

// Name implements Transport.
func (q *Query) Name() string { return NameQuery }

// Bin returns the wasync path this transport runs.
func (q *Query) Bin() string { return q.cfg.Bin }

// Version implements Transport.
func (q *Query) Version(ctx context.Context) (contract.VersionInfo, error) {
	var v contract.VersionInfo
	err := q.Get(ctx, "/version", nil, &v)
	var ce *contract.Error
	if errors.As(err, &ce) && ce.Code == contract.CodeInvalidResponse {
		ce.Code = contract.CodeWasyncTooOld
		ce.Message = "this wasync does not support `wasync query version --v1`"
		ce.Help = "upgrade wasync: " + installhint.UpgradeCore()
	}
	return v, err
}

// Get implements Transport.
func (q *Query) Get(ctx context.Context, path string, query url.Values, out any) error {
	if err := validatePath(path); err != nil {
		return err
	}
	args, err := QueryArgs(q.cfg.DataDir, path, query)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, q.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, q.cfg.Bin, args...)
	cmd.Env = childEnv(os.Environ())
	cmd.Stdin = nil
	stdout := &limitedBuffer{max: maxBody}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard // raw dependency output never reaches the agent
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return &contract.Error{Code: contract.CodeTransport, Message: "wasync query timed out", Help: "retry; if it persists run `wasync query status`", Transport: NameQuery}
	}
	if stdout.overflow {
		return &contract.Error{Code: contract.CodeInvalidResponse, Message: "wasync query output is too large", Transport: NameQuery}
	}
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return &contract.Error{Code: contract.CodeWasyncNotFound, Message: "cannot run wasync", Help: InstallHelp, Transport: NameQuery}
		}
		if len(bytes.TrimSpace(stdout.Bytes())) == 0 {
			return &contract.Error{Code: contract.CodeInvalidResponse, Message: "wasync query failed without a v1 envelope", Help: "upgrade wasync: " + installhint.UpgradeCore(), Transport: NameQuery}
		}
	}
	return decodeEnvelope(stdout.Bytes(), NameQuery, out)
}

// childEnv drops wasync-axi's own credential from the subprocess env:
// offline reads are unscoped and never need it.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, config.EnvToken+"=") || strings.HasPrefix(kv, config.LegacyEnvToken+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

type limitedBuffer struct {
	bytes.Buffer
	max      int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		b.overflow = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
