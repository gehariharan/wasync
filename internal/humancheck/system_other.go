//go:build !windows && !(darwin && cgo)

package humancheck

import "context"

type system struct{}

// System has no operating-system prompt on this platform, so approvals fall
// back to the typed confirmation in a terminal.
func System() Checker { return system{} }

func (system) Name() string                         { return "" }
func (system) Available() bool                      { return false }
func (system) Verify(context.Context, string) error { return ErrUnavailable }
