package main

import "testing"

func TestLegacyName(t *testing.T) {
	for argv0, want := range map[string]bool{
		"whatsapp-axi":                       true,
		"/opt/homebrew/bin/whatsapp-axi":     true,
		`C:\wasync\WhatsApp-AXI.exe`:         true,
		"wasync-axi":                         false,
		"/usr/local/bin/wasync-axi":          false,
		`C:\wasync\wasync-axi.exe`:           false,
		"/tmp/go-build123/exe/whatsapp-axi2": false,
	} {
		if legacyName(argv0) != want {
			t.Errorf("%q: want %v", argv0, want)
		}
	}
}
