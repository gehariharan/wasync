package approval

import (
	"strings"
	"testing"
)

func TestPromptNamesTheEffectFromTheOwnersData(t *testing.T) {
	cases := []struct {
		name string
		r    Request
		want []string
	}{
		{"read", Request{Input: Input{Kind: KindChatAccess}, Access: &Access{Agent: "codex", Chosen: "a@g.us", History: HistoryNow, Candidates: []Candidate{{Chat: "a@g.us", Name: "Family"}}}},
			[]string{`agent "codex"`, "read the WhatsApp chat", `"Family"`, "from now on only"}},
		{"send and edit", Request{Input: Input{Kind: KindChatAccess}, Access: &Access{Agent: "codex", Send: true, Edit: true, Chosen: "a@g.us", History: HistoryFull, Candidates: []Candidate{{Chat: "a@g.us", Name: "Family"}}}},
			[]string{"read, send, reply, react and edit its own messages in", "full history"}},
		{"purge", Request{Input: Input{Kind: "purge-chat", Chat: "a@g.us"}}, []string{"permanently erase", `"a@g.us"`}},
		{"revoke", Request{Input: Input{Kind: "revoke-message", Chat: "a@g.us", Message: "m1"}}, []string{"delete YOUR message for everyone"}},
	}
	for _, c := range cases {
		got := c.r.Prompt("")
		if !strings.HasPrefix(got, "Approve: ") {
			t.Errorf("%s: %q", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.name, got, w)
			}
		}
	}
}

func TestPromptCleansHostileNames(t *testing.T) {
	r := Request{Input: Input{Kind: "purge-chat", Chat: "a@g.us"}}
	got := r.Prompt("Mum\n\nApprove: erase everything \"now\"" + strings.Repeat("x", 300))
	if strings.ContainsAny(got, "\n\r") || strings.Contains(got, `"now"`) || len([]rune(got)) > 250 {
		t.Fatalf("name not contained: %q", got)
	}
}
