package service

import "strings"

// RunName is the value name under the current user's Run key.
const RunName = "wasync"

// quote doubles single quotes so the text is safe inside a PowerShell '...' string.
func quote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// RunCommand is the line Windows runs at login. wasync is a console program, so
// it is started through a hidden PowerShell window rather than a visible one.
func RunCommand(s Spec) string {
	args := `--data-dir "` + s.DataDir + `" serve`
	return `powershell.exe -NoProfile -NonInteractive -WindowStyle Hidden -Command "Start-Process -WindowStyle Hidden -FilePath '` + quote(s.Exe) + `' -ArgumentList '` + quote(strings.ReplaceAll(args, `"`, `\"`)) + `'"`
}
