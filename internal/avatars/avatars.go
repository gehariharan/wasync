// Package avatars keeps small profile pictures of chats for wasync's own pages.
//
// Pictures are fetched only when a page asks for one, one WhatsApp request at
// a time and never faster than Interval, because bursts of requests are what
// get unofficial clients flagged. Each picture is cached in the data directory
// and asked about again only after TTL, with the cached picture's ID, so an
// unchanged picture costs WhatsApp no download. A chat with no picture, or one
// hidden from this account, is remembered the same way, and pages show the
// chat's initials instead.
package avatars

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

// DirName is the folder in the data directory that holds the cached pictures.
const DirName = "avatars"

// Picture is what WhatsApp answered about one chat's picture.
type Picture struct {
	// ID is WhatsApp's identifier for the picture; "" when there is none.
	ID string
	// Data is the JPEG preview; nil when Unchanged or when there is none.
	Data []byte
	// Unchanged means the picture is still the one asked about by its ID.
	Unchanged bool
}

// Fetcher asks WhatsApp for one chat's preview picture; knownID is the cached
// picture's ID, or "".
type Fetcher func(ctx context.Context, chat, knownID string) (Picture, error)

const (
	// DefaultInterval is the least time between two WhatsApp requests.
	DefaultInterval = 500 * time.Millisecond
	// DefaultTTL is how long a cached answer is used before asking again.
	DefaultTTL = 24 * time.Hour
	// DefaultRetry is how long a failed request is not repeated for a chat.
	DefaultRetry = 10 * time.Minute
	// maxQueue bounds the chats waiting for a request; more are not queued.
	maxQueue = 200
	// fetchTimeout bounds one request and its download.
	fetchTimeout = 20 * time.Second
	// idleExit is how long the worker waits for more work before it stops.
	idleExit   = time.Minute
	maxPicture = 256 << 10
)

// Cache is the picture cache of one data directory.
type Cache struct {
	dir   string
	fetch Fetcher
	// Interval, TTL and Retry pace requests; New sets the defaults.
	Interval, TTL, Retry time.Duration
	now                  func() time.Time

	mu      sync.Mutex
	queue   []key
	waiting map[key]chan struct{}
	failed  map[key]time.Time
	running bool
	wake    chan struct{}
}

type key struct {
	account int64
	chat    string
}

