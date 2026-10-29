package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/webhook"
)

func TestWebhooksHelpWithoutLocalState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	for _, args := range [][]string{{"webhooks", "--help"}, {"webhooks"}, {"help", "webhooks"}, {"webhooks", "add", "--help"}, {"help", "webhooks", "deadletter"}} {
		app, out, _ := agentsApp(t, "", false)
		if err := app.Run(context.Background(), append([]string{"--data-dir", dir}, args...)); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "wasync webhooks add NAME") {
			t.Fatalf("%v: %q", args, out.String())
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("help created local state")
	}
}

func TestWebhooksSubcommandsAreCatalogued(t *testing.T) {
	catalogued := map[string]bool{}
	for _, command := range commandCatalog {
		catalogued[command.Path] = true
	}
	for _, name := range []string{"webhooks", "webhooks add", "webhooks list", "webhooks remove", "webhooks test", "webhooks deadletter"} {
		if !catalogued[name] {
			t.Errorf("%s missing from commandCatalog", name)
		}
		if extendedHelp[name] == "" {
			t.Errorf("%s has no extended help", name)
		}
	}
	if !strings.Contains(usage, "webhooks add|list|remove|test|deadletter") {
		t.Error("webhooks missing from root usage")
	}
}

func TestWebhooksAddRemoveRequireInteractiveConfirmation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	run := func(app *App, args ...string) error {
		return app.Run(context.Background(), append([]string{"--data-dir", dir, "webhooks"}, args...))
	}
	const target = "https://example.com/hook"
	app, out, _ := agentsApp(t, "crm\n", false)
	if err := run(app, "add", "crm", "--url", target); err == nil || !strings.Contains(err.Error(), "interactive terminal") || out.Len() != 0 {
		t.Fatalf("non-interactive add: %v %q", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, webhook.FileName)); !os.IsNotExist(err) {
		t.Fatal("registry written without confirmation")
	}
	for _, args := range [][]string{
		{"add", "crm", "--url", target, "--yes"},
		{"add", "Bad_Name", "--url", target},
		{"add", "crm"},
		{"add", "crm", "--url", "http://example.com/hook"},
		{"add", "crm", "--url", "https://127.0.0.1/hook"},
		{"add", "crm", "--url", "https://169.254.169.254/latest"},
		{"add", "crm", "--url", target, "--op", "message.deleted"},
		{"add", "crm", "--url", target, "--chat", "nonsense"},
	} {
		app, _, _ = agentsApp(t, "crm\n", true)
		if err := run(app, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	app, out, _ = agentsApp(t, "wrong\n", true)
	if err := run(app, "add", "crm", "--url", target); err == nil || out.Len() != 0 {
		t.Fatal("mismatched confirmation accepted")
	}
	app, out, errs := agentsApp(t, "crm\n", true)
	if err := run(app, "add", "crm", "--url", target, "--chat", "120363000000000001@g.us", "--op", "message.created,message.updated"); err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(out.String())
	if !strings.HasPrefix(secret, webhook.SecretPrefix) || strings.Count(out.String(), webhook.SecretPrefix) != 1 {
		t.Fatalf("secret output %q", out.String())
	}
	if !strings.Contains(errs.String(), "shown once") || strings.Contains(errs.String(), secret) || !strings.Contains(errs.String(), "never message text") {
		t.Fatalf("warning %q", errs.String())
	}

	app, out, _ = agentsApp(t, "", false)
	if err := run(app, "list", "--json"); err == nil || !strings.Contains(err.Error(), "interactive terminal") || out.Len() != 0 {
		t.Fatalf("non-interactive list: %v %q", err, out.String())
	}
	app, out, _ = agentsApp(t, "", true)
	if err := run(app, "list", "--json"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), "secret") {
		t.Fatalf("list exposes the secret: %s", out.String())
	}
	var listed struct {
		Webhooks []webhookView `json:"webhooks"`
	}
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil || len(listed.Webhooks) != 1 || listed.Webhooks[0].Status != "active" || listed.Webhooks[0].Chats[0] != "120363000000000001@g.us" {
		t.Fatalf("list %s %v", out.String(), err)
	}
	app, out, _ = agentsApp(t, "", true)
	if err := run(app, "list"); err != nil || !strings.Contains(out.String(), "crm") || strings.Contains(out.String(), secret) {
		t.Fatalf("table list: %v %q", err, out.String())
	}

	app, _, _ = agentsApp(t, "crm\n", false)
	if err := run(app, "remove", "crm"); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("non-interactive remove: %v", err)
	}
	app, _, _ = agentsApp(t, "nope\n", true)
	if err := run(app, "remove", "crm"); err == nil {
		t.Fatal("mismatched removal accepted")
	}
	app, out, _ = agentsApp(t, "crm\n", true)
	if err := run(app, "remove", "crm"); err != nil || !strings.Contains(out.String(), "removed crm") {
		t.Fatalf("remove: %v %q", err, out.String())
	}
	if active, _ := webhook.Active(dir); len(active) != 0 {
		t.Fatalf("still active: %+v", active)
	}
	audit, _ := os.ReadFile(filepath.Join(dir, "agent-audit.log"))
	if !strings.Contains(string(audit), "webhook.add") || !strings.Contains(string(audit), "webhook.remove") || strings.Contains(string(audit), secret) {
		t.Fatalf("audit log:\n%s", audit)
	}
}

