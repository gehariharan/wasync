package accessmode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDefaultsToAskAndValidatesChoices(t *testing.T) {
	dir := privateDir(t)
	m, chosen, err := Load(dir)
	if err != nil || chosen || m.Mode != Ask || m.AutoApproves() {
		t.Fatalf("default %+v %v %v", m, chosen, err)
	}
	for _, bad := range []Mode{{}, {Mode: "everything"}, {Mode: All}, {Mode: All, History: "forever"}, {Mode: Ask, History: HistoryNow}} {
		if bad.Validate() == nil || Save(dir, bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err = os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatal("an invalid choice was saved")
	}
	for _, good := range []Mode{{Mode: Ask}, {Mode: All, History: HistoryNow}, {Mode: All, History: HistoryFull}} {
		if err = Save(dir, good); err != nil {
			t.Fatal(err)
		}
		got, chosen, err := Load(dir)
		if err != nil || !chosen || got.Mode != good.Mode || got.History != good.History || got.SetAt.IsZero() || got.AutoApproves() != (good.Mode == All) {
			t.Fatalf("round trip %+v -> %+v %v %v", good, got, chosen, err)
		}
	}
}

func TestDamagedOrExposedSettingFailsClosedToAsk(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, FileName)
	for _, content := range []string{`not json`, `{"mode":"all"}`, `{"mode":"all","history":"now","extra":1}`, `{"mode":"ask","history":"full"}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		m, chosen, err := Load(dir)
		if err == nil || chosen || m.AutoApproves() {
			t.Fatalf("%q: %+v %v %v", content, m, chosen, err)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.WriteFile(path, []byte(`{"mode":"all","history":"now"}`), 0o666); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o666)
	if m, _, err := Load(dir); err == nil || m.AutoApproves() {
		t.Fatalf("a group-writable setting enabled auto approval: %+v %v", m, err)
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 == 0 {
		t.Fatal("test setup should have left a loose file")
	}
	if err := Save(dir, Mode{Mode: Ask}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("saved setting is not private: %v", info.Mode())
	}
}

func TestApplyAllEnablesEveryChatWithTheChosenHistoryDepth(t *testing.T) {
	ctx := context.Background()
	dir := privateDir(t)
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, _ := store.UseAccount(ctx, "synthetic@lid")
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: "one@g.us", Name: "One"}, {ID: "two@g.us", Name: "Two"}, {ID: "15551234567@s.whatsapp.net", Name: "Pat"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetEnabled(ctx, account, []string{"one@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	history := func() map[string]string {
		out := map[string]string{}
		groups, _ := store.GroupSummaries(ctx, account)
		for _, g := range groups {
			if !g.Enabled {
				out[g.ID] = "off"
			} else {
				out[g.ID] = "on:" + g.HistoryRequest
			}
		}
		return out
	}
	// Choosing ask never touches capture.
	if n, err := Apply(ctx, store, dir, account, Mode{Mode: Ask}); err != nil || n != 0 || history()["two@g.us"] != "off" {
		t.Fatalf("ask: %d %v %v", n, err, history())
	}
	if _, err = Apply(ctx, store, dir, account, Mode{Mode: All}); err == nil {
		t.Fatal("sync all applied without a history depth")
	}
	if m, _, _ := Load(dir); m.Mode != Ask {
		t.Fatal("a rejected choice replaced the saved mode")
	}
	n, err := Apply(ctx, store, dir, account, Mode{Mode: All, History: HistoryNow})
	got := history()
	if err != nil || n != 2 || got["two@g.us"] != "on:" || got["15551234567@s.whatsapp.net"] != "on:" || got["one@g.us"] != "on:queued" {
		t.Fatalf("sync all, from now on: %d %v %v", n, err, got)
	}
	if m, chosen, _ := Load(dir); !chosen || !m.AutoApproves() || m.History != HistoryNow {
		t.Fatalf("saved %+v", m)
	}
	// Switching back keeps capture on; it only stops automatic approval.
	if _, err = Apply(ctx, store, dir, account, Mode{Mode: Ask}); err != nil || history()["two@g.us"] != "on:" {
		t.Fatalf("back to ask: %v %v", err, history())
	}
	if m, _, _ := Load(dir); m.AutoApproves() {
		t.Fatal("still auto approving")
	}
	if _, err = Apply(ctx, store, dir, account, Mode{Mode: All, History: HistoryFull}); err != nil || history()["two@g.us"] != "on:queued" {
		t.Fatalf("full history: %v %v", err, history())
	}
	audit, _ := os.ReadFile(filepath.Join(dir, "agent-audit.log"))
	if strings.Count(string(audit), `"event":"access_mode"`) != 4 || !strings.Contains(string(audit), `"scope":"all/full"`) {
		t.Fatalf("audit %s", audit)
	}
}
