package agentauth

import (
	"testing"
	"time"
)

func TestAddAndRemoveSendKeepTheCredentialAndEveryGrant(t *testing.T) {
	dir := privateDir(t)
	cred, c := legacyOnDemand(t, dir, "helper", []string{ScopeReadChats, ScopeReadMessages}, ChatGrant{Chat: chatA, Scopes: []string{ScopeReadMessages}, GrantedAt: time.Now().UTC().Truncate(time.Second)})
	a := NewAuthorizer(dir)
	agent, err := a.Authenticate("Bearer " + cred)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Authorize(agent, ScopeSend, chatA); code(err) != CodeForbidden {
		t.Fatalf("send before it was added: %v", err)
	}
	added, err := AddScope(dir, "helper", ScopeSend, time.Now())
	if err != nil || !added.HasScope(ScopeSend) || !added.HasScope(ScopeReadMessages) {
		t.Fatalf("add: %+v %v", added, err)
	}
	// Adding a scope widens what the agent may do, but not where: the chat it was
	// granted for reading is still its only chat for sending.
	if err = a.Authorize(agent, ScopeSend, chatA); code(err) != CodeForbidden {
		t.Fatalf("a read grant must not become a send grant: %v", err)
	}
	// A grant that carried send, approved earlier, still allows it.
	if err := update(dir, func(reg *registry) error {
		for i := range reg.Clients {
			if reg.Clients[i].ID == c.ID {
				reg.Clients[i].Grants[0].Scopes = []string{ScopeReadMessages, ScopeSend}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = a.Authorize(agent, ScopeSend, chatA); err != nil {
		t.Fatalf("send in a chat approved for sending: %v", err)
	}
	if again, err := AddScope(dir, "helper", ScopeSend, time.Now()); err != nil || len(again.Scopes) != len(added.Scopes) {
		t.Fatalf("adding twice duplicated the scope: %+v %v", again, err)
	}
	removed, err := RemoveScope(dir, "helper", ScopeSend, time.Now())
	if err != nil || removed.HasScope(ScopeSend) || !removed.HasScope(ScopeReadMessages) {
		t.Fatalf("remove: %+v %v", removed, err)
	}
	if err = a.Authorize(agent, ScopeReadMessages, chatA); err != nil {
		t.Fatalf("reading must survive removing send: %v", err)
	}
	if err = a.Authorize(agent, ScopeSend, chatA); code(err) != CodeForbidden {
		t.Fatalf("send after removal: %v", err)
	}
	for _, g := range removed.Grants {
		for _, s := range g.Scopes {
			if s == ScopeSend {
				t.Fatalf("a chat grant kept the removed scope: %+v", g)
			}
		}
	}
	if _, err = RemoveScope(dir, "helper", ScopeSend, time.Now()); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
}

func TestAddScopeRefusesWhatItMustNotWiden(t *testing.T) {
	dir := privateDir(t)
	grant(t, dir, GrantRequest{Name: "everything", Scopes: []string{ScopeReadChats}})
	grant(t, dir, GrantRequest{Name: "helper", Scopes: []string{ScopeReadChats}, SyncedChats: true})
	if _, err := AddScope(dir, "everything", ScopeSend, time.Now()); err == nil {
		t.Fatal("an agent that reads every chat was given sending")
	}
	for _, scope := range []string{ScopeEditOwn, ScopeReadMessages, "made-up"} {
		if _, err := AddScope(dir, "helper", scope, time.Now()); err == nil {
			t.Fatalf("scope %s could be added this way", scope)
		}
	}
	if _, err := AddScope(dir, "nobody", ScopeSend, time.Now()); err == nil {
		t.Fatal("an unknown agent was changed")
	}
	if _, err := Revoke(dir, "helper", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := AddScope(dir, "helper", ScopeSend, time.Now()); err == nil {
		t.Fatal("a revoked agent was given sending")
	}
}

func TestOpenSendNeedsTheSendScopeAndIsAuditedAndRateLimited(t *testing.T) {
	dir := privateDir(t)
	a := NewAuthorizer(dir)
	cred, _ := grant(t, dir, GrantRequest{Name: "poster", Scopes: []string{ScopeSend}, Chats: []string{chatA}, RatePerMinute: 2})
	readCred, _ := grant(t, dir, GrantRequest{Name: "reader", Scopes: []string{ScopeReadChats}})
	poster, _ := a.Authenticate("Bearer " + cred)
	reader, _ := a.Authenticate("Bearer " + readCred)
	if err := a.AuthorizeOpenSend(poster, chatB, "r1"); err != nil {
		t.Fatalf("open send: %v", err)
	}
	if err := a.AuthorizeOpenSend(reader, chatB, "r2"); code(err) != CodeForbidden {
		t.Fatalf("an agent without send: %v", err)
	}
	if err := a.AuthorizeOpenSend(nil, chatB, "r3"); code(err) != CodeUnauthorized {
		t.Fatalf("no credential: %v", err)
	}
	if err := a.AuthorizeOpenSend(poster, chatB, "r4"); err != nil {
		t.Fatalf("second open send: %v", err)
	}
	if err := a.AuthorizeOpenSend(poster, chatB, "r5"); code(err) != CodeRateLimited {
		t.Fatalf("open sends must share the per-minute limit: %v", err)
	}
	var allowed, denied int
	for _, e := range readAudit(t, dir+"/"+AuditFileName) {
		if e["event"] != "send_open" {
			continue
		}
		if e["outcome"] == "allowed" {
			allowed++
		} else {
			denied++
		}
	}
	if allowed != 2 || denied != 3 {
		t.Fatalf("audit: %d allowed, %d denied", allowed, denied)
	}
}
