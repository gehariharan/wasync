package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Windows updates a copy installed by Setup (packaging/windows/wasync.iss) by
// running the new Setup program silently. Setup reuses the folder and the
// choices (tasks) of the earlier install. When the running wasync.exe is
// signed, an update must be signed by the same publisher; older unsigned
// previews rely on the checksum alone.
type Windows struct {
	Run Runner
	// Getenv reads the environment; nil uses os.Getenv.
	Getenv func(string) string
}

// setupTimeout bounds a silent Setup run.
const setupTimeout = 10 * time.Minute

func (w Windows) getenv(k string) string {
	if w.Getenv != nil {
		return w.Getenv(k)
	}
	return os.Getenv(k)
}

// powershell is Windows PowerShell by its full path, so a program called
// powershell.exe elsewhere on PATH is never run instead.
func (w Windows) powershell() string {
	root := w.getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

// signature returns a file's Authenticode status ("Valid", "NotSigned", ...)
// and signer subject. The path goes through the environment, never through
// the command text.
func (w Windows) signature(ctx context.Context, path string) (status, subject string, err error) {
	script := `$s = Get-AuthenticodeSignature -LiteralPath $env:WASYNC_UPDATE_FILE; $n = ''; if ($s.SignerCertificate) { $n = $s.SignerCertificate.Subject }; [Console]::Out.Write($s.Status.ToString() + "` + "`n" + `" + $n)`
	out, _, err := w.Run(ctx, w.powershell(), []string{"-NoProfile", "-NonInteractive", "-Command", script}, []string{"WASYNC_UPDATE_FILE=" + path})
	if err != nil {
		return "", "", err
	}
	status, subject, _ = strings.Cut(strings.ReplaceAll(string(out), "\r", ""), "\n")
	status, subject = strings.TrimSpace(status), strings.TrimSpace(subject)
	if status == "" {
		return "", "", errors.New("Windows did not report the file's signature")
	}
	return status, subject, nil
}

func (w Windows) Probe(ctx context.Context, ch *Channel) {
	if !ch.OneClick {
		return
	}
	status, subject, err := w.signature(ctx, ch.Target)
	if err != nil {
		ch.OneClick, ch.Note = false, "wasync could not read its own signature, so it cannot check an update. Download the new Setup from "+releasePage+"."
		return
	}
	switch {
	case status == "NotSigned":
	case status == "Valid" && subject != "":
		ch.Signer = subject
	default:
		ch.OneClick, ch.Note = false, "Windows cannot check wasync's own signature ("+status+"), so wasync cannot check an update. Download the new Setup from "+releasePage+"."
	}
}

// checkSigner requires a Valid signature by signer, when there is one to match.
func (w Windows) checkSigner(ctx context.Context, path, signer string) error {
	if signer == "" {
		return nil // unsigned previews: the checksum is the check
	}
	status, subject, err := w.signature(ctx, path)
	if err != nil {
		return fmt.Errorf("could not read the update's signature: %w", err)
	}
	if status != "Valid" || subject != signer {
		return fmt.Errorf("the update is not signed by wasync's publisher (%s, %q), so it was not installed", status, subject)
	}
	return nil
}

func (w Windows) Prepare(ctx context.Context, ch Channel, download, _ string) (Staged, error) {
	if err := w.checkSigner(ctx, download, ch.Signer); err != nil {
		return Staged{}, err
	}
	sum, err := fileSHA256(download)
	if err != nil {
		return Staged{}, err
	}
	return Staged{Path: download, SHA256: sum, Requirement: ch.Signer}, nil
}

// Install runs Setup silently: no pages, no message boxes, no computer
// restart. Setup closes running copies of wasync that still hold its files.
func (w Windows) Install(ctx context.Context, p Plan) error {
	sum, err := fileSHA256(p.Staged)
	if err != nil {
		return fmt.Errorf("could not read the downloaded Setup: %w", err)
	}
	if sum != p.SHA256 {
		return errors.New("the downloaded Setup changed after it was checked, so it was not run")
	}
	if err := w.checkSigner(ctx, p.Staged, p.Requirement); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	args := []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/CLOSEAPPLICATIONS", "/NORESTARTAPPLICATIONS", "/LOG=" + filepath.Join(filepath.Dir(p.Staged), "setup.log")}
	if _, _, err := w.Run(ctx, p.Staged, args, nil); err != nil {
		return fmt.Errorf("Setup did not finish: %w", err)
	}
	return nil
}
