package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestEnablingAutomaticallyQueuesHistoryAndDisableCancels(t *testing.T) {
	s, account := captureStore(t)
	ctx := context.Background()
	job, err := s.NextHistory(ctx, account)
	if err != nil || job == nil || job.GroupID != "one@g.us" {
		t.Fatalf("enabling did not queue history: %+v %v", job, err)
	}
	anchor := HistoryAnchor{ID: "original", Timestamp: time.Now()}
	started, err := s.StartHistoryRequest(ctx, account, job.GroupID, "request", anchor)
	if err != nil || !started {
		t.Fatal(err)
	}
	if err = s.QueueHistory(ctx, account, job.GroupID); err == nil {
		t.Fatal("duplicate pending request accepted")
	}
	if err = s.SetEnabled(ctx, account, []string{job.GroupID}, false); err != nil {
		t.Fatal(err)
	}
	if err = s.HistoryRequestSent(ctx, account, "request", nil); err != nil {
		t.Fatal(err)
	}
	job.RequestID = "request"
	job.Anchor = anchor
	if err = s.FinishHistoryRequest(ctx, account, *job, HistoryAnchor{ID: "older", Timestamp: anchor.Timestamp.Add(-time.Hour)}, 1); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingHistory(ctx, account)
	if err != nil || pending != nil {
		t.Fatal("disabled group retained pending request")
	}
	summaries, err := s.GroupSummaries(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range summaries {
		if g.ID == job.GroupID && g.HistoryRequest != "disabled" {
			t.Fatal("late response reactivated disabled job")
		}
	}
	if err = s.SetEnabled(ctx, account, []string{job.GroupID}, true); err != nil {
		t.Fatal(err)
	}
	job, err = s.NextHistory(ctx, account)
	if err != nil || job == nil {
		t.Fatal("re-enabling did not queue history")
	}
}
func TestHistoryTimeoutAndTransportFailureRemainUncertain(t *testing.T) {
	s, account := captureStore(t)
	ctx := context.Background()
	anchor := HistoryAnchor{ID: "anchor", Timestamp: time.Now()}
	if _, err := s.StartHistoryRequest(ctx, account, "one@g.us", "request", anchor); err != nil {
		t.Fatal(err)
	}
	if err := s.HistoryRequestSent(ctx, account, "request", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.TimeoutHistoryRequests(ctx, account, time.Now().Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	groups, err := s.GroupSummaries(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID == "one@g.us" && g.HistoryRequest != "timed_out" {
			t.Fatal("no response was treated as successful sync")
		}
	}
	if err = s.QueueHistory(ctx, account, "one@g.us"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartHistoryRequest(ctx, account, "one@g.us", "second", anchor); err != nil {
		t.Fatal(err)
	}
	if err = s.HistoryRequestSent(ctx, account, "second", errors.New("transport secret must not be stored")); err != nil {
		t.Fatal(err)
	}
	var state, message string
	if err = s.db.QueryRow("SELECT state,last_error FROM history_jobs WHERE group_id='one@g.us'").Scan(&state, &message); err != nil || state != "failed" || message == "transport secret must not be stored" {
		t.Fatal("transport failure not safely recorded")
	}
}
func TestHistoryProgressAndInterruptedRequestResume(t *testing.T) {
	s, account := captureStore(t)
	ctx := context.Background()
	anchor := HistoryAnchor{ID: "anchor", Timestamp: time.Now()}
	_, err := s.StartHistoryRequest(ctx, account, "one@g.us", "request", anchor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartCapture(ctx, account); err != nil {
		t.Fatal(err)
	}
	job, err := s.NextHistory(ctx, account)
	if err != nil || job == nil || job.Anchor.ID != "anchor" {
		t.Fatal("interrupted request lost its cursor")
	}
	if _, err = s.StartHistoryRequest(ctx, account, "one@g.us", "new", job.Anchor); err != nil {
		t.Fatal(err)
	}
	job.RequestID = "new"
	older := HistoryAnchor{ID: "older", Timestamp: anchor.Timestamp.Add(-time.Hour), FromMe: true}
	if err = s.FinishHistoryRequest(ctx, account, *job, older, 50); err != nil {
		t.Fatal(err)
	}
	next, err := s.NextHistory(ctx, account)
	if err != nil || next == nil || next.Anchor.ID != "older" || !next.Anchor.FromMe {
		t.Fatal("response did not advance queued cursor")
	}
	if _, err = s.StartHistoryRequest(ctx, account, next.GroupID, "last", next.Anchor); err != nil {
		t.Fatal(err)
	}
	next.RequestID = "last"
	if err = s.FinishHistoryRequest(ctx, account, *next, next.Anchor, 50); err != nil {
		t.Fatal(err)
	}
	next, err = s.NextHistory(ctx, account)
	if err != nil || next != nil {
		t.Fatal("repeated page caused an unbounded request loop")
	}
}
func TestMigrationQueuesPreviouslyEnabledGroups(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "archive.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the exact version-2 boundary, with no cursor/history-job schema.
	if _, err = s.db.Exec("DROP TABLE history_anchors;DROP TABLE history_jobs;PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	account, err := s.UseAccount(context.Background(), "own@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGroups(context.Background(), account, []Group{{ID: "one@g.us", Name: "existing"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE groups SET sync_enabled=1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.NextHistory(context.Background(), account)
	if err != nil || job == nil || job.GroupID != "one@g.us" {
		t.Fatal("existing enabled group was not bootstrapped")
	}
}

func TestEnableCaptureHistoryDepth(t *testing.T) {
	s, account := captureStore(t)
	ctx := context.Background()
	state := func(id string) string {
		t.Helper()
		groups, err := s.GroupSummaries(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if g.ID == id {
				if !g.Enabled {
					return "off:" + g.HistoryRequest
				}
				return "on:" + g.HistoryRequest
			}
		}
		t.Fatalf("%s missing", id)
		return ""
	}
	// From now on only: capture starts, no history request is queued.
	if err := s.EnableCapture(ctx, account, "two@g.us", false); err != nil || state("two@g.us") != "on:" {
		t.Fatalf("now only: %v %q", err, state("two@g.us"))
	}
	// Full history on a chat that is already capturing queues one request.
	if err := s.EnableCapture(ctx, account, "two@g.us", true); err != nil || state("two@g.us") != "on:queued" {
		t.Fatalf("full on enabled chat: %v %q", err, state("two@g.us"))
	}
	// A request already in flight is left alone instead of failing or restarting.
	job, err := s.NextHistory(ctx, account)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	started, err := s.StartHistoryRequest(ctx, account, job.GroupID, "request", HistoryAnchor{ID: "original", Timestamp: time.Now()})
	if err != nil || !started {
		t.Fatal(err)
	}
	if err = s.EnableCapture(ctx, account, job.GroupID, true); err != nil || state(job.GroupID) != "on:sending" {
		t.Fatalf("in-flight request disturbed: %v %q", err, state(job.GroupID))
	}
	// Plain enabling keeps its old behaviour: history on the off to on transition only.
	if err = s.SetEnabled(ctx, account, []string{"two@g.us"}, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, account, []string{"two@g.us"}, true); err != nil || state("two@g.us") != "on:queued" {
		t.Fatalf("SetEnabled history: %v %q", err, state("two@g.us"))
	}
	if err = s.EnableCapture(ctx, account, "missing@g.us", false); err == nil {
		t.Fatal("enabled an uncached chat")
	}
}
