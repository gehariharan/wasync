// Package setup holds what the setup page shows while wasync links WhatsApp:
// whether the phone has scanned yet, and the QR code to scan.
package setup

import (
	"fmt"
	"strings"
	"sync"

	"rsc.io/qr"
)

// State is written by the process that owns the WhatsApp connection and read
// by the setup page. It is safe for concurrent use.
type State struct {
	mu      sync.Mutex
	qr      string
	paired  bool
	failure string
}

// SetQR records the newest pairing code WhatsApp offered. It changes about
// every twenty seconds until the phone scans one.
func (s *State) SetQR(code string) {
	s.mu.Lock()
	s.qr, s.failure = code, ""
	s.mu.Unlock()
}

// SetPaired records that the phone linked this computer.
func (s *State) SetPaired() {
	s.mu.Lock()
	s.qr, s.paired, s.failure = "", true, ""
	s.mu.Unlock()
}

// SetFailure records why linking stopped, in words safe to show.
func (s *State) SetFailure(message string) {
	s.mu.Lock()
	s.qr, s.failure = "", message
	s.mu.Unlock()
}

// Snapshot is a copy of the state at one moment.
type Snapshot struct {
	Paired  bool
	QR      string
	Failure string
}

// Snapshot returns the current state. A nil State reads as "not linked".
func (s *State) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{Paired: s.paired, QR: s.qr, Failure: s.failure}
}

// SVG draws code as a QR image that scales to any size: dark squares on a
// white square with the quiet border scanners need.
func SVG(code string) (string, error) {
	c, err := qr.Encode(code, qr.L)
	if err != nil {
		return "", fmt.Errorf("draw QR code: %w", err)
	}
	const border = 4
	size := c.Size + 2*border
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code to scan with WhatsApp on your phone"><rect width="%d" height="%d" fill="#fff"/>`, size, size, size, size)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				fmt.Fprintf(&b, `<rect x="%d" y="%d" width="1" height="1" fill="#000"/>`, x+border, y+border)
			}
		}
	}
	b.WriteString(`</svg>`)
	return b.String(), nil
}
