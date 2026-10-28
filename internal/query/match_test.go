package query

import (
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/storage"
)

func TestMatchChatsTiers(t *testing.T) {
	groups := []storage.Group{{ID: "a@g.us", Name: "Family"}, {ID: "b@g.us", Name: " family office"}, {ID: "c@g.us", Name: "The Family"}, {ID: "family@s.whatsapp.net", Name: "Ops"}, {ID: "d@g.us", Name: "Opsec"}}
	for match, want := range map[string]string{
		"family@s.whatsapp.net": "family@s.whatsapp.net", // tier 1 beats every name tier
		" FAMILY ":              "a@g.us",                // tier 2 beats the prefix "family office"
		"family o":              "b@g.us",                // tier 3, leading space in the name ignored
		"he fam":                "c@g.us",                // tier 4
		"ops":                   "family@s.whatsapp.net", // tier 2 beats the prefix Opsec
		"op":                    "family@s.whatsapp.net,d@g.us",
		"fam":                   "a@g.us,b@g.us",
		"ily":                   "a@g.us,b@g.us,c@g.us",
		"none":                  "",
		"  ":                    "",
	} {
		out := []string{}
		for _, g := range MatchChats(groups, match) {
			out = append(out, g.ID)
		}
		if got := strings.Join(out, ","); got != want {
			t.Errorf("%q: %q want %q", match, got, want)
		}
	}
	list, amb := V1Chats(1, groups, false, "fam", func(chat string) bool { return chat != "b@g.us" })
	if amb != nil || len(list.Items) != 1 || list.Items[0].ID != "a@g.us" {
		t.Fatalf("allowlist applied after matching: %+v %+v", list, amb)
	}
	if _, amb = V1Chats(1, groups, false, "op", nil); amb == nil || len(amb.Candidates) != 2 || amb.Candidates[0].Kind != "direct" || amb.Error() != `chat name "op" matches 2 chats` {
		t.Fatalf("ambiguity %+v", amb)
	}
}

func TestMatchChatsFindsDirectChatsByContactName(t *testing.T) {
	groups := []storage.Group{
		{ID: "15550000001@s.whatsapp.net", Name: "Synthetic Uncle"},
		{ID: "90000000001@lid", Name: "synthetic aunt"},
		{ID: "15550000002@s.whatsapp.net", Name: "15550000002@s.whatsapp.net"},
	}
	for match, want := range map[string]string{
		"synthetic uncle": "15550000001@s.whatsapp.net",
		"AUNT":            "90000000001@lid",
		"synthetic":       "15550000001@s.whatsapp.net,90000000001@lid",
	} {
		out := []string{}
		for _, g := range MatchChats(groups, match) {
			out = append(out, g.ID)
		}
		if got := strings.Join(out, ","); got != want {
			t.Errorf("%q: %q want %q", match, got, want)
		}
	}
}
