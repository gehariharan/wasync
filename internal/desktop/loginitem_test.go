package desktop

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/gehariharan/wasync/internal/service"
)

// fakeItem is a login item kept in memory.
type fakeItem struct {
	installed  bool
	spec       service.Spec
	unreadable bool
	installErr error
	installs   int
	removals   int
}

func (f *fakeItem) Where() string { return "a fake login item" }
func (f *fakeItem) Install(s service.Spec) error {
	if f.installErr != nil {
		return f.installErr
	}
	f.installed, f.spec = true, s
	f.installs++
	return nil
}
func (f *fakeItem) Uninstall() error         { f.installed = false; f.removals++; return nil }
func (f *fakeItem) Installed() (bool, error) { return f.installed, nil }
func (f *fakeItem) Read() (service.Spec, error) {
	if !f.installed {
		return service.Spec{}, service.ErrNotInstalled
	}
	if f.unreadable {
		return service.Spec{}, errors.New("not a plist")
	}
	return f.spec, nil
}

func TestTheLegacyLoginItemMovesToTheAppsOwnForTheSameDataDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	app := service.Spec{Exe: "/Applications/wasync.app/Contents/MacOS/wasync-desktop", DataDir: dir}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name           string
		legacy, appIt  *fakeItem
		moved          bool
		appInstalls    int
		legacyRemovals int
		stops          int
		wantErr        bool
	}{
		{
			name:   "no legacy item",
			legacy: &fakeItem{}, appIt: &fakeItem{},
		},
		{
			name:   "legacy item for another data directory",
			legacy: &fakeItem{installed: true, spec: service.Spec{Exe: "/usr/local/bin/wasync", DataDir: filepath.Join(t.TempDir(), "other")}}, appIt: &fakeItem{},
		},
		{
			name:   "legacy item that cannot be read",
			legacy: &fakeItem{installed: true, unreadable: true}, appIt: &fakeItem{},
			wantErr: true,
		},
		{
			name:   "same data directory",
			legacy: &fakeItem{installed: true, spec: service.Spec{Exe: "/usr/local/bin/wasync", DataDir: dir}}, appIt: &fakeItem{},
			moved: true, appInstalls: 1, legacyRemovals: 1, stops: 1,
		},
		{
			name:   "same data directory written untidily",
			legacy: &fakeItem{installed: true, spec: service.Spec{Exe: "/usr/local/bin/wasync", DataDir: dir + string(filepath.Separator)}}, appIt: &fakeItem{},
			moved: true, appInstalls: 1, legacyRemovals: 1, stops: 1,
		},
		{
			name:   "the app's item is already on",
			legacy: &fakeItem{installed: true, spec: service.Spec{Exe: "/usr/local/bin/wasync", DataDir: dir}}, appIt: &fakeItem{installed: true, spec: app},
			moved: true, legacyRemovals: 1, stops: 1,
		},
		{
			name:   "the app's item cannot be installed",
			legacy: &fakeItem{installed: true, spec: service.Spec{Exe: "/usr/local/bin/wasync", DataDir: dir}}, appIt: &fakeItem{installErr: errors.New("read-only")},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stops, wasOn := 0, tc.legacy.installed
			m := loginItemMove{legacy: tc.legacy, app: tc.appIt, spec: app, log: quiet, stop: func() error { stops++; return nil }}
			moved, err := m.run()
			if moved != tc.moved || (err != nil) != tc.wantErr {
				t.Fatalf("moved %v err %v", moved, err)
			}
			if tc.appIt.installs != tc.appInstalls || tc.legacy.removals != tc.legacyRemovals || stops != tc.stops {
				t.Fatalf("app installs %d, legacy removals %d, stops %d", tc.appIt.installs, tc.legacy.removals, stops)
			}
			if tc.appInstalls == 1 && tc.appIt.spec != app {
				t.Fatalf("the app's item opens %+v, want %+v", tc.appIt.spec, app)
			}
			if !tc.moved && wasOn && !tc.legacy.installed {
				t.Fatal("a legacy item that was not moved must stay")
			}
		})
	}
}

func TestOnlyAServeTheLoginItemMayHaveStartedIsStopped(t *testing.T) {
	for _, tc := range []struct {
		kind, by string
		want     bool
	}{
		{"serve", "login item", true},
		{"serve", "", true}, // Windows does not say what started it
		{"serve", "manually", false},
		{"serve", "wasync app", false},
		{"tui", "", false},
		{"setup", "login item", false},
	} {
		if got := startedByLegacyItem(tc.kind, tc.by); got != tc.want {
			t.Errorf("%s started by %q: %v, want %v", tc.kind, tc.by, got, tc.want)
		}
	}
}
