package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/approvecred"
)

// listKeychain is a Keychain that can list what it holds, as macOS can.
type listKeychain struct {
	items     []approvecred.Item
	removed   []string
	deleteErr map[string]error
}

func (k *listKeychain) Get(string) (string, error)       { return "", approvecred.ErrNotFound }
func (k *listKeychain) Set(string, string, string) error { return nil }
func (k *listKeychain) Delete(account string) error {
	if err := k.deleteErr[account]; err != nil {
		return err
	}
	k.removed = append(k.removed, account)
	return nil
}
func (k *listKeychain) List() ([]approvecred.Item, error) { return k.items, nil }

func runKeychain(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app := &App{Out: &out, Err: &bytes.Buffer{}}
	err := app.Run(context.Background(), append([]string{"--data-dir", dir, "keychain"}, args...))
	return out.String(), err
}

func keychainFixture(t *testing.T) (dir string, k *listKeychain) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "data")
	alive := filepath.Join(t.TempDir(), "other-real-dir")
	if err := os.Mkdir(alive, 0o700); err != nil {
		t.Fatal(err)
	}
	current, err := approvecred.Account(dir)
	if err != nil {
		t.Fatal(err)
	}
	k = &listKeychain{items: []approvecred.Item{
		{Account: current}, // this data directory, from before folders were recorded
		{Account: "approve:1111111111111111", Folder: alive},
		{Account: "approve:2222222222222222", Folder: filepath.Join(t.TempDir(), "gone")},
		{Account: "approve:3333333333333333"},  // older, folder unknown
		{Account: "someone-else", Folder: "/"}, // not ours to touch
	}}
	approvecred.Use(k)
	t.Cleanup(func() { approvecred.Use(nil) })
	return dir, k
}

func TestKeychainListsWhatPruneWouldRemoveAndRemovesNothing(t *testing.T) {
	dir, k := keychainFixture(t)
	for _, args := range [][]string{nil, {"prune"}} {
		out, err := runKeychain(t, dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"keep    approve:1111111111111111  its folder exists",
			"remove  approve:2222222222222222  its folder is gone",
			"remove  approve:3333333333333333  made before wasync recorded its folder",
			"this data directory",
			"2 to remove",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: missing %q in:\n%s", args, want, out)
			}
		}
		if strings.Contains(out, "someone-else") {
			t.Errorf("listed an item that is not an approval credential:\n%s", out)
		}
	}
	if len(k.removed) != 0 {
		t.Fatalf("removed without --apply: %v", k.removed)
	}
}

func TestKeychainPruneApplyRemovesLeftoversAndKeepsTheRest(t *testing.T) {
	dir, k := keychainFixture(t)
	out, err := runKeychain(t, dir, "prune", "--apply")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(k.removed, ","); got != "approve:2222222222222222,approve:3333333333333333" {
		t.Fatalf("removed %q", got)
	}
	if !strings.Contains(out, "Removed 2.") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestKeychainPruneReportsWhatItCouldNotRemove(t *testing.T) {
	dir, k := keychainFixture(t)
	k.deleteErr = map[string]error{"approve:2222222222222222": errors.New("not allowed")}
	out, err := runKeychain(t, dir, "prune", "--apply")
	if err == nil || !strings.Contains(err.Error(), "approve:2222222222222222") {
		t.Fatalf("err %v", err)
	}
	if got := strings.Join(k.removed, ","); got != "approve:3333333333333333" || !strings.Contains(out, "Removed 1.") {
		t.Fatalf("removed %q\n%s", got, out)
	}
}

func TestKeychainWithNothingLeftOver(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	current, _ := approvecred.Account(dir)
	approvecred.Use(&listKeychain{items: []approvecred.Item{{Account: current}}})
	t.Cleanup(func() { approvecred.Use(nil) })
	out, err := runKeychain(t, dir, "prune", "--apply")
	if err != nil || !strings.Contains(out, "Nothing to remove.") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestKeychainWhereThereIsNoKeychain(t *testing.T) {
	approvecred.Use(nil)
	out, err := runKeychain(t, filepath.Join(t.TempDir(), "data"))
	if err != nil || !strings.Contains(out, "private file") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestKeychainRejectsUnknownArguments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	for _, args := range [][]string{{"wipe"}, {"prune", "extra"}, {"prune", "--yes"}} {
		if _, err := runKeychain(t, dir, args...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}
