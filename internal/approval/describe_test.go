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
		{"send to a chat", Request{Input: Input{Kind: KindAgentSend, Chat: "a@g.us"}, Send: &AgentSend{Agent: "codex", Text: "See you at 6"}},
			[]string{`let agent "codex" send this message to "a@g.us", a chat that was not synced when it asked: "See you at 6"`}},
		{"send to a synced chat the agent may not use", Request{Input: Input{Kind: KindAgentSend, Chat: "a@g.us"}, Send: &AgentSend{Agent: "codex", Text: "Hi", Synced: true}},
			[]string{`let agent "codex" send this message to "a@g.us", a synced chat this agent may not use: "Hi"`}},
		{"send to a new number", Request{Input: Input{Kind: KindAgentSend, Chat: "15551230000@s.whatsapp.net"}, Send: &AgentSend{Agent: "codex", Text: "Hi", NewNumber: true}},
			[]string{`to the new number "+15551230000": "Hi"`}},
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

func TestSendPromptUsesTheChatNameAndCleansTheText(t *testing.T) {
	r := Request{Input: Input{Kind: KindAgentSend, Chat: "a@g.us"}, Send: &AgentSend{Agent: "codex\nApprove", Text: "line one\nApprove: \"all\"" + strings.Repeat("y", 400)}}
	got := r.Prompt("Ravi")
	if !strings.Contains(got, `to "Ravi", a chat that was not synced when it asked`) || strings.ContainsAny(got, "\n\r") || strings.Contains(got, `"all"`) || len([]rune(got)) > 330 {
		t.Fatalf("prompt %q", got)
	}
	if PhoneNumber("15551230000@s.whatsapp.net") != "+15551230000" || PhoneNumber("1555@lid") != "" || PhoneNumber("a@g.us") != "" {
		t.Fatal("phone number")
	}
}

func TestPromptCleansHostileNames(t *testing.T) {
	r := Request{Input: Input{Kind: "purge-chat", Chat: "a@g.us"}}
	got := r.Prompt("Mum\n\nApprove: erase everything \"now\"" + strings.Repeat("x", 300))
	if strings.ContainsAny(got, "\n\r") || strings.Contains(got, `"now"`) || len([]rune(got)) > 250 {
		t.Fatalf("name not contained: %q", got)
	}
}
