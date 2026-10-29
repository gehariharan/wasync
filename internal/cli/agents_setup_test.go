package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/storage"
)

// setupApp returns an interactive App whose input is served one byte at a
// time (so each prompt consumes only its own line) and whose whatsapp-axi
// config directory is a temporary one.
func setupApp(t *testing.T, input string) (*App, *strings.Builder, *strings.Builder, string) {
	t.Helper()
	axiDir := filepath.Join(t.TempDir(), "axi-config")
	var out, errs strings.Builder
	app := &App{
		In: iotest.OneByteReader(strings.NewReader(input)), Out: &out, Err: &errs, Interactive: true,
		Getenv: func(k string) string {
			if k == "WHATSAPP_AXI_CONFIG_DIR" {
				return axiDir
			}
			return ""
		},
	}
	return app, &out, &errs, filepath.Join(axiDir, "credential")
}

func grantArgs(dir string, args ...string) []string {
	return append([]string{"--data-dir", dir, "agents", "grant"}, args...)
}

// seedArchive creates a fake archive with a few chats in a temporary data dir.
func seedArchive(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.UseAccount(ctx, "me@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGroups(ctx, id, []storage.Group{
		{ID: "111@g.us", Name: "Family"},
		{ID: "222@g.us", Name: "Family Trip"},
		{ID: "333@g.us", Name: "Book Club"},
		{ID: "444@g.us", Name: "Work Team"},
		{ID: "555@g.us", Name: "Work Social"},
		{ID: "666@g.us", Name: "Work\x1b[2KParty"},
		{ID: "777@g.us", Name: "Mom@Home"},
		{ID: "15551234567@s.whatsapp.net", Name: "Dana Example"},
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGrantWithoutChatsPointsToOnDemandFirst(t *testing.T) {
	app, out, _, _ := setupApp(t, "")
	err := app.Run(context.Background(), grantArgs(filepath.Join(t.TempDir(), "d"), "helper", "--scope", "read.chats,send"))
	if err == nil || out.Len() != 0 {
		t.Fatalf("%v %q", err, out.String())
	}
	msg := err.Error()
	if !strings.Contains(msg, "--on-demand") || !strings.Contains(msg, "wasync actions approve") || !strings.Contains(msg, "--chat NAME") {
		t.Fatalf("message %q", msg)
	}
	if strings.Index(msg, "--on-demand") > strings.Index(msg, "--chat") || strings.Contains(msg, "CHAT_JID") {
		t.Fatalf("on-demand should come first and no raw JID placeholder: %q", msg)
	}
}

func TestGrantChatByNameShowsJIDAndNeedsConfirmation(t *testing.T) {
	dir := seedArchive(t)
	// y to the chat, name to grant, n to the save offer.
	app, out, errs, credPath := setupApp(t, "y\nhelper\nn\n")
	if err := app.Run(context.Background(), grantArgs(dir, "helper", "--scope", "read.chats,send", "--chat", "book club")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.String(), "Book Club") || !strings.Contains(errs.String(), "333@g.us") {
		t.Fatalf("prompt did not show name and JID: %q", errs.String())
	}
	c, err := agentauth.ActiveByName(dir, "helper")
	if err != nil || c == nil || len(c.Chats) != 1 || c.Chats[0] != "333@g.us" {
		t.Fatalf("%v %+v", err, c)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "wsa_") {
		t.Fatalf("declined save must still print the credential: %q", out.String())
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Fatal("credential saved although declined")
	}
}

func TestGrantChatNameWithAtIsLookedUpNotTakenAsJID(t *testing.T) {
	dir := seedArchive(t)
	app, _, errs, _ := setupApp(t, "y\nhelper\nn\n")
	if err := app.Run(context.Background(), grantArgs(dir, "helper", "--scope", "send", "--chat", "Mom@Home")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.String(), "777@g.us") {
		t.Fatalf("prompt did not show the JID: %q", errs.String())
	}
	c, err := agentauth.ActiveByName(dir, "helper")
	if err != nil || c == nil || len(c.Chats) != 1 || c.Chats[0] != "777@g.us" {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestGrantChatByNameDirectChatAndDecline(t *testing.T) {
	dir := seedArchive(t)
	app, _, errs, _ := setupApp(t, "n\n")
	err := app.Run(context.Background(), grantArgs(dir, "helper", "--scope", "send", "--chat", "dana"))
	if err == nil || !strings.Contains(err.Error(), "not confirmed") || !strings.Contains(errs.String(), "15551234567@s.whatsapp.net") {
		t.Fatalf("%v %q", err, errs.String())
	}
	if c, _ := agentauth.ActiveByName(dir, "helper"); c != nil {
		t.Fatal("client created after declining the chat")
	}
}

func TestGrantChatByNameRefusesAmbiguousAndUnknown(t *testing.T) {
	dir := seedArchive(t)
	app, _, _, _ := setupApp(t, "y\nhelper\n")
	err := app.Run(context.Background(), grantArgs(dir, "helper", "--scope", "send", "--chat", "work"))
	if err == nil || !strings.Contains(err.Error(), "444@g.us") || !strings.Contains(err.Error(), "555@g.us") || !strings.Contains(err.Error(), "Work Team") {
		t.Fatalf("ambiguous: %v", err)
	}
	if strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), "Work[2KParty") {
		t.Fatalf("chat name control characters not removed: %q", err)
	}
	app, _, _, _ = setupApp(t, "y\nhelper\n")
	err = app.Run(context.Background(), grantArgs(dir, "helper", "--scope", "send", "--chat", "nope"))
	if err == nil || !strings.Contains(err.Error(), "--on-demand") {
		t.Fatalf("unknown: %v", err)
	}
	if c, _ := agentauth.ActiveByName(dir, "helper"); c != nil {
		t.Fatal("client created for a refused name")
	}
}

func TestGrantChatByNameChecksOtherFlagsBeforeAskingAboutChats(t *testing.T) {
	dir := seedArchive(t)
	for _, args := range [][]string{
		{"helper", "--scope", "send", "--chat", "Family", "--rate", "7000"},
		{"bad name!", "--scope", "send", "--chat", "Family"},
	} {
		app, _, errs, _ := setupApp(t, "y\nhelper\n")
		if err := app.Run(context.Background(), grantArgs(dir, args...)); err == nil {
			t.Fatalf("%v: accepted", args)
		}
		if strings.Contains(errs.String(), "Allow chat") {
			t.Fatalf("%v: asked about chats before rejecting: %q", args, errs.String())
		}
	}
}

func TestGrantChatByNameWithoutArchiveSuggestsOnDemand(t *testing.T) {
	app, _, _, _ := setupApp(t, "")
	err := app.Run(context.Background(), grantArgs(filepath.Join(t.TempDir(), "empty"), "helper", "--scope", "send", "--chat", "Family"))
	if err == nil || !strings.Contains(err.Error(), "--on-demand") {
		t.Fatalf("%v", err)
	}
}

func TestGrantRawJIDStillWorksWithoutArchive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	app, _, _, _ := setupApp(t, "helper\nn\n")
	if err := app.Run(context.Background(), grantArgs(dir, "helper", "--scope", "send", "--chat", "120363000000000001@g.us")); err != nil {
		t.Fatal(err)
	}
}

func TestGrantSavesCredentialForAxiPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	app, out, errs, credPath := setupApp(t, "helper\ny\n")
	if err := app.Run(context.Background(), grantArgs(dir, "helper", "--on-demand", "--scope", "read.chats,read.messages,send")); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(credPath)
	if err != nil || !strings.HasPrefix(string(saved), "wsa_") {
		t.Fatalf("%v %q", err, saved)
	}
	secret := strings.TrimSpace(string(saved))
	if strings.Contains(out.String(), secret) || strings.Contains(errs.String(), secret) {
		t.Fatal("saved credential also leaked to the terminal")
	}
	if !strings.Contains(errs.String(), credPath) {
		t.Fatalf("saved path not reported: %q", errs.String())
	}
	if fi, _ := os.Stat(credPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(credPath)); di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
	if _, err := agentauth.NewAuthorizer(dir).Authenticate("Bearer " + secret); err != nil {
		t.Fatalf("saved credential does not authenticate: %v", err)
	}
	audit, _ := os.ReadFile(filepath.Join(dir, agentauth.AuditFileName))
	if strings.Contains(string(audit), secret) {
		t.Fatal("audit log holds the secret")
	}
}

