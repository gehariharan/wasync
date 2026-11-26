package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runner runs a command and returns what it printed. env adds variables to
// this process's environment. Tests replace it.
type Runner func(ctx context.Context, name string, args []string, env []string) (stdout, stderr []byte, err error)

// ExecRunner runs real commands.
func ExecRunner(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		if msg != "" {
			err = fmt.Errorf("%s: %w: %s", name, err, msg)
		} else {
			err = fmt.Errorf("%s: %w", name, err)
		}
	}
	return out.Bytes(), errOut.Bytes(), err
}

// Staged is a checked update, ready for the helper to install.
type Staged struct {
	// Path is the copied .app bundle (macOS) or the Setup program (Windows).
	Path string
	// SHA256 is the Setup program's hash, checked again just before it runs.
	SHA256 string
	// Requirement is the code-signing requirement the app must still meet
	// just before it replaces the running one.
	Requirement string
}

// Platform does the parts of an update that differ by operating system.
type Platform interface {
	// Probe fills in what a one-click channel needs to check an update (the
	// signer of the running copy). When that cannot be found it turns
	// one-click off and says why in ch.Note.
	Probe(ctx context.Context, ch *Channel)
	// Prepare checks a downloaded file whose checksum already matched and
	// stages what Install will put in place, inside the staging folder.
	Prepare(ctx context.Context, ch Channel, download, staging string) (Staged, error)
	// Install puts the staged update in place. It runs in the helper, after
	// the running wasync has stopped.
	Install(ctx context.Context, p Plan) error
}

// fileSHA256 is the hex SHA-256 of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unsupportedPlatform never offers one-click updates.
type unsupportedPlatform struct{}

func (unsupportedPlatform) Probe(_ context.Context, ch *Channel) {
	if ch.OneClick {
		ch.OneClick, ch.Note = false, "wasync cannot update itself on this computer. Download the new wasync from "+releasePage+"."
	}
}

func (unsupportedPlatform) Prepare(context.Context, Channel, string, string) (Staged, error) {
	return Staged{}, fmt.Errorf("wasync cannot update itself on this computer")
}

func (unsupportedPlatform) Install(context.Context, Plan) error {
	return fmt.Errorf("wasync cannot update itself on this computer")
}
