package setup

import (
	"strings"
	"testing"
)

func TestSnapshotFollowsTheLinkingSteps(t *testing.T) {
	var nilState *State
	if got := nilState.Snapshot(); got.Paired || got.QR != "" {
		t.Fatalf("nil state: %+v", got)
	}
	s := &State{}
	s.SetQR("code-1")
	if got := s.Snapshot(); got.QR != "code-1" || got.Paired {
		t.Fatalf("after QR: %+v", got)
	}
	s.SetFailure("phone said no")
	if got := s.Snapshot(); got.QR != "" || got.Failure != "phone said no" {
		t.Fatalf("after failure: %+v", got)
	}
	s.SetQR("code-2")
	if got := s.Snapshot(); got.Failure != "" || got.QR != "code-2" {
		t.Fatalf("a new code should clear the failure: %+v", got)
	}
	s.SetPaired()
	if got := s.Snapshot(); !got.Paired || got.QR != "" {
		t.Fatalf("after pairing the code must be gone: %+v", got)
	}
}

func TestSVGIsAScannableImageNotText(t *testing.T) {
	svg, err := SVG("2@abcdefghijklmnopqrstuvwxyz,0123456789+/=,more")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(svg, "<svg ") || !strings.HasSuffix(svg, "</svg>") || strings.Count(svg, "<rect") < 100 {
		t.Fatalf("not a drawn QR code: %.120s", svg)
	}
	if strings.Contains(svg, "abcdefghijkl") {
		t.Fatal("the pairing code leaked into the image as text")
	}
	other, _ := SVG("a different code")
	if other == svg {
		t.Fatal("different codes drew the same image")
	}
}
