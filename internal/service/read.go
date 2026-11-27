package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Reader is a Manager that can say what its login item starts. The wasync app
// uses it to take over from the `wasync service install` item only when that
// item starts wasync for the same data directory as the app.
type Reader interface {
	// Read returns the program the item starts and the data directory it
	// names. It returns ErrNotInstalled when there is no item.
	Read() (Spec, error)
}

// Read returns what m's login item starts. It returns ErrNotInstalled when
// there is no item, and ErrUnsupported when m cannot read its item back.
func Read(m Manager) (Spec, error) {
	r, ok := m.(Reader)
	if !ok {
		return Spec{}, ErrUnsupported
	}
	return r.Read()
}

func (l launchAgent) Read() (Spec, error) { return readPlist(l.path()) }

func (a appAgent) Read() (Spec, error) { return readPlist(a.path()) }

// readPlist reads a LaunchAgent this package wrote, or one an earlier version
// wrote: both name the program first and the data directory after --data-dir.
func readPlist(path string) (Spec, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Spec{}, ErrNotInstalled
	}
	if err != nil {
		return Spec{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return Spec{}, err
	}
	args, err := programArguments(b)
	if err != nil {
		return Spec{}, fmt.Errorf("read the login item %s: %w", path, err)
	}
	return specFromArgs(args)
}

// specFromArgs finds the program and the data directory in a command line.
func specFromArgs(args []string) (Spec, error) {
	if len(args) == 0 || args[0] == "" {
		return Spec{}, errors.New("the login item names no program")
	}
	s := Spec{Exe: args[0]}
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--data-dir" {
			s.DataDir = args[i+1]
		}
	}
	if s.DataDir == "" {
		return Spec{}, errors.New("the login item names no data directory")
	}
	return s, nil
}

// programArguments returns the ProgramArguments array of a property list's
// top-level dictionary.
func programArguments(b []byte) ([]string, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	// depth counts open elements: plist is 1, its dict 2, the dict's keys and
	// values 3, and the strings of an array value 4.
	depth, key, inArgs, found := 0, "", false, false
	var args []string
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 3 && t.Name.Local == "key":
				var s string
				if err := d.DecodeElement(&s, &t); err != nil {
					return nil, err
				}
				key = strings.TrimSpace(s)
				depth--
			case depth == 3 && key == "ProgramArguments" && t.Name.Local == "array":
				inArgs = true
			case depth == 4 && inArgs && t.Name.Local == "string":
				var s string
				if err := d.DecodeElement(&s, &t); err != nil {
					return nil, err
				}
				args = append(args, s)
				depth--
			}
		case xml.EndElement:
			if depth == 3 {
				if inArgs {
					inArgs, found = false, true
				}
				key = ""
			}
			depth--
		}
	}
	if !found {
		return nil, errors.New("it has no ProgramArguments")
	}
	return args, nil
}

// ParseRunCommand reads back the data directory and program from a line
// RunCommand made, as Windows keeps it under the Run key.
func ParseRunCommand(line string) (Spec, error) {
	exe, rest, ok := singleQuoted(line, "-FilePath '")
	if !ok {
		return Spec{}, errors.New("the startup item names no program")
	}
	argList, _, ok := singleQuoted(rest, "-ArgumentList '")
	if !ok {
		return Spec{}, errors.New("the startup item has no arguments")
	}
	argList = strings.ReplaceAll(argList, `\"`, `"`)
	dir, ok := strings.CutPrefix(argList, `--data-dir "`)
	if !ok {
		return Spec{}, errors.New("the startup item names no data directory")
	}
	dir, ok = strings.CutSuffix(dir, `" serve`)
	if !ok || dir == "" {
		return Spec{}, errors.New("the startup item names no data directory")
	}
	return Spec{Exe: exe, DataDir: dir}, nil
}

// singleQuoted finds prefix in s and returns the PowerShell single-quoted text
// that follows it, where two quotes in a row stand for one, and what comes
// after.
func singleQuoted(s, prefix string) (text, rest string, ok bool) {
	i := strings.Index(s, prefix)
	if i < 0 {
		return "", "", false
	}
	s = s[i+len(prefix):]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		return b.String(), s[i+1:], true
	}
	return "", "", false
}

// ParseAppRunCommand reads back a line AppRunCommand made.
func ParseAppRunCommand(line string) (Spec, error) {
	return specFromArgs(splitWindowsArgs(line))
}

// splitWindowsArgs splits a command line the way Windows programs do, the
// reverse of windowsArg.
func splitWindowsArgs(line string) []string {
	var args []string
	var b strings.Builder
	inQuote, started := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\':
			n := 0
			for i < len(line) && line[i] == '\\' {
				n++
				i++
			}
			if i < len(line) && line[i] == '"' {
				b.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					b.WriteByte('"')
				} else {
					inQuote = !inQuote
				}
			} else {
				b.WriteString(strings.Repeat(`\`, n))
				i--
			}
			started = true
		case c == '"':
			inQuote, started = !inQuote, true
		case (c == ' ' || c == '\t') && !inQuote:
			if started {
				args = append(args, b.String())
				b.Reset()
				started = false
			}
		default:
			b.WriteByte(c)
			started = true
		}
	}
	if started {
		args = append(args, b.String())
	}
	return args
}
