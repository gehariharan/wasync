//go:build windows

package humancheck

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Windows Hello (fingerprint, face or PIN) is asked for first, through the Windows
// UserConsentVerifier API, reached from the PowerShell that ships with Windows.
// The prompt text goes in through the environment, never into the script, so a
// chat name cannot change what runs. Only the exact word VERIFIED counts.
//
// This path has not been run on a real Windows computer; see docs/architecture/threat-model.md.

const helloScript = `
$ErrorActionPreference = 'Stop'
try {
  Add-Type -AssemblyName System.Runtime.WindowsRuntime
  $null = [Windows.Security.Credentials.UI.UserConsentVerifier, Windows.Security.Credentials.UI, ContentType = WindowsRuntime]
  $asTask = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object {
    $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation` + "`" + `1' })[0]
  function Await($op, $type) {
    $task = $asTask.MakeGenericMethod($type).Invoke($null, @($op))
    $null = $task.Wait(-1)
    $task.Result
  }
  $availability = Await ([Windows.Security.Credentials.UI.UserConsentVerifier]::CheckAvailabilityAsync()) ([Windows.Security.Credentials.UI.UserConsentVerifierAvailability])
  if ($env:WASYNC_MODE -eq 'check') {
    if ($availability -eq 'Available') { 'AVAILABLE' } else { 'UNAVAILABLE' }
    exit 0
  }
  if ($availability -ne 'Available') { 'UNAVAILABLE'; exit 0 }
  $result = Await ([Windows.Security.Credentials.UI.UserConsentVerifier]::RequestVerificationAsync($env:WASYNC_REASON)) ([Windows.Security.Credentials.UI.UserConsentVerificationResult])
  if ($result -eq 'Verified') { 'VERIFIED' } else { 'DECLINED' }
} catch {
  'ERROR'
}
`

type system struct{}

// System is Windows Hello.
func System() Checker { return system{} }

func (system) Name() string { return "Windows Hello, or your account password if Hello is not set up" }

func powershell() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

func run(ctx context.Context, mode, reason string) string {
	cmd := exec.CommandContext(ctx, powershell(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", helloScript)
	cmd.Env = append(os.Environ(), "WASYNC_MODE="+mode, "WASYNC_REASON="+reason)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "ERROR"
	}
	lines := strings.Fields(out.String())
	if len(lines) == 0 {
		return "ERROR"
	}
	return lines[len(lines)-1]
}

var (
	helloMu      sync.Mutex
	helloChecked time.Time
	helloResult  bool
)

// helloAvailable asks Windows at most once every thirty seconds; starting PowerShell is slow.
func helloAvailable() bool {
	helloMu.Lock()
	defer helloMu.Unlock()
	if !helloChecked.IsZero() && time.Since(helloChecked) < 30*time.Second {
		return helloResult
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	helloResult, helloChecked = run(ctx, "check", "") == "AVAILABLE", time.Now()
	return helloResult
}

// Available is true in a user's own desktop session: Windows Hello when it is
// set up, otherwise the account-password dialog. A prompt that then cannot be
// answered refuses the approval; it never lets it through.
func (system) Available() bool { return interactiveSession() }

func (system) Verify(ctx context.Context, reason string) error {
	if ctx.Err() != nil {
		return ErrDeclined
	}
	if helloAvailable() {
		switch run(ctx, "verify", reason) {
		case "VERIFIED":
			return nil
		case "DECLINED":
			return ErrDeclined // the person said no; do not ask again another way
		}
		// Hello failed to run: fall through to the password, which is also a person.
	}
	return passwordPrompt(ctx, reason)
}
