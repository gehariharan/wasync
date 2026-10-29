package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/gehariharan/wasync/internal/agentauth"
)

// agentsApp returns an App whose input is served one byte at a time (so each
// prompt consumes only its own line) and whose whatsapp-axi config directory
// is an empty temporary one, so a grant never saves into the real one.
func agentsApp(t *testing.T, in string, interactive bool) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	axiDir := filepath.Join(t.TempDir(), "axi-config")
	var out, errs bytes.Buffer
	return &App{In: iotest.OneByteReader(strings.NewReader(in)), Out: &out, Err: &errs, Interactive: interactive,
		Getenv: func(k string) string {
			if k == "WHATSAPP_AXI_CONFIG_DIR" {
				return axiDir
			}
			return ""
		}}, &out, &errs
}

func TestAgentsHelpWithoutLocalState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	for _, args := range [][]string{{"agents", "--help"}, {"agents"}, {"help", "agents"}, {"agents", "grant", "--help"}} {
		app, out, _ := agentsApp(t, "", false)
		if err := app.Run(context.Background(), append([]string{"--data-dir", dir}, args...)); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "wasync agents grant NAME") {
			t.Fatalf("%v: %q", args, out.String())
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("help created local state")
	}
}

func TestAgentsGrantRevokeRequireInteractiveConfirmation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	run := func(app *App, args ...string) error {
		return app.Run(context.Background(), append([]string{"--data-dir", dir, "agents"}, args...))
	}
	app, out, _ := agentsApp(t, "axi\n", false)
	if err := run(app, "grant", "axi"); err == nil || !strings.Contains(err.Error(), "interactive terminal") || out.Len() != 0 {
		t.Fatalf("non-interactive grant: %v %q", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, agentauth.FileName)); !os.IsNotExist(err) {
		t.Fatal("registry written without confirmation")
	}
	for _, args := range [][]string{{"grant", "axi", "--yes"}, {"grant", "Bad_Name"}, {"grant", "axi", "--scope", "destructive"}, {"grant", "axi", "--scope", "send"}, {"grant", "axi", "--rate", "0"}, {"grant"}} {
		app, _, _ = agentsApp(t, "axi\n", true)
		if err := run(app, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	app, out, _ = agentsApp(t, "wrong\n", true)
	if err := run(app, "grant", "axi"); err == nil || out.Len() != 0 {
		t.Fatal("mismatched confirmation accepted")
	}
	app, out, errs := agentsApp(t, "axi\nn\n", true)
	if err := run(app, "grant", "axi", "--scope", "read.chats,send", "--chat", "120363000000000001@g.us", "--rate", "30", "--expires", "720h"); err != nil {
		t.Fatal(err)
	}
	cred := strings.TrimSpace(out.String())
	if _, _, err := agentauth.ParseCredential(cred); err != nil || strings.Count(out.String(), "wsa_") != 1 {
		t.Fatalf("credential output %q", out.String())
	}
	if !strings.Contains(errs.String(), "shown once") || strings.Contains(errs.String(), cred) {
		t.Fatalf("warning %q", errs.String())
	}
	a := agentauth.NewAuthorizer(dir)
	if c, err := a.Authenticate("Bearer " + cred); err != nil || c.RatePerMinute != 30 || c.ExpiresAt == nil {
		t.Fatalf("%v %+v", err, c)
	}

	// list never shows secrets or hashes.
	app, out, _ = agentsApp(t, "", false)
	if err := run(app, "list", "--json"); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Clients []map[string]any `json:"clients"`
	}
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil || len(listed.Clients) != 1 || listed.Clients[0]["name"] != "axi" || listed.Clients[0]["status"] != "active" {
		t.Fatalf("%v %s", err, out.String())
	}
	_, secret, _ := agentauth.ParseCredential(cred)
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), "sha256") {
		t.Fatal("list leaks secret material")
	}
	app, out, _ = agentsApp(t, "", false)
	if err := run(app, "list"); err != nil || !strings.Contains(out.String(), "NAME") || !strings.Contains(out.String(), "read.chats,send") || strings.Contains(out.String(), secret) {
		t.Fatalf("%v %q", err, out.String())
	}

	app, _, _ = agentsApp(t, "axi\n", false)
	if err := run(app, "revoke", "axi"); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatal("non-interactive revoke")
	}
	app, _, _ = agentsApp(t, "nope\n", true)
	if err := run(app, "revoke", "axi"); err == nil {
		t.Fatal("revoke without matching confirmation")
	}
	if _, err := a.Authenticate("Bearer " + cred); err != nil {
		t.Fatal("refused revoke changed state")
	}
	app, out, _ = agentsApp(t, "axi\n", true)
	if err := run(app, "revoke", "axi"); err != nil || !strings.Contains(out.String(), "revoked axi") {
		t.Fatal(err)
	}
	if _, err := a.Authenticate("Bearer " + cred); err == nil {
		t.Fatal("revoked credential accepted")
	}
	audit, _ := os.ReadFile(filepath.Join(dir, agentauth.AuditFileName))
	if !strings.Contains(string(audit), `"event":"grant"`) || !strings.Contains(string(audit), `"event":"revoke"`) || strings.Contains(string(audit), secret) {
		t.Fatalf("audit %s", audit)
	}
	app, out, _ = agentsApp(t, "", false)
	if err := run(app, "list", "--json"); err != nil || !strings.Contains(out.String(), `"status":"revoked"`) {
		t.Fatalf("%v %s", err, out.String())
	}
}
