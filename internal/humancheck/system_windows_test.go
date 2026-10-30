//go:build windows

package humancheck

import (
	"context"
	"errors"
	"testing"
)

// No dialog is shown: a cancelled request must be refused without asking anyone.
func TestWindowsRefusesWithoutAskingWhenAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := System().Verify(ctx, "Approve: test"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("got %v, want ErrDeclined", err)
	}
	if err := passwordPrompt(ctx, "Approve: test"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("password prompt got %v, want ErrDeclined", err)
	}
}
