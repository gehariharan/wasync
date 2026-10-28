package transport

import (
	"context"
	"net/http"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
)

// Delete sends DELETE to path (relative to /api/v1) with no body. Like Post
// and Put it exists only on the API transport.
func (a *API) Delete(ctx context.Context, path string, out any) error {
	if err := validatePath(path); err != nil {
		return err
	}
	if a.cred == nil {
		return &contract.Error{Code: contract.CodeCredentialAbsent, Message: "no whatsapp-axi agent credential is configured", Help: config.GrantHelp, Transport: NameAPI}
	}
	return a.send(ctx, http.MethodDelete, path, nil, nil, true, out)
}
