package webhook

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

const (
	// DeadLetterFileName is the private JSONL list of events that could not be
	// delivered. Entries are identifiers and outcomes only.
	DeadLetterFileName     = "webhook-deadletter.jsonl"
	deadLetterLockFileName = "webhook-deadletter.lock"
	deadLetterMaxBytes     = 1 << 20
	maxLine                = 64 << 10
)

// DeadLetter records one event given up on. It has no field for message
// content or secrets.
type DeadLetter struct {
	Time         time.Time `json:"time"`
	EndpointID   string    `json:"endpoint_id"`
	EndpointName string    `json:"endpoint_name"`
	EventID      string    `json:"event_id"`
	Sequence     int64     `json:"sequence"`
	Op           string    `json:"op"`
	ChatID       string    `json:"chat_id"`
	Ref          string    `json:"ref,omitempty"`
	Attempts     int       `json:"attempts"`
	Code         string    `json:"error_code"`
	Status       int       `json:"http_status,omitempty"`
}

// AppendDeadLetter adds an entry under the cross-process lock, keeping the
// current file plus one rotated generation (about 2 MiB at most).
func AppendDeadLetter(dir string, d DeadLetter) (result error) {
	d.Time = d.Time.UTC().Truncate(time.Second)
	line, err := json.Marshal(d)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	lock, err := acquire(filepath.Join(dir, deadLetterLockFileName))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	path := filepath.Join(dir, DeadLetterFileName)
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() && info.Size()+int64(len(line)) > deadLetterMaxBytes {
		if err = os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	f, err := localfs.OpenFile(path)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, f.Close()) }()
	if _, err = f.Seek(0, 2); err != nil {
		return err
	}
	if _, err = f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

// ReadDeadLetters returns entries oldest first, optionally only those of one
// endpoint ID. A missing file is an empty list.
func ReadDeadLetters(dir, endpointID string) ([]DeadLetter, error) {
	out := []DeadLetter{}
	for _, name := range []string{DeadLetterFileName + ".1", DeadLetterFileName} {
		entries, err := readDeadLetterFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		for _, d := range entries {
			if endpointID == "" || d.EndpointID == endpointID {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

func readDeadLetterFile(path string) ([]DeadLetter, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err := localfs.CheckPrivateFile(path); err != nil {
		return nil, fmt.Errorf("%s must be a regular private file (0600, owner only): %w", filepath.Base(path), err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []DeadLetter
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4096), maxLine)
	for scanner.Scan() {
		var d DeadLetter
		if json.Unmarshal(scanner.Bytes(), &d) != nil {
			continue // a torn final line after a crash must not hide the rest
		}
		out = append(out, d)
	}
	return out, scanner.Err()
}

// ClearDeadLetters removes the list and returns how many entries it held.
func ClearDeadLetters(dir string) (n int, result error) {
	lock, err := acquire(filepath.Join(dir, deadLetterLockFileName))
	if err != nil {
		return 0, err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	all, err := ReadDeadLetters(dir, "")
	if err != nil {
		return 0, err
	}
	for _, name := range []string{DeadLetterFileName, DeadLetterFileName + ".1"} {
		if err = os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
	}
	return len(all), nil
}
