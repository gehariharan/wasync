package transport

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
)

// The change journal reads (`wasync-axi observe`). Checkpoints are read
// on both transports; committing one is a write and exists only on the API.
func init() {
	queryRoutes["/changes"] = QueryRoute{Command: []string{"changes"}, Params: map[string]string{"after": "after", "limit": "limit"}}
	queryRoutes["/checkpoints/{consumer}"] = QueryRoute{Command: []string{"checkpoint"}, PathParams: map[string]string{"consumer": "consumer"}}
}

// Put sends body as JSON with PUT to path (relative to /api/v1). Only the
// API transport writes: `wasync query` is read-only by design.
func (a *API) Put(ctx context.Context, path string, body any, out any) error {
	if err := validatePath(path); err != nil {
		return err
	}
	if a.cred == nil {
		return &contract.Error{Code: contract.CodeCredentialAbsent, Message: "no wasync-axi agent credential is configured", Help: config.GrantHelp, Transport: NameAPI}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return &contract.Error{Code: contract.CodeInternal, Message: "cannot encode request body", Transport: NameAPI}
	}
	return a.send(ctx, a.client, http.MethodPut, path, nil, b, true, out)
}
