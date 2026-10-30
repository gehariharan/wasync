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
	"fmt"
	"unsafe"
)

const service = "wasync"

type keychain struct{}

func platform() Backend { return keychain{} }

func (keychain) Get(account string) (string, error) {
	svc, acct := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	buf := make([]byte, 256)
	switch rc := C.wasync_kc_get(svc, acct, (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))); rc {
	case 0:
		n := 0
		for n < len(buf) && buf[n] != 0 {
			n++
		}
		return string(buf[:n]), nil
	case 1:
		return "", ErrNotFound
	default:
		return "", fmt.Errorf("keychain read failed (%d)", int(rc))
	}
}

func (keychain) Set(account, secret string) error {
	svc, acct, sec := C.CString(service), C.CString(account), C.CString(secret)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	defer C.free(unsafe.Pointer(sec))
	if rc := C.wasync_kc_set(svc, acct, sec); rc != 0 {
		return fmt.Errorf("keychain write failed (%d)", int(rc))
	}
	return nil
}

func (keychain) Delete(account string) error {
	svc, acct := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	if rc := C.wasync_kc_delete(svc, acct); rc < 0 {
		return fmt.Errorf("keychain delete failed (%d)", int(rc))
	}
	return nil
}
