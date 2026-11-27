//go:build !darwin && !windows

package service

// System has no login item on this platform.
func System() Manager { return unsupported{} }

// App has no login item on this platform; the wasync app is macOS and Windows only.
func App() Manager { return unsupported{} }
