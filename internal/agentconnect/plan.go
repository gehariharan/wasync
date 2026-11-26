package agentconnect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"sort"
)

// Entry is the tool server wasync writes into an app's settings.
type Entry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

func (e Entry) envKeys() []string {
	keys := make([]string, 0, len(e.Env))
	for k := range e.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonEntry is how the entry looks in kind's JSON file.
func (e Entry) jsonEntry(kind Kind) ([]byte, error) {
	v := struct {
		Type    string            `json:"type,omitempty"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
	}{Command: e.Command, Args: e.Args, Env: e.Env}
	if v.Args == nil {
		v.Args = []string{}
	}
	if kind == ClaudeCode {
		v.Type = "stdio" // Claude Code also accepts it missing; naming it is clearer
	}
	return json.MarshalIndent(v, "", "  ")
}

// matches reports whether a decoded entry (from TOML or JSON) is e.
func (e Entry) matches(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || m["command"] != e.Command {
		return false
	}
	args, _ := m["args"].([]any)
	if len(args) != len(e.Args) {
		return false
	}
	for i, a := range args {
		if a != e.Args[i] {
			return false
		}
	}
	env := map[string]string{}
	if raw, ok := m["env"].(map[string]any); ok {
		for k, v := range raw {
			s, ok := v.(string)
			if !ok {
				return false
			}
			env[k] = s
		}
	}
	want := e.Env
	if want == nil {
		want = map[string]string{}
	}
	return reflect.DeepEqual(env, want)
}

// Plan is one change to an app's settings file, shown to the person before
// it is made.
type Plan struct {
	Agent Agent
	// Path is the settings file; Exists says whether it exists yet.
	Path   string
	Exists bool
	// Removed is the exact text taken out: an earlier "whatsapp" entry that
	// the new one replaces, or wasync's own entry when disconnecting.
	Removed string
	// Added is the exact text put in.
	Added string
	// Digest identifies the file and the entry change, so what is written
	// is what was shown. The rest of the file is not part of it: the app may
	// rewrite its other settings in the meantime, and those bytes are kept.
	Digest string

	file    file
	after   []byte
	set     *Entry
	restore string
}

// Changes reports whether writing the plan would change the file.
func (p Plan) Changes() bool { return string(p.after) != string(p.file.Data) }

func (p Plan) digest() string {
	h := sha256.New()
	for _, s := range []string{p.file.Target, fmt.Sprint(p.Exists), p.Removed, p.Added} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// sameChange reports whether q makes the change p showed.
func (p Plan) sameChange(q Plan) bool {
	return p.file.Target == q.file.Target && p.Exists == q.Exists && p.Removed == q.Removed && p.Added == q.Added
}

// edit applies set (nil only removes wasync's entry, restore puts back an
// entry text that was there before) to agent a's settings file at path.
func (e Env) edit(a Agent, path string, set *Entry, restore string) (Plan, error) {
	p := Plan{Agent: a, Path: path, set: set, restore: restore}
	f, err := readFile(p.Path)
	if err != nil {
		return p, fmt.Errorf("wasync cannot read %s: %w", p.Path, err)
	}
	p.file, p.Exists = f, f.Exists
	fail := func(err error) (Plan, error) {
		return p, fmt.Errorf("wasync cannot change %s safely: %w", p.Path, err)
	}
	switch a.Kind {
	case Codex:
		content := string(f.Data)
		block := restore
		if set != nil {
			block = tomlEntry(*set, newline(content))
		}
		out, removed, err := editTOML(content, block)
		if err != nil {
			return fail(err)
		}
		p.after, p.Removed = []byte(out), removed
		if set != nil {
			p.Added = block
		} else {
			p.Added = restore
		}
	case ClaudeCode, ClaudeDesktop:
		var raw []byte
		switch {
		case set != nil:
			if raw, err = set.jsonEntry(a.Kind); err != nil {
				return fail(err)
			}
		case restore != "":
			raw = []byte(restore)
		}
		out, removed, err := editJSON(f.Data, raw)
		if err != nil {
			return fail(err)
		}
		if err := checkJSON(f.Data, out, raw); err != nil {
			return fail(err)
		}
		p.after, p.Removed = out, removed
		if raw != nil {
			p.Added = fmt.Sprintf("%q: %s", ServerName, raw)
		}
		if removed != "" {
			p.Removed = fmt.Sprintf("%q: %s", ServerName, removed)
		}
	default:
		return p, fmt.Errorf("unknown agent %q", a.Kind)
	}
	p.Digest = p.digest()
	return p, nil
}

// PlanConnect previews adding entry to agent a's settings.
func (e Env) PlanConnect(a Agent, entry Entry) (Plan, error) {
	return e.edit(a, e.ConfigPath(a.Kind), &entry, "")
}

// Previous is the entry text a connect plan replaces, in the form a later
// disconnect puts back: the TOML table, or the raw JSON value.
func (p Plan) Previous() string {
	if p.Removed == "" || p.Agent.Kind == Codex {
		return p.Removed
	}
	return p.Removed[len(fmt.Sprintf("%q: ", ServerName)):]
}

// PlanDisconnect previews taking c's entry out of its app's settings and
// putting back the entry it replaced. When the file no longer holds the
// entry wasync wrote (the person removed or changed it), the plan changes
// nothing and kept says whether a changed entry was left in place.
func (e Env) PlanDisconnect(c Connection) (p Plan, kept bool, err error) {
	a, ok := Lookup(string(c.Agent))
	if !ok {
		return p, false, fmt.Errorf("unknown agent %q", c.Agent)
	}
	cur, err := current(a, c.ConfigPath)
	if err != nil {
		return Plan{Agent: a, Path: c.ConfigPath}, false, fmt.Errorf("wasync cannot read %s: %w", c.ConfigPath, err)
	}
	if !c.Entry.matches(cur) {
		f, err := readFile(c.ConfigPath)
		p = Plan{Agent: a, Path: c.ConfigPath, Exists: f.Exists, file: f, after: f.Data}
		return p, cur != nil, err
	}
	p, err = e.edit(a, c.ConfigPath, nil, c.Previous)
	return p, false, err
}

// current reports the entry the settings file at path holds now (nil when none).
func current(a Agent, path string) (any, error) {
	f, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if a.Kind == Codex {
		return tomlCurrent(string(f.Data))
	}
	return jsonCurrent(f.Data)
}

// Wrote reports whether c's settings file still holds the entry wasync wrote.
func Wrote(c Connection) bool {
	a, ok := Lookup(string(c.Agent))
	if !ok {
		return false
	}
	cur, err := current(a, c.ConfigPath)
	return err == nil && c.Entry.matches(cur)
}

// Write makes the change p describes, after backing up the file. It plans
// the change again against the file as it is now, keeping whatever else
// changed in it, and refuses when the entry it would take out or put in is
// not exactly what p showed. It returns the backup's path ("" when the file
// did not exist).
func (e Env) Write(p Plan) (backupPath string, err error) {
	if !p.Changes() {
		return "", nil
	}
	q, err := e.edit(p.Agent, p.Path, p.set, p.restore)
	if err != nil {
		return "", err
	}
	if !p.sameChange(q) {
		return "", ErrChanged
	}
	if !q.Changes() {
		return "", nil
	}
	f := q.file
	if backupPath, err = backup(f, e.now().UTC().Format("20060102-150405")); err != nil {
		return "", err
	}
	if err := writeAtomic(f.Target, q.after, f.Mode); err != nil {
		return backupPath, fmt.Errorf("write %s: %w", p.Path, err)
	}
	return backupPath, nil
}

func isWindows() bool { return runtime.GOOS == "windows" }
