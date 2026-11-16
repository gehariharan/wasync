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
	"unsafe"
)

const service = "wasync"

type keychain struct{}

func platform() Backend { return keychain{} }

func (keychain) Get(account string) (string, error) {
	if err := refuseInTest(); err != nil {
		return "", err
	}
	svc, acct := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	buf := make([]byte, 256)
	rc := int(C.wasync_kc_get(svc, acct, (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))))
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
	if err := refuseInTest(); err != nil {
		return err
	}
	return setItem(k, account, secret, folder)
}

func (keychain) Delete(account string) error {
	if err := refuseInTest(); err != nil {
		return err
	}
	svc, acct := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	return deleteError(int(C.wasync_kc_delete(svc, acct)))
}

func (keychain) List() ([]Item, error) {
	if err := refuseInTest(); err != nil {
		return nil, err
	}
	svc := C.CString(service)
	defer C.free(unsafe.Pointer(svc))
	var out *C.char
	var n C.int
	if rc := int(C.wasync_kc_list(svc, &out, &n)); rc != statusOK {
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

// The single calls setItem makes. Each one passes strings to C and frees them.
func (keychain) add(account, secret, folder string) int {
	return write(account, secret, folder, func(svc, acct, sec, dir *C.char) C.int { return C.wasync_kc_add(svc, acct, sec, dir) })
}

func (keychain) update(account, secret, folder string) int {
	return write(account, secret, folder, func(svc, acct, sec, dir *C.char) C.int { return C.wasync_kc_update(svc, acct, sec, dir) })
}

func (keychain) remove(account string) int {
	svc, acct := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	return int(C.wasync_kc_delete(svc, acct))
}

func write(account, secret, folder string, call func(svc, acct, sec, dir *C.char) C.int) int {
	svc, acct, sec, dir := C.CString(service), C.CString(account), C.CString(secret), C.CString(folder)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	defer C.free(unsafe.Pointer(sec))
	defer C.free(unsafe.Pointer(dir))
	return int(call(svc, acct, sec, dir))
}