type privateResolver struct{}

func (privateResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.0.0.7")}, nil
}

func TestWebhooksTestSendsSignedPingThroughTheGuard(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	secret, _, err := webhook.Add(dir, webhook.AddRequest{Name: "crm", URL: "https://example.com/hook"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var got webhook.Event
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if webhook.Verify(secret, r.Header.Get(webhook.HeaderTimestamp), r.Header.Get(webhook.HeaderSignature), body, time.Now()) != nil || json.Unmarshal(body, &got) != nil {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	app, out, _ := agentsApp(t, "", true)
	app.WebhookSender = testSender(t, srv)
	if err = app.Run(context.Background(), []string{"--data-dir", dir, "webhooks", "test", "crm"}); err != nil {
		t.Fatal(err)
	}
	if got.Type != "ping" || !strings.Contains(out.String(), "delivered ping") || !strings.Contains(out.String(), "204") {
		t.Fatalf("ping %+v out %q", got, out.String())
	}
	app, out, _ = agentsApp(t, "", false)
	if err = app.Run(context.Background(), []string{"--data-dir", dir, "webhooks", "test", "crm"}); err == nil || out.Len() != 0 {
		t.Fatalf("non-interactive test: %v", err)
	}
	// A name that resolves privately is refused without any connection.
	app, _, _ = agentsApp(t, "", true)
	app.WebhookSender = webhook.NewSender(webhook.NewClient(webhook.ClientOptions{Resolver: privateResolver{}}))
	if err = app.Run(context.Background(), []string{"--data-dir", dir, "webhooks", "test", "crm"}); err == nil || !strings.Contains(err.Error(), webhook.CodeBlocked) {
		t.Fatalf("private resolution: %v", err)
	}
}

func TestWebhooksDeadLetterCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if _, _, err := webhook.Add(dir, webhook.AddRequest{Name: "crm", URL: "https://example.com/hook"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	eps, _ := webhook.List(dir)
	if err := webhook.AppendDeadLetter(dir, webhook.DeadLetter{Time: time.Now(), EndpointID: eps[0].ID, EndpointName: "crm", EventID: "evt_1", Sequence: 1, Op: "message.created", ChatID: "one@g.us", Attempts: 5, Code: webhook.CodeServerError, Status: 503}); err != nil {
		t.Fatal(err)
	}
	run := func(interactive bool, args ...string) (string, error) {
		app, out, _ := agentsApp(t, "", interactive)
		err := app.Run(context.Background(), append([]string{"--data-dir", dir, "webhooks", "deadletter"}, args...))
		return out.String(), err
	}
	if out, err := run(false, "crm", "--json"); err == nil || out != "" {
		t.Fatalf("non-interactive deadletter: %v %q", err, out)
	}
	out, err := run(true, "crm", "--json")
	if err != nil || !strings.Contains(out, `"event_id":"evt_1"`) {
		t.Fatalf("%v %q", err, out)
	}
	if _, err = run(true, "ghost"); err == nil {
		t.Fatal("unknown endpoint accepted")
	}
	if _, err = run(false, "--clear"); err == nil {
		t.Fatal("non-interactive clear accepted")
	}
	if out, err = run(true, "--clear"); err != nil || !strings.Contains(out, "cleared 1") {
		t.Fatalf("%v %q", err, out)
	}
	if out, _ = run(true, "--json"); !strings.Contains(out, `"dead_letters":[]`) {
		t.Fatalf("after clear: %q", out)
	}
}
