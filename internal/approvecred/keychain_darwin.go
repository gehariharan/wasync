//go:build darwin && cgo

package approvecred

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework Security
#include <stdlib.h>
#include "keychain_darwin.h"
*/
import "C"

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"unsafe"
)

const service = "wasync"

// keychain is the person's login Keychain or, when path is set, a keychain
// file (tests, and a person checking a build with WASYNC_TEST_KEYCHAIN=path).
// No call ever shows a window except unlock: one that macOS would have to ask
// the person about fails instead (see status.go).
type keychain struct{ path string }

// oneCall runs one Keychain call at a time: each one turns the Keychain's
// windows off for the whole process while it runs.
var oneCall sync.Mutex

// platform is the Keychain for a build signed by a developer team, named after
// that signature, and nothing (the private file) for any other build. The
// Keychain trusts a program by its signature, so an unsigned or ad-hoc build,
// whose signature changes every time it is built, would find each earlier
// build's item belongs to someone else and could only read it by asking the
// person; it keeps the file instead, as wasync does where there is no Keychain.
// WASYNC_TEST_KEYCHAIN=1 gives any build the login Keychain, and an absolute
// path gives it that keychain file.
func platform() (Backend, string) {
	if v := os.Getenv(RealKeychainEnv); filepath.IsAbs(v) {
		return keychain{path: v}, signer()
	}
	id := signer()
	if id == "" && os.Getenv(RealKeychainEnv) != "1" {
		return nil, ""
	}
	return keychain{}, id
}

// signer is "TEAM.identifier" for a program signed by a developer team, or "".
func signer() string {
	buf := make([]byte, 512)
	if C.wasync_kc_signer((*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))) != statusOK {
		return ""
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
}

// guard refuses a test that did not ask for the person's own Keychain. A
// keychain file is never theirs.
func (k keychain) guard() error {
	if k.path != "" {
		return nil
	}
	return refuseInTest()
}

// cpath is the keychain file for C, or NULL for the person's own; free it.
func (k keychain) cpath() *C.char {
	if k.path == "" {
		return nil
	}
	return C.CString(k.path)
}

func free(p *C.char) {
	if p != nil {
		C.free(unsafe.Pointer(p))
	}
}

func (k keychain) Get(account string) (string, error) {
	if err := k.guard(); err != nil {
		return "", err
	}
	path, svc, acct := k.cpath(), C.CString(service), C.CString(account)
	defer free(path)
	defer free(svc)
	defer free(acct)
	buf := make([]byte, 256)
	oneCall.Lock()
	rc := int(C.wasync_kc_get(path, svc, acct, (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))))
	oneCall.Unlock()
	if rc != statusOK {
		return "", readError(rc)
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n]), nil
}

func (k keychain) Set(account, secret, folder string) error {
	if err := k.guard(); err != nil {
		return err
	}
	return setItem(k, account, secret, folder)
}

func (k keychain) Delete(account string) error {
	if err := k.guard(); err != nil {
		return err
	}
	return deleteError(k.remove(account))
}

func (k keychain) List() ([]Item, error) {
	if err := k.guard(); err != nil {
		return nil, err
	}
	path, svc := k.cpath(), C.CString(service)
	defer free(path)
	defer free(svc)
	var out *C.char
	var n C.int
	oneCall.Lock()
	rc := int(C.wasync_kc_list(path, svc, &out, &n))
	oneCall.Unlock()
	if rc != statusOK {
		return nil, readError(rc)
	}
	if out == nil {
		return nil, nil
	}
	defer C.free(unsafe.Pointer(out))
	fields := bytes.Split(C.GoBytes(unsafe.Pointer(out), n), []byte{0})
	var items []Item
	for i := 0; i+1 < len(fields); i += 2 {
		items = append(items, Item{Account: string(fields[i]), Folder: string(fields[i+1])})
	}
	return items, nil
}

// unlock asks the person to unlock the keychain when it is locked. It is the
// only call that may show a window.
func (k keychain) unlock() error {
	if err := k.guard(); err != nil {
		return err
	}
	path := k.cpath()
	defer free(path)
	oneCall.Lock()
	rc := int(C.wasync_kc_unlock(path))
	oneCall.Unlock()
	if rc != statusOK {
		return readError(rc)
	}
	return nil
}

// The single calls setItem makes. Each one passes strings to C and frees them.
func (k keychain) add(account, secret, folder string) int {
	return k.write(account, secret, folder, func(path, svc, acct, sec, dir *C.char) C.int { return C.wasync_kc_add(path, svc, acct, sec, dir) })
}

func (k keychain) update(account, secret, folder string) int {
	return k.write(account, secret, folder, func(path, svc, acct, sec, dir *C.char) C.int {
		return C.wasync_kc_update(path, svc, acct, sec, dir)
	})
}

func (k keychain) remove(account string) int {
	path, svc, acct := k.cpath(), C.CString(service), C.CString(account)
	defer free(path)
	defer free(svc)
	defer free(acct)
	oneCall.Lock()
	defer oneCall.Unlock()
	return int(C.wasync_kc_delete(path, svc, acct))
}

func (k keychain) write(account, secret, folder string, call func(path, svc, acct, sec, dir *C.char) C.int) int {
	path, svc, acct, sec, dir := k.cpath(), C.CString(service), C.CString(account), C.CString(secret), C.CString(folder)
	defer free(path)
	defer free(svc)
	defer free(acct)
	defer free(sec)
	defer free(dir)
	oneCall.Lock()
	defer oneCall.Unlock()
	return int(call(path, svc, acct, sec, dir))
}

// newKeychainFile makes an unlocked keychain file that is not on the person's
// keychain list, for tests.
func newKeychainFile(path, password string) int {
	p, pw := C.CString(path), C.CString(password)
	defer free(p)
	defer free(pw)
	return int(C.wasync_kc_create(p, pw))
}
