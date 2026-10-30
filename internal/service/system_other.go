//go:build !darwin && !windows

package service

// System has no login item on this platform.
func System() Manager { return unsupported{} }
