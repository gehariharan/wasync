package approval

import (
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

func testAccess(query string) Access {
	return Access{Agent: "jit", AgentID: "0123abcd", Scopes: []string{"read.chats", "read.messages"}, Query: query, Candidates: []Candidate{{Chat: "one@g.us", Name: "One"}, {Chat: "two@g.us", Name: "Two"}}}
}

func TestAccessRequestNeedsAChoiceAndCannotReplay(t *testing.T) {
	q := &Queue{}
	r, err := q.AddAccess(1, testAccess("o"))
	if err != nil || r.Kind != KindChatAccess || r.Chat != "" || r.Status != StatusPending {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = q.Take(1, r.ID); err == nil {
		t.Fatal("approved without a chosen chat and history depth")
	}
	if q.Get(1, r.ID).Status != StatusPending {
		t.Fatal("refused approval consumed the request")
	}
	for _, bad := range [][2]string{{"three@g.us", HistoryNow}, {"one@g.us", ""}, {"one@g.us", "everything"}} {
		if q.Decide(1, r.ID, bad[0], bad[1]) == nil {
			t.Fatalf("accepted choice %v", bad)
		}
	}
	if q.Decide(2, r.ID, "one@g.us", HistoryNow) == nil {
		t.Fatal("other account decided")
	}
	if err = q.Decide(1, r.ID, "two@g.us", HistoryFull); err != nil {
		t.Fatal(err)
	}
	taken, err := q.Take(1, r.ID)
	if err != nil || taken.Access.Chosen != "two@g.us" || taken.Access.History != HistoryFull {
		t.Fatalf("%+v %v", taken, err)
	}
	if _, err = q.Take(1, r.ID); err == nil {
		t.Fatal("approval replay accepted")
	}
	if q.Decide(1, r.ID, "one@g.us", HistoryNow) == nil {
		t.Fatal("decided after execution began")
	}
}

func TestAccessRequestDedupeCapAndValidation(t *testing.T) {
	q := &Queue{}
	first, _ := q.AddAccess(1, testAccess("Alice  Smith"))
	again, err := q.AddAccess(1, testAccess(" alice smith "))
	if err != nil || again.ID != first.ID {
		t.Fatalf("duplicate prompted twice: %v", err)
	}
	send := testAccess("alice smith")
	send.Send = true
	if sent, _ := q.AddAccess(1, send); sent.ID == first.ID {
		t.Fatal("send request merged with a read request")
	}
	if _, err = q.AddAccess(1, testAccess("third")); err != nil {
		t.Fatal(err)
	}
	if _, err = q.AddAccess(1, testAccess("fourth")); err == nil {
		t.Fatal("one agent queued unlimited prompts")
	}
	other := testAccess("fourth")
	other.AgentID = "ffff0000"
	if _, err = q.AddAccess(1, other); err != nil {
		t.Fatalf("another agent was blocked: %v", err)
	}
	empty := testAccess("none")
	empty.AgentID = "eeee1111"
	empty.Candidates = nil
	none, err := q.AddAccess(1, empty)
	if err != nil || !none.NoMatch() || none.Chat != "" {
		t.Fatalf("a request that matched nothing must queue like any other: %+v %v", none, err)
	}
	if q.Decide(1, none.ID, "one@g.us", HistoryNow) == nil {
		t.Fatal("decided a request with no candidates")
	}
	if _, err = q.Take(1, none.ID); err == nil {
		t.Fatal("approved a request that matched nothing")
	}
	if q.Reject(1, none.ID) != nil {
		t.Fatal("a no-match request cannot be rejected")
	}
	bad := testAccess("x")
	bad.Candidates = []Candidate{{Chat: "not a jid"}}
	if _, err = q.AddAccess(1, bad); err == nil {
		t.Fatal("accepted a malformed candidate")
	}
	// The copy handed out cannot alter the queue.
	got := q.Get(1, first.ID)
	got.Access.Candidates[0].Chat = "evil@g.us"
	if q.Get(1, first.ID).Access.Candidates[0].Chat != "one@g.us" {
		t.Fatal("Get exposed queue internals")
	}
}

func TestAccessRequestRejectAndExpiryChangeNothing(t *testing.T) {
	q := &Queue{}
	r, _ := q.AddAccess(1, testAccess("o"))
	if err := q.Reject(1, r.ID); err != nil || q.Get(1, r.ID).Status != "rejected" {
		t.Fatalf("reject: %v", err)
	}
	if q.Decide(1, r.ID, "one@g.us", HistoryNow) == nil {
		t.Fatal("decided after rejection")
	}
	e, _ := q.AddAccess(1, testAccess("e"))
	q.mu.Lock()
	for i := range q.requests {
		if q.requests[i].ID == e.ID {
			q.requests[i].Expires = time.Now().Add(-time.Second)
		}
	}
	q.mu.Unlock()
	if q.Decide(1, e.ID, "one@g.us", HistoryNow) == nil {
		t.Fatal("decided after expiry")
	}
	if _, err := q.Take(1, e.ID); err == nil || q.Get(1, e.ID).Status != "expired" {
		t.Fatal("expired access request executed")
	}
}

func TestAccessAnnouncementAndPendingList(t *testing.T) {
	var seen []string
	q := &Queue{OnAccess: func(r Request) { seen = append(seen, r.ID) }}
	first, _ := q.AddAccess(1, testAccess("a"))
	q.AddAccess(1, testAccess("A")) // repeat of the same ask
	q.AddAccess(1, testAccess("b")) // a different ask
	q.AddAccess(1, Access{})        // invalid, never announced
	q.Add(1, Input{Kind: "purge-chat", Chat: "one@g.us"})
	if len(seen) != 2 || seen[0] != first.ID {
		t.Fatalf("announced %v", seen)
	}
	list := q.PendingList(1)
	if len(list) != 3 || list[0].ID != first.ID || len(q.PendingList(2)) != 0 {
		t.Fatalf("list %+v", list)
	}
	if err := q.Reject(1, first.ID); err != nil || len(q.PendingList(1)) != 2 {
		t.Fatal("rejected request still listed")
	}
	list = q.PendingList(1)
	list[0].Access.Candidates[0].Chat = "evil@g.us"
	if q.PendingList(1)[0].Access.Candidates[0].Chat == "evil@g.us" {
		t.Fatal("list exposed queue internals")
	}
}
