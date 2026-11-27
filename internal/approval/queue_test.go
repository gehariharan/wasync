package approval

import (
	"strings"
	"testing"
	"time"
)

func TestApprovalRequiresExplicitTakeAndCannotReplay(t *testing.T) {
	q := &Queue{}
	r, err := q.Add(1, Input{Kind: "purge-chat", Chat: "one@g.us"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Pending(1) == nil || q.Pending(2) != nil {
		t.Fatal("account isolation failed")
	}
	if _, err = q.Take(2, r.ID); err == nil {
		t.Fatal("other account approved request")
	}
	if _, err = q.Take(1, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Take(1, r.ID); err == nil {
		t.Fatal("approval replay accepted")
	}
	q.Finish(r.ID, "completed", nil)
	if q.Get(1, r.ID).Status != "completed" {
		t.Fatal("outcome missing")
	}
}
func TestApprovalExpiryPreventsExecution(t *testing.T) {
	q := &Queue{}
	r, err := q.Add(1, Input{Kind: "clear-message", Chat: "one@g.us", Message: "one"})
	if err != nil {
		t.Fatal(err)
	}
	q.requests[0].Expires = time.Now().Add(-time.Second)
	if _, err = q.Take(1, r.ID); err == nil {
		t.Fatal("expired request executed")
	}
	if q.Get(1, r.ID).Status != "expired" {
		t.Fatal("expired state not reported")
	}
}

func testSend(agentID, text string) AgentSend {
	return AgentSend{Agent: "codex", AgentID: agentID, Text: text}
}

func TestSendRequestIsTakenOnceAndCarriesItsMessage(t *testing.T) {
	q := &Queue{}
	r, created, err := q.AddSend(1, "15551230000@s.whatsapp.net", AgentSend{Agent: "codex", AgentID: "0123abcd", Text: "hello", NewNumber: true, MessageID: "forged"})
	if err != nil || !created || r.Kind != KindAgentSend || r.Chat != "15551230000@s.whatsapp.net" || r.Status != StatusPending || !r.IsAgentRequest() || r.Send.MessageID != "" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = q.Take(2, r.ID); err == nil {
		t.Fatal("another account took the request")
	}
	taken, err := q.Take(1, r.ID)
	if err != nil || taken.Send.Text != "hello" || !taken.Send.NewNumber {
		t.Fatalf("take %+v %v", taken, err)
	}
	if _, err = q.Take(1, r.ID); err == nil {
		t.Fatal("a send request was approved twice")
	}
	q.MarkSent(r.ID, "3EB0AA")
	q.Finish(r.ID, "completed", nil)
	if got := q.Get(1, r.ID); got.Status != "completed" || got.Send.MessageID != "3EB0AA" {
		t.Fatalf("finished %+v", got)
	}
}

func TestSendRequestDedupeCapAndValidation(t *testing.T) {
	q := &Queue{}
	first, _, _ := q.AddSend(1, "one@g.us", testSend("0123abcd", "hi"))
	again, created, err := q.AddSend(1, "one@g.us", testSend("0123abcd", "hi"))
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("the same message prompted twice: %v", err)
	}
	if other, _, _ := q.AddSend(1, "one@g.us", testSend("0123abcd", "hi again")); other.ID == first.ID {
		t.Fatal("a different message merged with the first")
	}
	for i := 2; i < MaxPendingSendsPerAgent; i++ {
		if _, _, err = q.AddSend(1, "one@g.us", testSend("0123abcd", strings.Repeat("x", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err = q.AddSend(1, "one@g.us", testSend("0123abcd", "one too many")); err == nil {
		t.Fatal("one agent queued unlimited prompts")
	}
	if _, _, err = q.AddSend(1, "one@g.us", testSend("ffff0000", "another agent")); err != nil {
		t.Fatalf("another agent was blocked: %v", err)
	}
	for name, bad := range map[string]struct {
		chat string
		s    AgentSend
	}{
		"no text":     {"one@g.us", testSend("eeee1111", "  ")},
		"long text":   {"one@g.us", testSend("eeee1111", strings.Repeat("x", MaxSendText+1))},
		"bad chat":    {"not a chat", testSend("eeee1111", "hi")},
		"no agent id": {"one@g.us", AgentSend{Agent: "codex", Text: "hi"}},
	} {
		if _, _, err = q.AddSend(1, bad.chat, bad.s); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// The copy handed out cannot alter the queue.
	got := q.Get(1, first.ID)
	got.Send.Text = "evil"
	if q.Get(1, first.ID).Send.Text != "hi" {
		t.Fatal("Get exposed queue internals")
	}
}

func TestAgentRequestsAreListedOldestFirstApartFromTheOwnersDeletes(t *testing.T) {
	var announced []string
	q := &Queue{OnSend: func(r Request) { announced = append(announced, r.ID) }}
	purge, _ := q.Add(1, Input{Kind: "purge-chat", Chat: "one@g.us"})
	newer, _, _ := q.AddSend(1, "two@g.us", testSend("0123abcd", "second"))
	older, _, _ := q.AddSend(1, "one@g.us", testSend("0123abcd", "first"))
	q.AddSend(1, "one@g.us", testSend("0123abcd", "first")) // a repeat is not announced again
	q.AddSend(2, "one@g.us", testSend("0123abcd", "other account"))
	q.mu.Lock()
	for i := range q.requests {
		if q.requests[i].ID == older.ID {
			q.requests[i].Expires = q.requests[i].Expires.Add(-time.Minute)
		}
	}
	q.mu.Unlock()
	if len(announced) != 3 {
		t.Fatalf("announced %v", announced)
	}
	list := q.AgentRequests(1)
	if len(list) != 2 || list[0].ID != older.ID || list[1].ID != newer.ID {
		t.Fatalf("agent requests %+v", list)
	}
	declined := q.RejectAgentRequests(1)
	if len(declined) != 2 || len(q.AgentRequests(1)) != 0 || q.Get(1, older.ID).Status != StatusRejected {
		t.Fatalf("declined %+v", declined)
	}
	if q.Get(1, purge.ID).Status != StatusPending || len(q.AgentRequests(2)) != 1 {
		t.Fatal("declining every agent request touched the owner's delete or another account")
	}
	if _, err := q.Take(1, older.ID); err == nil {
		t.Fatal("a declined send was approved")
	}
	if len(q.RejectAgentRequests(1)) != 0 {
		t.Fatal("declining twice declined something")
	}
}

func TestSendRequestExpiryChangesNothing(t *testing.T) {
	q := &Queue{}
	r, _, _ := q.AddSend(1, "one@g.us", testSend("0123abcd", "hi"))
	q.mu.Lock()
	q.requests[0].Expires = time.Now().Add(-time.Second)
	q.mu.Unlock()
	if _, err := q.Take(1, r.ID); err == nil || q.Get(1, r.ID).Status != "expired" {
		t.Fatal("an expired send request was approved")
	}
	if q.Reject(1, r.ID) == nil {
		t.Fatal("an expired request was declined")
	}
}
