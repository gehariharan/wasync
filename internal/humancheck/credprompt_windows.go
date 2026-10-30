//go:build windows

package humancheck

import (
	"context"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The account-password prompt is the fallback for a Windows computer where
// Windows Hello is not set up. It shows the standard Windows security dialog
// for the current account, takes the password, and checks it with LogonUser;
// it counts only if the account that logged on is the account wasync runs as.
// It refuses on any error. This path has not been run on a real Windows
// computer; see docs/architecture/threat-model.md.

var (
	credui   = windows.NewLazySystemDLL("credui.dll")
	advapi32 = windows.NewLazySystemDLL("advapi32.dll")

	procPrompt = credui.NewProc("CredUIPromptForWindowsCredentialW")
	procUnpack = credui.NewProc("CredUnPackAuthenticationBufferW")
	procLogon  = advapi32.NewProc("LogonUserW")
)

const (
	credUIWinEnumerateCurrentUser = 0x200
	errorCancelled                = 1223
	logon32LogonNetwork           = 3
	logon32ProviderDefault        = 0
)

type credUIInfo struct {
	size    uint32
	parent  windows.Handle
	message *uint16
	caption *uint16
	banner  windows.Handle
}

// interactiveSession reports whether wasync runs in a user's own desktop
// session rather than as a service, where no dialog can be shown.
func interactiveSession() bool {
	var id uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id); err != nil {
		return false
	}
	return id != 0
}

// passwordPrompt shows the dialog and returns nil only for the current
// account's correct password.
func passwordPrompt(ctx context.Context, reason string) error {
	if ctx.Err() != nil || !interactiveSession() {
		return ErrDeclined
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		done <- askAndCheck(reason)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// The dialog may stay on screen until it is closed, but its answer is
		// ignored: a late password cannot approve anything.
		return ErrDeclined
	}
}

func askAndCheck(reason string) error {
	message, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return ErrDeclined
	}
	caption, _ := windows.UTF16PtrFromString("wasync: confirm on this computer")
	info := credUIInfo{message: message, caption: caption}
	info.size = uint32(unsafe.Sizeof(info))
	var (
		authPackage uint32
		out         unsafe.Pointer
		outSize     uint32
	)
	r, _, _ := procPrompt.Call(uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&authPackage)), 0, 0,
		uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&outSize)), 0, credUIWinEnumerateCurrentUser)
	if r != 0 || out == nil {
		return ErrDeclined // cancelled (1223) or the dialog could not be shown
	}
	defer windows.CoTaskMemFree(out)

	const n = 512
	user, domain, password := make([]uint16, n), make([]uint16, n), make([]uint16, n)
	defer func() {
		for i := range password {
			password[i] = 0
		}
	}()
	userLen, domainLen, passLen := uint32(n), uint32(n), uint32(n)
	r, _, _ = procUnpack.Call(0, uintptr(out), uintptr(outSize), uintptr(unsafe.Pointer(&user[0])), uintptr(unsafe.Pointer(&userLen)),
		uintptr(unsafe.Pointer(&domain[0])), uintptr(unsafe.Pointer(&domainLen)), uintptr(unsafe.Pointer(&password[0])), uintptr(unsafe.Pointer(&passLen)))
	if r == 0 {
		return ErrDeclined
	}
	name, dom := windows.UTF16ToString(user), windows.UTF16ToString(domain)
	if dom == "" && strings.Contains(name, `\`) {
		parts := strings.SplitN(name, `\`, 2)
		dom, name = parts[0], parts[1]
	}
	var domainPtr *uint16
	if dom != "" {
		domainPtr, _ = windows.UTF16PtrFromString(dom)
	}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return ErrDeclined
	}
	var token windows.Token
	r, _, _ = procLogon.Call(uintptr(unsafe.Pointer(namePtr)), uintptr(unsafe.Pointer(domainPtr)), uintptr(unsafe.Pointer(&password[0])),
		logon32LogonNetwork, logon32ProviderDefault, uintptr(unsafe.Pointer(&token)))
	if r == 0 {
		return ErrDeclined
	}
	defer token.Close()
	got, err := token.GetTokenUser()
	if err != nil {
		return ErrDeclined
	}
	mine, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !windows.EqualSid(got.User.Sid, mine.User.Sid) {
		return ErrDeclined
	}
	return nil
}
