//go:build !(darwin && cgo)

package uninstall

import "errors"

// trash is only needed for the macOS app, which is always built with cgo.
func trash(string) error { return errors.New("this copy of wasync cannot use the Bin") }
