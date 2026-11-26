package approval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

// RequestTTL is how long a request waits for the person. It is long enough to
// answer after stepping away, and short enough that a request nobody wanted
// does not linger.
const RequestTTL = 30 * time.Minute

// PendingFileName is the private file that keeps waiting requests across a
// restart of the owner.
const PendingFileName = "pending-approvals.json"

const maxPendingFile = 1 << 20

type pendingFile struct {
	Version  int       `json:"version"`
	Requests []Request `json:"requests"`
}

// Persist keeps the requests that are waiting for a person in a private file in
// dir, so restarting wasync does not lose them, and loads any that were saved
// and have not expired. A request that was being carried out when wasync
// stopped is never restored, so nothing runs twice. Restored requests still
// need the same confirmation as new ones. A damaged file is ignored and
// replaced the next time something changes; the error says so.
func (q *Queue) Persist(dir string) error {
	if q == nil || dir == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dir = dir
	restored, err := loadPending(dir, time.Now())
	q.requests = append(restored, q.requests...)
	return err
}

func loadPending(dir string, now time.Time) ([]Request, error) {
	path := filepath.Join(dir, PendingFileName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err := localfs.CheckPrivateFile(path); err != nil {
		return nil, fmt.Errorf("%s must be a regular private file, so waiting requests were not restored: %w", PendingFileName, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPendingFile+1))
	if err != nil || len(data) > maxPendingFile {
		return nil, fmt.Errorf("%s is unreadable or too large, so waiting requests were not restored", PendingFileName)
	}
	var file pendingFile
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = dec.Decode(&file); err != nil || file.Version != 1 {
		return nil, fmt.Errorf("%s is damaged, so waiting requests were not restored", PendingFileName)
	}
	var out []Request
	for _, r := range file.Requests {
		if r.Status != StatusPending || !now.Before(r.Expires) || len(r.ID) != 32 || len(out) >= 32 {
			continue
		}
		// A per-chat access request from before agents used every synced
		// chat has no kind that is valid now, so it is dropped: it had
		// granted nothing yet.
		if r.Kind == KindAgentSend {
			if r.Send == nil || r.Message != "" || validateSend(r.Chat, *r.Send) != nil {
				continue
			}
		} else if r.Send != nil || r.Input.Validate() != nil {
			continue
		}
		out = append(out, r.clone())
	}
	return out, nil
}

// save writes the waiting requests, best effort: a request that cannot be saved
// still works until wasync stops. The caller holds q.mu.
func (q *Queue) save() {
	if q.dir == "" {
		return
	}
	file := pendingFile{Version: 1, Requests: []Request{}}
	for _, r := range q.requests {
		if r.Status == StatusPending {
			file.Requests = append(file.Requests, r.clone())
		}
	}
	data, err := json.Marshal(file)
	if err != nil {
		return
	}
	tmp := filepath.Join(q.dir, "."+PendingFileName+".tmp")
	f, err := localfs.OpenFile(tmp)
	if err != nil {
		return
	}
	err = f.Truncate(0)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err == nil {
		err = os.Rename(tmp, filepath.Join(q.dir, PendingFileName))
	}
	if err != nil {
		os.Remove(tmp)
	}
}
