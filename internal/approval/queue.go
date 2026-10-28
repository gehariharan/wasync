// Package approval holds expiring requests. Execution requires a separately authorized interactive approval.
package approval

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/whatsapp"
)

type Input struct {
	Kind    string `json:"kind"`
	Chat    string `json:"chat"`
	Message string `json:"message_id,omitempty"`
}

func (in Input) Validate() error {
	if _, err := whatsapp.ParseChat(in.Chat); err != nil {
		return err
	}
	switch in.Kind {
	case "clear-message", "revoke-message":
		if in.Message == "" {
			return fmt.Errorf("message ID required")
		}
	case "purge-chat", "delete-whatsapp-chat":
		if in.Message != "" {
			return fmt.Errorf("chat action cannot target a message")
		}
	default:
		return fmt.Errorf("unknown destructive action")
	}
	if len(in.Message) > 256 {
		return fmt.Errorf("message ID too long")
	}
	return nil
}

// StatusPending is a request waiting for a human.
const StatusPending = "pending_human_confirmation"

type Request struct {
	ID      string `json:"id"`
	Account int64  `json:"account_id"`
	Input
	Access  *Access   `json:"access,omitempty"`
	Expires time.Time `json:"expires_at"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
}
type Queue struct {
	mu       sync.Mutex
	requests []Request
}

func (q *Queue) Add(account int64, in Input) (Request, error) {
	if err := in.Validate(); err != nil {
		return Request{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	active := 0
	for _, r := range q.requests {
		if r.Status == StatusPending {
			active++
		}
	}
	if active >= 32 {
		return Request{}, fmt.Errorf("too many pending requests")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Request{}, err
	}
	r := Request{ID: hex.EncodeToString(id), Account: account, Input: in, Expires: time.Now().Add(5 * time.Minute), Status: StatusPending}
	q.evictOld()
	q.requests = append(q.requests, r)
	return r, nil
}

// evictOld makes room by dropping the oldest finished request once the queue is full.
func (q *Queue) evictOld() {
	if len(q.requests) >= 256 {
		for i, old := range q.requests {
			if old.Status != StatusPending && old.Status != "executing" {
				q.requests = append(q.requests[:i], q.requests[i+1:]...)
				break
			}
		}
	}
}
func (q *Queue) expire() {
	for i := range q.requests {
		if q.requests[i].Status == StatusPending && time.Now().After(q.requests[i].Expires) {
			q.requests[i].Status = "expired"
		}
	}
}
func (q *Queue) Pending(account int64) *Request {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	for _, r := range q.requests {
		if r.Account == account && r.Status == StatusPending {
			copy := r.clone()
			return &copy
		}
	}
	return nil
}
func (q *Queue) Take(account int64, id string) (Request, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	for i, r := range q.requests {
		if r.Account == account && r.ID == id && r.Status == StatusPending {
			if r.Kind == KindChatAccess && (r.Access.Chosen == "" || r.Access.History == "") {
				return Request{}, fmt.Errorf("choose the chat and the history depth before approving")
			}
			q.requests[i].Status = "executing"
			return r.clone(), nil
		}
	}
	return Request{}, fmt.Errorf("request expired or already handled")
}
func (q *Queue) Finish(id, status string, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.requests {
		if q.requests[i].ID == id {
			q.requests[i].Status = status
			if err != nil {
				q.requests[i].Error = err.Error()
			}
			return
		}
	}
}
func (q *Queue) Get(account int64, id string) *Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	for _, r := range q.requests {
		if r.Account == account && r.ID == id {
			copy := r.clone()
			return &copy
		}
	}
	return nil
}
