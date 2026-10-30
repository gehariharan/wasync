package api

import (
	"os"
	"testing"

	"github.com/gehariharan/wasync/internal/approvecred"
)

// Tests sign with the approval credential file they can read; the real
// Keychain belongs to the person running them and is never touched here.
func TestMain(m *testing.M) {
	approvecred.Use(nil)
	os.Exit(m.Run())
}
