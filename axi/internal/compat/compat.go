// Package compat decides whether the detected wasync can serve a command.
// The rules live in the embedded compatibility.json: the minimum v1 API and
// the wasync features each wasync-axi command requires.
//
// JSON keeps decoding within the standard library.
package compat

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/installhint"
)

//go:embed compatibility.json
var raw []byte

// Command lists what one wasync-axi command needs from wasync.
type Command struct {
	Features []string `json:"features"`
}

// Matrix is the parsed compatibility file.
type Matrix struct {
	Format      int                `json:"format"`
	MinAPI      string             `json:"min_api"`
	UpgradeHelp string             `json:"upgrade_help"`
	Commands    map[string]Command `json:"commands"`

	minMajor, minMinor int
}

// Parse parses and validates a compatibility file.
func Parse(b []byte) (*Matrix, error) {
	var m Matrix
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("compatibility file: %w", err)
	}
	if m.Format != 1 {
		return nil, fmt.Errorf("compatibility file: unsupported format %d", m.Format)
	}
	major, minor, err := parseAPI(m.MinAPI)
	if err != nil {
		return nil, err
	}
	if major != contract.APIVersion {
		return nil, fmt.Errorf("compatibility file: min_api %s does not match API major %d", m.MinAPI, contract.APIVersion)
	}
	if m.UpgradeHelp == "" || len(m.Commands) == 0 {
		return nil, fmt.Errorf("compatibility file: upgrade_help and commands are required")
	}
	m.minMajor, m.minMinor = major, minor
	return &m, nil
}

func parseAPI(s string) (int, int, error) {
	a, b, ok := strings.Cut(s, ".")
	major, err1 := strconv.Atoi(a)
	minor, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || major < 0 || minor < 0 {
		return 0, 0, fmt.Errorf("compatibility file: min_api %q is not MAJOR.MINOR", s)
	}
	return major, minor, nil
}

// Default returns the embedded matrix. The embedded file is validated by
// tests, so a parse failure is a build defect.
func Default() *Matrix {
	m, err := Parse(raw)
	if err != nil {
		panic(err)
	}
	m.UpgradeHelp = installhint.UpgradeCore()
	return m
}

// CommandNames returns the command names in the matrix, sorted.
func (m *Matrix) CommandNames() []string {
	out := make([]string, 0, len(m.Commands))
	for k := range m.Commands {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (m *Matrix) tooOld(format string, args ...any) *contract.Error {
	return &contract.Error{Code: contract.CodeWasyncTooOld, Message: fmt.Sprintf(format, args...), Help: "upgrade wasync: " + m.UpgradeHelp}
}

// CheckAPI verifies the API version is one this build speaks.
func (m *Matrix) CheckAPI(v contract.VersionInfo) error {
	switch {
	case v.APIVersion > contract.APIVersion:
		return &contract.Error{Code: contract.CodeAXITooOld, Message: fmt.Sprintf("wasync speaks API %s; this wasync-axi supports API %d.x", v.API(), contract.APIVersion), Help: "upgrade wasync-axi to a build that supports this wasync"}
	case v.APIVersion < m.minMajor || (v.APIVersion == m.minMajor && v.APIMinor < m.minMinor):
		return m.tooOld("wasync %s speaks API %s; wasync-axi needs API %s or newer", orUnknown(v.WasyncVersion), v.API(), m.MinAPI)
	}
	return nil
}

// Check verifies the API version and every feature cmd requires.
func (m *Matrix) Check(cmd string, v contract.VersionInfo) error {
	if err := m.CheckAPI(v); err != nil {
		return err
	}
	c, ok := m.Commands[cmd]
	if !ok {
		return &contract.Error{Code: contract.CodeInternal, Message: fmt.Sprintf("command %q has no compatibility entry", cmd)}
	}
	var missing []string
	for _, f := range c.Features {
		if !v.HasFeature(f) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return m.tooOld("wasync %s lacks feature(s) %s needed by `wasync-axi %s`", orUnknown(v.WasyncVersion), strings.Join(missing, ","), cmd)
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown version)"
	}
	return s
}