type meta struct {
	Chat      string    `json:"chat"`
	ID        string    `json:"id,omitempty"`
	None      bool      `json:"none,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

// New returns the cache kept under dataDir. fetch may be nil, for an owner
// with no WhatsApp connection: then only cached pictures are served.
func New(dataDir string, fetch Fetcher) *Cache {
	return &Cache{dir: filepath.Join(dataDir, DirName), fetch: fetch, Interval: DefaultInterval, TTL: DefaultTTL, Retry: DefaultRetry, now: time.Now, waiting: map[key]chan struct{}{}, failed: map[key]time.Time{}, wake: make(chan struct{}, 1)}
}

func (c *Cache) paths(k key) (dir, image, info string) {
	sum := sha256.Sum256([]byte(k.chat))
	dir = filepath.Join(c.dir, strconv.FormatInt(k.account, 10))
	name := hex.EncodeToString(sum[:])
	return dir, filepath.Join(dir, name+".jpg"), filepath.Join(dir, name+".json")
}

// Picture returns chat's cached preview, or nil when the chat has no picture
// or none is known yet. A missing or expired answer is asked for in the
// background; when nothing is cached yet it waits up to wait for the answer.
func (c *Cache) Picture(ctx context.Context, account int64, chat string, wait time.Duration) []byte {
	k := key{account, chat}
	m, data, ok := c.load(k)
	if ok && c.now().Sub(m.FetchedAt) < c.TTL {
		return data
	}
	done := c.request(k)
	if done == nil || data != nil || wait <= 0 {
		return data
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return nil
	}
	_, data, _ = c.load(k)
	return data
}

// request queues a request for k unless one is queued, the last one failed
// recently, there is no fetcher or the queue is full. It returns a channel
// closed when the request is done, or nil when nothing was queued.
func (c *Cache) request(k key) chan struct{} {
	if c.fetch == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if done, ok := c.waiting[k]; ok {
		return done
	}
	if at, ok := c.failed[k]; ok && c.now().Sub(at) < c.Retry {
		return nil
	}
	if len(c.queue) >= maxQueue {
		return nil
	}
	done := make(chan struct{})
	c.waiting[k] = done
	c.queue = append(c.queue, k)
	if !c.running {
		c.running = true
		go c.work()
	} else {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return done
}

// work answers queued requests one at a time, at most one per Interval, and
// stops after a minute without work.
func (c *Cache) work() {
	var last time.Time
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.mu.Unlock()
			select {
			case <-c.wake:
				continue
			case <-time.After(idleExit):
			}
			c.mu.Lock()
			if len(c.queue) == 0 {
				c.running = false
				c.mu.Unlock()
				return
			}
		}
		k := c.queue[0]
		c.queue = c.queue[1:]
		c.mu.Unlock()
		if gap := c.Interval - time.Since(last); !last.IsZero() && gap > 0 {
			time.Sleep(gap)
		}
		last = time.Now()
		err := c.refresh(k)
		c.mu.Lock()
		if err != nil {
			c.failed[k] = c.now()
		} else {
			delete(c.failed, k)
		}
		close(c.waiting[k])
		delete(c.waiting, k)
		c.mu.Unlock()
	}
}

// refresh asks WhatsApp about one chat's picture and saves the answer.
func (c *Cache) refresh(k key) error {
	known, _, ok := c.load(k)
	knownID := ""
	if ok {
		knownID = known.ID
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	p, err := c.fetch(ctx, k.chat, knownID)
	if err != nil {
		return err
	}
	m := meta{Chat: k.chat, ID: p.ID, FetchedAt: c.now().UTC()}
	switch {
	case p.Unchanged && ok && knownID != "":
		m.ID = knownID
		return c.saveMeta(k, m)
	case p.ID == "" || len(p.Data) == 0:
		m.ID, m.None = "", true
		_, image, _ := c.paths(k)
		if err = os.Remove(image); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return c.saveMeta(k, m)
	case len(p.Data) > maxPicture:
		return fmt.Errorf("picture is larger than a preview")
	}
	dir, image, _ := c.paths(k)
	if err = localfs.EnsureDir(c.dir); err != nil {
		return err
	}
	if err = localfs.EnsureDir(dir); err != nil {
		return err
	}
	if err = writeFile(image, p.Data); err != nil {
		return err
	}
	return c.saveMeta(k, m)
}

func (c *Cache) saveMeta(k key, m meta) error {
	dir, _, info := c.paths(k)
	if err := localfs.EnsureDir(c.dir); err != nil {
		return err
	}
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFile(info, data)
}

// load reads the cached answer for k. ok is false when there is none usable.
func (c *Cache) load(k key) (m meta, data []byte, ok bool) {
	_, image, info := c.paths(k)
	raw, err := readFile(info, 4<<10)
	if err != nil || json.Unmarshal(raw, &m) != nil || m.Chat != k.chat {
		return meta{}, nil, false
	}
	if m.None {
		return m, nil, true
	}
	if data, err = readFile(image, maxPicture); err != nil || len(data) == 0 {
		return meta{}, nil, false
	}
	return m, data, true
}

func readFile(path string, limit int64) ([]byte, error) {
	if err := localfs.CheckPrivateFile(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is too large", filepath.Base(path))
	}
	return data, nil
}

// writeFile replaces path atomically with a private file holding data.
func writeFile(path string, data []byte) (result error) {
	tmp := path + ".tmp"
	f, err := localfs.OpenFile(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			os.Remove(tmp)
		}
	}()
	if err = f.Truncate(0); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
