package transport

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
)

// Post sends body as JSON with POST to path (relative to /api/v1). Writes to
// WhatsApp exist only on the API transport: `wasync query` is read-only by
// design, and wasync alone decides whether the credential may write.
func (a *API) Post(ctx context.Context, path string, body any, out any) error {
	if err := validatePath(path); err != nil {
		return err
	}
	if a.cred == nil {
		return &contract.Error{Code: contract.CodeCredentialAbsent, Message: "no whatsapp-axi agent credential is configured", Help: config.GrantHelp, Transport: NameAPI}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return &contract.Error{Code: contract.CodeInternal, Message: "cannot encode request body", Transport: NameAPI}
	}
	return a.send(ctx, http.MethodPost, path, nil, b, true, out)
}
