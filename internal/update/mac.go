package update

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Mac updates the wasync app in /Applications (or another folder this user
// may write to) from the release's drag-to-Applications disk image. It relies
// on the tools every Mac has: hdiutil, ditto, codesign, spctl and plutil.
type Mac struct {
	Run Runner
}

var appleTeam = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// requirement is what an update must satisfy: notarized by Apple and signed
// with a Developer ID certificate of the running app's team.
func requirement(team string) string {
	return `notarized and anchor apple generic and certificate leaf[subject.OU] = "` + team + `"`
}

func (m Mac) Probe(ctx context.Context, ch *Channel) {
	if !ch.OneClick {
		return
	}
	notSigned := "This copy of wasync is not signed by its developer, so it cannot check that an update is genuine. Download the new disk image from " + releasePage + " and drag wasync to Applications, eject the disk, then open wasync from Applications."
	stdout, stderr, err := m.Run(ctx, "codesign", []string{"-dv", "--verbose=2", ch.Target}, nil)
	if err != nil {
		ch.OneClick, ch.Note = false, notSigned
		return
	}
	team := ""
	for _, line := range strings.Split(string(stderr)+"\n"+string(stdout), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "TeamIdentifier="); ok {
			team = strings.TrimSpace(v)
		}
	}
	if !appleTeam.MatchString(team) {
		ch.OneClick, ch.Note = false, notSigned
		return
	}
	id, err := m.bundleID(ctx, ch.Target)
	if err != nil {
		ch.OneClick, ch.Note = false, notSigned
		return
	}
	ch.Team, ch.BundleID = team, id
}

func (m Mac) bundleID(ctx context.Context, app string) (string, error) {
	out, _, err := m.Run(ctx, "plutil", []string{"-extract", "CFBundleIdentifier", "raw", "-o", "-", filepath.Join(app, "Contents", "Info.plist")}, nil)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("the app has no bundle identifier")
	}
	return id, nil
}

// verify checks an app is notarized, signed by team and is the same app.
func (m Mac) verify(ctx context.Context, app, req, bundleID string) error {
	if _, _, err := m.Run(ctx, "codesign", []string{"--verify", "--deep", "--strict", "-R=" + req, app}, nil); err != nil {
		return fmt.Errorf("the update is not notarized and signed by wasync's developer, so it was not installed (%v)", err)
	}
	if _, _, err := m.Run(ctx, "spctl", []string{"--assess", "--type", "execute", app}, nil); err != nil {
		return fmt.Errorf("macOS Gatekeeper does not accept the update, so it was not installed (%v)", err)
	}
	if bundleID != "" {
		id, err := m.bundleID(ctx, app)
		if err != nil {
			return fmt.Errorf("could not read the update's bundle identifier: %w", err)
		}
		if id != bundleID {
			return fmt.Errorf("the update is a different app (%s, not %s), so it was not installed", id, bundleID)
		}
	}
	return nil
}

// Prepare copies the app out of the disk image and checks its signature.
func (m Mac) Prepare(ctx context.Context, ch Channel, download, staging string) (Staged, error) {
	if !appleTeam.MatchString(ch.Team) {
		return Staged{}, errors.New("the running app's signing team is not known, so the update cannot be checked")
	}
	mount := filepath.Join(staging, "mount")
	if err := os.Mkdir(mount, 0o700); err != nil {
		return Staged{}, err
	}
	if _, _, err := m.Run(ctx, "hdiutil", []string{"attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mount, download}, nil); err != nil {
		return Staged{}, fmt.Errorf("could not open the disk image: %w", err)
	}
	app, copyErr := m.copyApp(ctx, mount, filepath.Join(staging, "app"), filepath.Base(ch.Target))
	if err := m.detach(mount); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		return Staged{}, copyErr
	}
	req := requirement(ch.Team)
	if err := m.verify(ctx, app, req, ch.BundleID); err != nil {
		return Staged{}, err
	}
	return Staged{Path: app, Requirement: req}, nil
}

// copyApp copies the one app on the mounted image into dir, named name.
func (m Mac) copyApp(ctx context.Context, mount, dir, name string) (string, error) {
	entries, err := os.ReadDir(mount)
	if err != nil {
		return "", fmt.Errorf("could not read the disk image: %w", err)
	}
	var apps []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			apps = append(apps, e.Name())
		}
	}
	if len(apps) != 1 {
		return "", fmt.Errorf("the disk image should hold one app, but holds %d", len(apps))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	app := filepath.Join(dir, name)
	if _, _, err := m.Run(ctx, "ditto", []string{filepath.Join(mount, apps[0]), app}, nil); err != nil {
		return "", fmt.Errorf("could not copy the app out of the disk image: %w", err)
	}
	return app, nil
}

// detach unmounts the disk image, forcing it if a plain detach fails.
func (m Mac) detach(mount string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, _, err := m.Run(ctx, "hdiutil", []string{"detach", mount}, nil); err != nil {
		if _, _, err := m.Run(ctx, "hdiutil", []string{"detach", "-force", mount}, nil); err != nil {
			return fmt.Errorf("could not close the disk image: %w", err)
		}
	}
	return nil
}

// Install swaps the staged app in for the running one. The new app is copied
// beside the old one first, so the swap is two renames in one folder; if the
// second fails the old app is put back.
func (m Mac) Install(ctx context.Context, p Plan) error {
	if err := m.verify(ctx, p.Staged, p.Requirement, ""); err != nil {
		return err
	}
	parent, base := filepath.Dir(p.Target), filepath.Base(p.Target)
	suffix := randomSuffix()
	fresh := filepath.Join(parent, "."+base+".update-"+suffix)
	old := filepath.Join(parent, "."+base+".old-"+suffix)
	if err := os.Rename(p.Staged, fresh); err != nil {
		// The staging folder is on another volume: copy instead.
		if _, _, err := m.Run(ctx, "ditto", []string{p.Staged, fresh}, nil); err != nil {
			_ = os.RemoveAll(fresh)
			return fmt.Errorf("could not copy the update into %s: %w", parent, err)
		}
	}
	hadOld := true
	if err := os.Rename(p.Target, old); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = os.RemoveAll(fresh)
			return fmt.Errorf("could not move the current app aside: %w", err)
		}
		hadOld = false
	}
	if err := os.Rename(fresh, p.Target); err != nil {
		if hadOld {
			_ = os.Rename(old, p.Target)
		}
		_ = os.RemoveAll(fresh)
		return fmt.Errorf("could not put the update in place: %w", err)
	}
	if hadOld {
		_ = os.RemoveAll(old)
	}
	return nil
}

func randomSuffix() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
