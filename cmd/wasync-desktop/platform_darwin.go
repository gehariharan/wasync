package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// wasync_show_in_dock gives the app a Dock icon while its window is open, and
// keeps it to the menu bar otherwise.
static void wasync_show_in_dock(int show) {
	if (show) {
		[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
		[NSApp activateIgnoringOtherApps:YES];
	} else {
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	}
}
*/
import "C"

import (
	"os/exec"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func showInDock(show bool) {
	v := C.int(0)
	if show {
		v = 1
	}
	application.InvokeSync(func() { C.wasync_show_in_dock(v) })
}

func wasyncName() string { return "wasync" }

func wasyncCommand(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
