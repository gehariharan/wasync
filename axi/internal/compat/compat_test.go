package compat

import (
	"errors"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/installhint"
)

func v(major, minor int, features ...string) contract.VersionInfo {
	return contract.VersionInfo{APIVersion: major, APIMinor: minor, WasyncVersion: "0.1.0", Features: features}
}

func code(err error) string {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func TestEmbeddedFile(t *testing.T) {
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.MinAPI != "1.0" {
		t.Fatalf("min_api %s", m.MinAPI)
	}
	if got := strings.Join(m.CommandNames(), ","); got != "chats,doctor,edit,home,message,messages,observe,react,reply,search,thread,version" {
		t.Fatalf("commands %s", got)
	}
}

func TestCheckAPI(t *testing.T) {
	m := Default()
	if err := m.CheckAPI(v(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckAPI(v(1, 7)); err != nil {
		t.Fatal(err)
	}
	err := m.CheckAPI(v(0, 9))
	if code(err) != contract.CodeWasyncTooOld || !strings.Contains(err.(*contract.Error).Help, installhint.UpgradeCore()) {
		t.Fatalf("old api: %v", err)
	}
	if code(m.CheckAPI(v(2, 0))) != contract.CodeAXITooOld {
		t.Fatal("newer major accepted")
	}
	m.minMinor = 3
	if code(m.CheckAPI(v(1, 2))) != contract.CodeWasyncTooOld {
		t.Fatal("older minor accepted")
	}
}

func TestCheckFeatures(t *testing.T) {
	m, err := Parse([]byte(`{"format":1,"min_api":"1.0","upgrade_help":"upgrade fixture wasync",
		"commands":{"observe":{"features":["changes","checkpoints"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Check("observe", v(1, 0, "changes", "checkpoints", "chats")); err != nil {
		t.Fatal(err)
	}
	err = m.Check("observe", v(1, 0, "changes"))
	ce, _ := err.(*contract.Error)
	if ce == nil || ce.Code != contract.CodeWasyncTooOld || !strings.Contains(ce.Message, "checkpoints") || !strings.Contains(ce.Help, "upgrade fixture wasync") {
		t.Fatalf("missing feature: %v", err)
	}
	if code(m.Check("nope", v(1, 0))) != contract.CodeInternal {
		t.Fatal("unknown command passed")
	}
	if code(m.Check("observe", v(0, 1, "changes", "checkpoints"))) != contract.CodeWasyncTooOld {
		t.Fatal("api version not checked first")
	}
}

func TestParseRejectsBadFiles(t *testing.T) {
	for _, bad := range []string{
		`{}`,
		`{"format":2,"min_api":"1.0","upgrade_help":"x","commands":{"a":{"features":[]}}}`,
		`{"format":1,"min_api":"1","upgrade_help":"x","commands":{"a":{"features":[]}}}`,
		`{"format":1,"min_api":"2.0","upgrade_help":"x","commands":{"a":{"features":[]}}}`,
		`{"format":1,"min_api":"1.0","upgrade_help":"","commands":{"a":{"features":[]}}}`,
		`{"format":1,"min_api":"1.0","upgrade_help":"x","commands":{}}`,
		`{"format":1,"min_api":"1.0","upgrade_help":"x","commands":{"a":{"features":[]}},"extra":1}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
