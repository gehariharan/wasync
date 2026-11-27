//go:build darwin && cgo

package uninstall

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
#include "trash_darwin.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// trash moves path to the Bin with the system's own file manager, the way
// Finder's Move to Bin does. Going through Finder instead would make macOS ask
// the person to let wasync control Finder.
func trash(path string) error {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	msg := make([]byte, 512)
	if C.wasync_trash(p, (*C.char)(unsafe.Pointer(&msg[0])), C.int(len(msg))) == 0 {
		return nil
	}
	n := 0
	for n < len(msg) && msg[n] != 0 {
		n++
	}
	if n == 0 {
		return errors.New("macOS did not move it to the Bin")
	}
	return errors.New(string(msg[:n]))
}
