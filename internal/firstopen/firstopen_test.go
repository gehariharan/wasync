package firstopen

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFirstOpenIsPendingUntilDone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if pending, err := Pending(dir); err != nil || !pending {
		t.Fatalf("a new data directory: pending %v, %v", pending, err)
	}
	if err := Done(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if pending, err := Pending(dir); err != nil || pending {
		t.Fatalf("after Done: pending %v, %v", pending, err)
	}
	if err := Done(dir, time.Now()); err != nil {
		t.Fatalf("recording it again: %v", err)
	}
	if pending, _ := Pending(t.TempDir()); !pending {
		t.Fatal("another data directory has its own First open")
	}
}
