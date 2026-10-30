//go:build darwin && cgo

package humancheck

/*
#cgo LDFLAGS: -framework Foundation -framework LocalAuthentication
#include <stdlib.h>
#include "la_darwin.h"
*/
import "C"

import (
	"context"
	"time"
	"unsafe"
)

type system struct{}

// System is the operating system's own confirmation prompt.
func System() Checker { return system{} }

func (system) Name() string    { return "Touch ID or your account password" }
func (system) Available() bool { return C.wasync_la_available() == 1 }

func (system) Verify(ctx context.Context, reason string) error {
	if err := ctx.Err(); err != nil {
		return ErrDeclined
	}
	wait := promptTimeout
	if deadline, ok := ctx.Deadline(); ok {
		wait = time.Until(deadline)
	}
	if wait <= 0 {
		return ErrDeclined
	}
	text := C.CString(reason)
	defer C.free(unsafe.Pointer(text))
	switch C.wasync_la_verify(text, C.int(wait.Milliseconds())) {
	case 1:
		return nil
	case -1:
		return ErrUnavailable
	}
	return ErrDeclined
}
