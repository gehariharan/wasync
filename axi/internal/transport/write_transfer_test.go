package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
)

// A write with an attachment is acknowledged only after wasync has uploaded
// it to WhatsApp, so it may take far longer than a read.
func TestPostTransferOutwaitsTheReadTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"r-1","data":{"status":"server_acknowledged"}}`))
	}))
	t.Cleanup(srv.Close)
	a, err := NewAPI(APIConfig{Addr: strings.TrimPrefix(srv.URL, "http://"), Credential: &config.Credential{Secret: "wsa_secret"}, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var resp contract.Response
	if err = a.Post(context.Background(), "/messages", map[string]any{"text": "hi"}, &resp); err == nil {
		t.Fatal("an ordinary write waited past its timeout")
	}
	if err = a.PostTransfer(context.Background(), "/messages", map[string]any{"video": []byte("v")}, &resp); err != nil || resp.RequestID != "r-1" {
		t.Fatalf("%+v %v", resp, err)
	}
	none, err := NewAPI(APIConfig{Addr: strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	var ce *contract.Error
	if err = none.PostTransfer(context.Background(), "/messages", nil, &resp); !errors.As(err, &ce) || ce.Code != contract.CodeCredentialAbsent {
		t.Fatal(err)
	}
}
