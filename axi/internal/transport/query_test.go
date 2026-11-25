package transport

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/testutil"
)

func TestQueryArgs(t *testing.T) {
	args, err := QueryArgs("", "/version", nil)
	if err != nil || !reflect.DeepEqual(args, []string{"query", "version", "--v1", "--json"}) {
		t.Fatalf("args %v err %v", args, err)
	}
	args, err = QueryArgs("/data dir", "/version", nil)
	if err != nil || !reflect.DeepEqual(args, []string{"--data-dir", "/data dir", "query", "version", "--v1", "--json"}) {
		t.Fatalf("args %v err %v", args, err)
	}
	if _, err := QueryArgs("", "/whoami", nil); codeOf(t, err) != contract.CodeOwnerOffline {
		t.Fatalf("unmapped path: %v", err)
	}
	if _, err := QueryArgs("", "/version", url.Values{"bogus": {"1"}}); codeOf(t, err) != contract.CodeInvalidArgument {
		t.Fatalf("unknown param: %v", err)
	}
	want := []string{"/changes", "/chats", "/chats/{chat}/messages", "/checkpoints/{consumer}", "/diagnostics", "/messages/search", "/messages/{ref}", "/messages/{ref}/thread", "/version"}
	if got := QueryRoutes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("routes %v", got)
	}
}

func TestQueryArgsMapsParams(t *testing.T) {
	queryRoutes["/test/chats"] = QueryRoute{Command: []string{"chats"}, Params: map[string]string{"match": "match", "limit": "limit"}}
	defer delete(queryRoutes, "/test/chats")
	args, err := QueryArgs("", "/test/chats", url.Values{"match": {"-rf"}, "limit": {"5"}})
	want := []string{"query", "chats", "--limit=5", "--match=-rf", "--v1", "--json"}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("args %v err %v", args, err)
	}
}

func TestQueryArgsReadRoutes(t *testing.T) {
	cases := []struct {
		path  string
		query url.Values
		want  string
	}{
		{"/chats", url.Values{"match": {"Team A"}}, "query chats --match=Team A --v1 --json"},
		{"/chats/" + url.PathEscape("a b@g.us") + "/messages", url.Values{"limit": {"20"}, "cursor": {"c1"}, "from": {"2026-10-01T00:00:00Z"}, "sender_id": {"x@lid"}},
			"query messages --cursor=c1 --since=2026-10-01T00:00:00Z --limit=20 --sender=x@lid --chat=a b@g.us --v1 --json"},
		{"/messages/search", url.Values{"q": {"-rf hello"}, "chat": {"one@g.us"}, "context": {"2"}},
			"query search --chat=one@g.us --context=2 --v1 --json -- -rf hello"},
		{"/messages/m1_abc", nil, "query message --ref=m1_abc --v1 --json"},
		{"/messages/m1_abc/thread", url.Values{"before": {"1"}, "after": {"3"}}, "query thread --after=3 --before=1 --ref=m1_abc --v1 --json"},
	}
	for _, c := range cases {
		args, err := QueryArgs("", c.path, c.query)
		if err != nil || strings.Join(args, " ") != c.want {
			t.Errorf("%s: %q err %v\nwant %q", c.path, strings.Join(args, " "), err, c.want)
		}
	}
	for _, path := range []string{"/chats/one@g.us", "/messages/m1_abc/other", "/messages"} {
		if _, err := QueryArgs("", path, nil); codeOf(t, err) != contract.CodeOwnerOffline {
			t.Errorf("%s: %v", path, err)
		}
	}
	if _, err := QueryArgs("", "/messages/m1_abc/thread", url.Values{"limit": {"1"}}); codeOf(t, err) != contract.CodeInvalidArgument {
		t.Fatalf("thread limit: %v", err)
	}
}

func TestQueryVersionRunsWasync(t *testing.T) {
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	t.Setenv("WASYNC_AXI_TOKEN", "wsa_should_not_leak")
	q := NewQuery(QueryConfig{Bin: bin, DataDir: t.TempDir()})
	v, err := q.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, testutil.FakeVersion()) {
		t.Fatalf("version %+v", v)
	}
	call := testutil.ReadFakeCall(t, log)
	if call.TokenPresent {
		t.Fatal("agent credential leaked into the wasync subprocess")
	}
	if len(call.Args) != 6 || call.Args[0] != "--data-dir" || strings.Join(call.Args[2:], " ") != "query version --v1 --json" {
		t.Fatalf("args %v", call.Args)
	}
}

func TestQueryFailures(t *testing.T) {
	cases := map[string]string{
		testutil.ModeOld:     contract.CodeWasyncTooOld,
		testutil.ModeGarbage: contract.CodeWasyncTooOld,
		testutil.ModeError:   contract.CodeArchiveUnavailable,
	}
	for mode, want := range cases {
		bin, _ := testutil.FakeWasync(t, mode)
		_, err := NewQuery(QueryConfig{Bin: bin}).Version(context.Background())
		if got := codeOf(t, err); got != want {
			t.Errorf("%s: code %s want %s (%v)", mode, got, want, err)
		}
	}
	bin, _ := testutil.FakeWasync(t, testutil.ModeError)
	_, err := NewQuery(QueryConfig{Bin: bin}).Version(context.Background())
	if ce := err.(*contract.Error); ce.StateString() != "unpaired" || ce.Help != "run wasync once to pair" || ce.Transport != NameQuery {
		t.Fatalf("error envelope %+v", ce)
	}
}

func TestQueryTimeout(t *testing.T) {
	bin, _ := testutil.FakeWasync(t, testutil.ModeSleep)
	start := time.Now()
	_, err := NewQuery(QueryConfig{Bin: bin, Timeout: 300 * time.Millisecond}).Version(context.Background())
	if codeOf(t, err) != contract.CodeTransport || time.Since(start) > 10*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestResolveBin(t *testing.T) {
	if _, err := ResolveBin(t.TempDir()); codeOf(t, err) != contract.CodeWasyncNotFound {
		t.Fatalf("dir accepted: %v", err)
	}
	if _, err := ResolveBin("/no/such/wasync"); codeOf(t, err) != contract.CodeWasyncNotFound {
		t.Fatalf("missing accepted: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := ResolveBin(""); codeOf(t, err) != contract.CodeWasyncNotFound {
		t.Fatalf("PATH lookup: %v", err)
	}
}