func TestGrantDoesNotReplaceADifferentSavedCredentialWithoutAsking(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	app, out, _, credPath := setupApp(t, "helper\nn\n")
	if err := os.MkdirAll(filepath.Dir(credPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte("wsa_old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), grantArgs(dir, "helper", "--on-demand")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(credPath); string(b) != "wsa_old\n" {
		t.Fatalf("overwritten: %q", b)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "wsa_") {
		t.Fatalf("not printed after declining replace: %q", out.String())
	}

	// Agreeing to the replacement overwrites it.
	dir2 := filepath.Join(t.TempDir(), "data2")
	app, out, _, _ = setupApp(t, "helper\ny\n")
	app.Getenv = func(k string) string {
		if k == "WHATSAPP_AXI_CONFIG_DIR" {
			return filepath.Dir(credPath)
		}
		return ""
	}
	if err := app.Run(context.Background(), grantArgs(dir2, "helper", "--on-demand")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(credPath); strings.Contains(string(b), "wsa_old") || out.Len() != 0 {
		t.Fatalf("not replaced: %q out=%q", b, out.String())
	}
}

func TestGrantSaveFailureStillPrintsCredential(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	app, out, errs, credPath := setupApp(t, "helper\ny\n")
	// A regular file where the config directory should be makes saving fail.
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(credPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(credPath), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), grantArgs(dir, "helper", "--on-demand")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "wsa_") || !strings.Contains(errs.String(), "not saving there") {
		t.Fatalf("out=%q err=%q", out.String(), errs.String())
	}
}
