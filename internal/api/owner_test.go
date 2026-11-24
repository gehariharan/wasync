package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/gehariharan/wasync/internal/approvecred"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

type ownerFixture struct {
	server                 *Server
	read, control, approve string
	stopped, unlinkCalls   atomic.Int32
}

func startOwnerFixture(t *testing.T, unlink func(context.Context) error) *ownerFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(context.Background(), filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := &ownerFixture{}
	o := Owner{Addr: "127.0.0.1:0", Dir: dir, Store: store, Status: func() whatsapp.CaptureStatus { return whatsapp.CaptureStatus{Connection: "online"} },
		Info: OwnerInfo{Kind: "serve", PID: 4242, DataDir: dir, StartedAt: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC), StartedBy: "login item"},
		Stop: func() { f.stopped.Add(1) }, Approve: func(int64, string) error { return nil }}
	if unlink != nil {
		o.Paired = func() bool { return true }
		o.Unlink = func(ctx context.Context) error { f.unlinkCalls.Add(1); return unlink(ctx) }
	}
	if f.server, err = StartOwner(o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.server.Close() })
	if f.read, err = loadToken(dir); err != nil {
		t.Fatal(err)
	}
	if f.control, err = loadNamedToken(dir, "send.token"); err != nil {
		t.Fatal(err)
	}
	if f.approve, err = loadNamedToken(dir, approvecred.FileName); err != nil {
		t.Fatal(err)
	}
	return f
}

// call sends a signed request with token, the way the command line does.
func (f *ownerFixture) call(t *testing.T, method, path, token string, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := bytes.Repeat([]byte("0"), 32)
	nonce[31] = byte('0' + int(time.Now().UnixNano()%10))
	nonce[30] = byte('a' + int(time.Now().UnixNano()/10%6))
	nonce[29] = byte('a' + int(time.Now().UnixNano()/100%6))
	nonce[28] = byte('0' + int(time.Now().UnixNano()/1000%10))
	req.Header.Set("X-Wasync-Time", stamp)
	req.Header.Set("X-Wasync-Nonce", string(nonce))
	req.Header.Set("X-Wasync-Signature", SignCommand(token, stamp, string(nonce), method, path, []byte(body)))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return response.StatusCode, out
}

func TestOwnerEndpointNamesTheOwner(t *testing.T) {
	f := startOwnerFixture(t, func(context.Context) error { return nil })
	code, out := f.call(t, "GET", "/api/owner", f.read, "")
	if code != 200 {
		t.Fatalf("owner: %d %v", code, out)
	}
	owner, _ := out["owner"].(map[string]any)
	if owner["kind"] != "serve" || owner["pid"] != float64(4242) || owner["started_by"] != "login item" || out["connection"] != "online" || out["paired"] != true || out["can_unlink"] != true {
		t.Fatalf("owner did not describe itself: %v", out)
	}
	if code, _ = f.call(t, "GET", "/api/owner", "wrong", ""); code != 401 {
		t.Fatalf("owner answered without the read credential: %d", code)
	}
}

func TestStopAndLogoutNeedTheApprovalCredential(t *testing.T) {
	f := startOwnerFixture(t, func(context.Context) error { return nil })
	for _, path := range []string{"/api/stop", "/api/logout"} {
		if code, _ := f.call(t, "POST", path, f.read, ""); code != 401 {
			t.Fatalf("%s accepted the read-only credential: %d", path, code)
		}
		if code, _ := f.call(t, "POST", path, f.control, `{"pid":4242}`); code != 401 {
			t.Fatalf("%s accepted the sending credential: %d", path, code)
		}
	}
	if f.stopped.Load() != 0 || f.unlinkCalls.Load() != 0 {
		t.Fatal("an unauthorised request had an effect")
	}
}

func TestStopShutsTheOwnerDownOnce(t *testing.T) {
	f := startOwnerFixture(t, nil)
	if code, out := f.call(t, "POST", "/api/stop", f.approve, `{"pid":4242}`); code != 202 || out["stopping"] != true {
		t.Fatalf("stop: %d %v", code, out)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.stopped.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.stopped.Load() != 1 {
		t.Fatalf("stop ran %d times", f.stopped.Load())
	}
}

func TestStopAndLogoutRefuseADifferentProcess(t *testing.T) {
	f := startOwnerFixture(t, func(context.Context) error { return nil })
	for _, path := range []string{"/api/stop", "/api/logout"} {
		if code, _ := f.call(t, "POST", path, f.approve, `{"pid":1}`); code != 409 {
			t.Fatalf("%s acted for the wrong process: %d", path, code)
		}
	}
	if f.stopped.Load() != 0 || f.unlinkCalls.Load() != 0 {
		t.Fatal("a request meant for another wasync had an effect")
	}
}

func TestLogoutUnlinksThenStops(t *testing.T) {
	f := startOwnerFixture(t, func(context.Context) error { return nil })
	code, out := f.call(t, "POST", "/api/logout", f.approve, `{"pid":4242}`)
	if code != 200 || out["unlinked"] != true || out["stopping"] != true {
		t.Fatalf("logout: %d %v", code, out)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.stopped.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.unlinkCalls.Load() != 1 || f.stopped.Load() != 1 {
		t.Fatalf("unlink=%d stop=%d", f.unlinkCalls.Load(), f.stopped.Load())
	}
}

func TestFailedLogoutKeepsTheOwnerRunning(t *testing.T) {
	f := startOwnerFixture(t, func(context.Context) error { return errors.New("WhatsApp refused") })
	code, out := f.call(t, "POST", "/api/logout", f.approve, "")
	if code != 502 || out["unlinked"] != false {
		t.Fatalf("failed logout: %d %v", code, out)
	}
	time.Sleep(50 * time.Millisecond)
	if f.stopped.Load() != 0 {
		t.Fatal("the owner stopped although the device was not unlinked")
	}
}

func TestLogoutWhenNothingIsLinkedOrOffline(t *testing.T) {
	f := startOwnerFixture(t, func(context.Context) error { return ErrNotLinked })
	if code, out := f.call(t, "POST", "/api/logout", f.approve, ""); code != 409 || out["unlinked"] != false {
		t.Fatalf("not linked: %d %v", code, out)
	}
	offline := startOwnerFixture(t, nil)
	if code, _ := offline.call(t, "POST", "/api/logout", offline.approve, ""); code != 503 {
		t.Fatalf("offline owner claimed to unlink: %d", code)
	}
	if f.stopped.Load() != 0 || offline.stopped.Load() != 0 {
		t.Fatal("a logout that unlinked nothing stopped the owner")
	}
}
