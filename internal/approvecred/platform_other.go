//go:build !(darwin && cgo)

package approvecred

// platform has no Keychain here, so the credential stays a private file.
func platform() Backend { return nil }
