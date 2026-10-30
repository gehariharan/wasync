//go:build windows

package notify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// A tray balloon needs no Start menu shortcut. This path has not been run on a
// real Windows computer. The text travels in the environment, not the script.
const balloon = `
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$n = New-Object System.Windows.Forms.NotifyIcon
$n.Icon = [System.Drawing.SystemIcons]::Information
$n.Visible = $true
$n.ShowBalloonTip(10000, $env:WASYNC_TITLE, $env:WASYNC_TEXT, [System.Windows.Forms.ToolTipIcon]::Info)
Start-Sleep -Seconds 11
$n.Dispose()
`

func send(title, text string) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", balloon)
	cmd.Env = append(os.Environ(), "WASYNC_TITLE="+title, "WASYNC_TEXT="+text)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if cmd.Start() != nil {
		cancel()
		return
	}
	go func() { defer cancel(); _ = cmd.Wait() }()
}
