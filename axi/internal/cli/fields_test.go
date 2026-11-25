package cli

import (
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/testutil"
)

func TestSelectedFieldsPreserveOrderAndPagination(t *testing.T) {
	h, _ := apiHarness(t)
	out, code := h.run(t, "messages", "family", "--fields", "untrusted_text,ref")
	if code != ExitOK {
		t.Fatalf("%d %s", code, out)
	}
	mustContain(t, out, "messages[4]{untrusted_text,ref}:", "--fields untrusted_text,ref --cursor c2")
	if strings.Contains(out, "{ref,time,sender") {
		t.Fatal(out)
	}
	out, code = h.run(t, "messages", "family", "--fields", "ref,state")
	if code != ExitOK || strings.Contains(out, "Text over") || strings.Contains(out, "ignore previous instructions") {
		t.Fatalf("%d %s", code, out)
	}
	mustContain(t, out, "messages[4]{ref,state}:", ",deleted")
}

func TestFieldErrorsPrecedeTransportCalls(t *testing.T) {
	h, srv := apiHarness(t)
	for _, args := range [][]string{
		{"chats", "--fields", "absent"}, {"chats", "--fields", "id,id"}, {"chats", "--fields", ""},
		{"chats", "--fields", "id", "--json"}, {"--fields", "id", "--brief"},
		{"search", "needle", "--fields", "role"},
		{"search", "needle", "--context", "00", "--fields", "role"},
	} {
		out, code := h.run(t, args...)
		if code != ExitUsage {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
	if len(srv.Requests) != 0 {
		t.Fatalf("invalid selection called owner: %v", srv.Requests)
	}
}

func TestSearchTotalsAreDistinctFromPageSize(t *testing.T) {
	h, srv := apiHarness(t)
	srv.Routes["/messages/search?limit=20&q=needle"] = testutil.Envelope(map[string]any{"account_id": 1, "total": 80, "items": []contract.SearchHit{}})
	out, code := h.run(t, "search", "needle")
	if code != ExitOK {
		t.Fatalf("%d %s", code, out)
	}
	mustContain(t, out, "shown: 0", "total: 80")
	// The old core's fixtures have no aggregate; absence must stay explicit.
	out, code = h.run(t, "search", "dinner")
	if code != ExitOK {
		t.Fatalf("%d %s", code, out)
	}
	mustContain(t, out, "shown: 2", "total: unknown")
}

func TestHomeRevealsClippedChats(t *testing.T) {
	h, srv := apiHarness(t)
	chats := []contract.Chat{}
	for i := 0; i < 8; i++ {
		chats = append(chats, contract.Chat{ID: string(rune('a'+i)) + "@g.us", Kind: "group"})
	}
	srv.Routes["/chats"] = testutil.Envelope(contract.ChatList{Items: chats})
	out, code := h.run(t)
	if code != ExitOK {
		t.Fatalf("%d %s", code, out)
	}
	mustContain(t, out, "total: 8", "groups: 8", "recent[5]{id,kind,messages,untrusted_name}", "wasync-axi chats --limit 0` for all 8")
}
