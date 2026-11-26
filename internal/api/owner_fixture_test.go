package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/tui"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

const (
	familyChat = "120363000000000003@g.us"
	phoneChat  = "15551234567@s.whatsapp.net"
	otherChat  = "15557654321@s.whatsapp.net"
)

// accessFixture is a real owner: archive, approval queue, and a session that
// applies approvals, with synthetic chats. Its two agents are credentials as
// the previous release stored them for an agent connected "on demand": they
// start with no chats.
type accessFixture struct {
	store    *storage.Store
	account  int64
	queue    *approval.Queue
	session  *tui.Session
	server   *Server
	dir      string
	approver string                // approve.token
	jit      string                // on-demand credential: read.chats, read.messages, send
	other    string                // a second on-demand credential
	sent     *[]whatsapp.SendInput // what the owner sent for approved agent requests
}

func setupAccess(t *testing.T) accessFixture { return setupAccessWith(t, nil) }

// setupAccessWith starts the owner with a human-check gate (nil: terminal only).
func setupAccessWith(t *testing.T, human *humancheck.Gate) accessFixture {
	t.Helper()
	return setupAccessOwner(t, human, nil)
}

// setupAccessOwner also lets the test adjust what the owner offers the page.
func setupAccessOwner(t *testing.T, human *humancheck.Gate, tweak func(*Owner)) accessFixture {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	account, err := store.UseAccount(ctx, "synthetic@lid")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: familyChat, Name: "Family Group"}, {ID: phoneChat, Name: phoneChat}, {ID: otherChat, Name: "+1∙∙∙∙21"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetEnabled(ctx, account, []string{familyChat}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Apply(ctx, account, []storage.Message{{GroupID: familyChat, MessageID: "m1", SenderID: "alice@lid", Timestamp: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Type: "text", Text: "synthetic family text", Source: "live"}}); err != nil {
		t.Fatal(err)
	}
	// Capture is switched off again, so no chat is synced to begin with.
	if err = store.SetEnabled(ctx, account, []string{familyChat}, false); err != nil {
		t.Fatal(err)
	}
	queue := &approval.Queue{}
	session := tui.NewSession(ctx, store, nil)
	session.Approvals, session.DataDir = queue, dir
	session.Human = human
	sent := &[]whatsapp.SendInput{}
	session.Send = func(_ context.Context, in whatsapp.SendInput) (whatsapp.SendResult, error) {
		*sent = append(*sent, in)
		return whatsapp.SendResult{MessageID: "3EB0" + strconv.Itoa(len(*sent))}, nil
	}
	owner := Owner{Addr: "127.0.0.1:0", Dir: dir, Store: store, Approvals: queue, Approve: session.ApproveAction, Reject: session.RejectAction, Human: human}
	if tweak != nil {
		tweak(&owner)
	}
	server, err := StartOwner(owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	approver, err := loadNamedToken(dir, "approve.token")
	if err != nil {
		t.Fatal(err)
	}
	jit := grantLegacyOnDemand(t, dir, "jit", "read.chats", "read.messages", "send")
	other := grantLegacyOnDemand(t, dir, "other", "read.chats", "read.messages")
	return accessFixture{store, account, queue, session, server, dir, approver, jit, other, sent}
}

// grantLegacyOnDemand stores a credential exactly as the previous release
// stored an agent connected "on demand": no chats of its own, so it can only
// read the chats a person approved for it one by one. It returns the
// credential.
func grantLegacyOnDemand(t *testing.T, dir, name string, scopes ...string) string {
	t.Helper()
	credential, client, err := agentauth.Grant(dir, agentauth.GrantRequest{Name: name, Scopes: scopes, Chats: []string{familyChat}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, agentauth.FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	if err = json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	for _, c := range reg["clients"].([]any) {
		if entry := c.(map[string]any); entry["id"] == client.ID {
			entry["chats"], entry["on_demand"] = []string{}, true
		}
	}
	if data, err = json.Marshal(reg); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return credential
}

func (f accessFixture) do(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.server.URL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

// signed sends what `wasync actions approve`, `wasync web` and the desktop
// app send: a request signed with token (approve.token or send.token).
func (f accessFixture) signed(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	b := []byte(nil)
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	rand.Read(nonce)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-Wasync-Time", stamp)
	req.Header.Set("X-Wasync-Nonce", hex.EncodeToString(nonce))
	req.Header.Set("X-Wasync-Signature", SignCommand(token, stamp, hex.EncodeToString(nonce), method, path, b))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

// approve is what `wasync actions approve` sends.
func (f accessFixture) approve(t *testing.T, id string, body any) int {
	t.Helper()
	code, _ := f.signed(t, "POST", "/api/actions/"+id+"/approve", f.approver, body)
	return code
}

func (f accessFixture) group(t *testing.T, id string) storage.Group {
	t.Helper()
	groups, err := f.store.GroupSummaries(context.Background(), f.account)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("chat %s missing", id)
	return storage.Group{}
}

func mustClient(t *testing.T, dir, name string) *agentauth.Client {
	t.Helper()
	c, err := agentauth.ActiveByName(dir, name)
	if err != nil || c == nil {
		t.Fatalf("client %s: %v", name, err)
	}
	return c
}

func mustOpen(t *testing.T, path string) io.Reader {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b)
}

// askSend queues the "jit" agent's request to send text to chat, as
// POST /api/v1/send-requests would. text is the agent's own, untrusted
// wording.
func (f accessFixture) askSend(t *testing.T, chat, text string) approval.Request {
	t.Helper()
	id, _, err := agentauth.ParseCredential(f.jit)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := f.queue.AddSend(f.account, chat, approval.AgentSend{Agent: "jit", AgentID: id, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
