package approvecred

import (
	"errors"
	"fmt"
)

// macOS Keychain results (OSStatus) that this package acts on. They live here,
// not in the macOS file, so the decisions made on them are tested everywhere.
const (
	statusOK                    = 0
	statusUserCanceled          = -128
	statusInvalidOwnerEdit      = -25244
	statusDuplicate             = -25299
	statusNotFound              = -25300
	statusAuthFailed            = -25293
	statusInteractionNotAllowed = -25308
)

// statusTooLong is not an OSStatus; the macOS file uses it when a stored value
// is longer than the buffer it reads into.
const statusTooLong = 1

// rawOps are the single Keychain calls the macOS file makes, each returning the
// system's own result code.
type rawOps interface {
	add(account, secret, folder string) int
	update(account, secret, folder string) int
	remove(account string) int
}

// describe names a result code the way a person can look it up.
func describe(status int) string {
	switch status {
	case statusUserCanceled:
		return "OSStatus -128, the request was cancelled"
	case statusInvalidOwnerEdit:
		return "OSStatus -25244, the item was made by a different program"
	case statusAuthFailed:
		return "OSStatus -25293, not allowed without asking"
	case statusInteractionNotAllowed:
		return "OSStatus -25308, the Keychain is locked"
	case statusDuplicate:
		return "OSStatus -25299, the item already exists"
	case statusNotFound:
		return "OSStatus -25300, no such item"
	}
	return fmt.Sprintf("OSStatus %d", status)
}

// denied reports a result that means the person, or the system on their behalf,
// said no. It is never retried: asking again would only repeat the prompt.
func denied(status int) bool {
	return status == statusUserCanceled || status == statusAuthFailed
}

// wasync's Keychain calls never show a window, so a result that would have
// needed one comes back as an error instead: -25293 when the item belongs to a
// program the Keychain would have asked the person about, and -25308 when the
// Keychain is locked and macOS would have asked for its password.

// readError turns the result of a read into the error the rest of wasync knows.
func readError(status int) error {
	switch {
	case status == statusNotFound:
		return ErrNotFound
	case status == statusAuthFailed:
		return fmt.Errorf("%w (%s)", ErrForeignItem, describe(status))
	case status == statusInteractionNotAllowed:
		return ErrLocked
	case denied(status):
		return ErrAccessDenied
	case status == statusTooLong:
		return errors.New("keychain read failed (stored value is too long)")
	}
	return fmt.Errorf("keychain read failed (%s)", describe(status))
}

// setItem writes one item. An existing item is updated in place. When macOS
// refuses that edit because a different build of wasync made the item (the code
// signature changes with every upgrade or rebuild of an unsigned copy), the
// item is deleted and written once more, so the person is never left with a
// credential that cannot be saved. A "no" from the person ends it at once.
func setItem(o rawOps, account, secret, folder string) error {
	status := o.add(account, secret, folder)
	if status == statusDuplicate {
		status = o.update(account, secret, folder)
	}
	if status == statusInvalidOwnerEdit {
		if removed := o.remove(account); removed != statusOK && removed != statusNotFound {
			if removed == statusInteractionNotAllowed {
				return ErrLocked
			}
			if denied(removed) {
				return ErrAccessDenied
			}
			return fmt.Errorf("%w; delete the %q item in Keychain Access and run wasync again (%s)", ErrForeignItem, account, describe(removed))
		}
		status = o.add(account, secret, folder)
	}
	switch {
	case status == statusOK:
		return nil
	case status == statusInteractionNotAllowed:
		return ErrLocked
	case denied(status):
		return ErrAccessDenied
	case status == statusInvalidOwnerEdit:
		return fmt.Errorf("%w; delete the %q item in Keychain Access and run wasync again (%s)", ErrForeignItem, account, describe(status))
	}
	return fmt.Errorf("keychain write failed (%s)", describe(status))
}

// deleteError turns the result of a delete into an error; nothing to delete is fine.
func deleteError(status int) error {
	switch {
	case status == statusOK || status == statusNotFound:
		return nil
	case status == statusInteractionNotAllowed:
		return ErrLocked
	case status == statusAuthFailed:
		return fmt.Errorf("%w (%s)", ErrForeignItem, describe(status))
	case denied(status):
		return ErrAccessDenied
	}
	return fmt.Errorf("keychain delete failed (%s)", describe(status))
}
