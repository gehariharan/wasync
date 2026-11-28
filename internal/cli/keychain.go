package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/config"
)

// keychainRow is one stored approval credential and what prune would do with it.
type keychainRow struct {
	approvecred.Item
	Remove  bool
	Unknown bool
	Why     string
}

// planKeychain decides, for each approval credential, whether it is still wanted.
// Each data directory has exactly one, named after the directory, so a credential
// is a leftover when its directory is gone. Items made before wasync recorded the
// directory cannot be checked, so they are never removed.
func planKeychain(items []approvecred.Item, current string) []keychainRow {
	var rows []keychainRow
	for _, item := range items {
		if !strings.HasPrefix(item.Account, "approve:") {
			continue
		}
		row := keychainRow{Item: item}
		switch {
		case item.Account == current:
			row.Why = "this data directory"
		case item.Folder == "":
			row.Unknown, row.Why = true, "unknown folder - not removed"
		default:
			if _, err := os.Stat(item.Folder); errors.Is(err, fs.ErrNotExist) {
				row.Remove, row.Why = true, "its folder is gone: "+item.Folder
			} else {
				row.Why = "its folder exists: " + item.Folder
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Account < rows[j].Account })
	return rows
}

// runKeychain lists wasync's Keychain items, or removes the leftover ones. It
// reads only item names, never a secret, so listing raises no prompt.
func (a *App) runKeychain(cfg config.Config, args []string) error {
	const usage = "usage: wasync keychain [prune [--apply]]"
	apply := false
	switch {
	case len(args) == 0:
	case args[0] == "prune":
		set := a.flags("keychain prune")
		set.BoolVar(&apply, "apply", false, "remove the leftover items instead of only listing them")
		if err := a.parseFlags(set, args[1:]); err != nil {
			return err
		}
		if set.NArg() != 0 {
			return errors.New(usage)
		}
	default:
		return errors.New(usage)
	}
	items, err := approvecred.Items()
	if errors.Is(err, errors.ErrUnsupported) {
		_, err = fmt.Fprintln(a.Out, "This wasync keeps the approval credential in a private file in the data directory, not the Keychain (only a signed macOS release uses the Keychain), so there is nothing to clean up.")
		return err
	}
	if err != nil {
		return err
	}
	current, err := approvecred.Account(cfg.DataDir)
	if err != nil {
		return err
	}
	rows := planKeychain(items, current)
	leftover, unknown := 0, 0
	for _, row := range rows {
		verb := "keep  "
		switch {
		case row.Remove:
			verb = "remove"
			leftover++
		case row.Unknown:
			verb = "skip  "
			unknown++
		}
		fmt.Fprintf(a.Out, "%s  %s  %s\n", verb, row.Account, row.Why)
	}
	if unknown > 0 {
		fmt.Fprintf(a.Out, "%d item(s) were made before wasync recorded their folder, so prune cannot tell whether they are still in use and never removes them. To remove one by hand, open Keychain Access, search for wasync, and delete the item with that name.\n", unknown)
	}
	switch {
	case len(rows) == 0:
		_, err = fmt.Fprintln(a.Out, "wasync has no approval credentials in the Keychain.")
	case leftover == 0:
		_, err = fmt.Fprintln(a.Out, "Nothing to remove.")
	case !apply:
		_, err = fmt.Fprintf(a.Out, "%d to remove. wasync keychain prune --apply removes them; the item for this data directory is never removed.\n", leftover)
	}
	if err != nil || !apply || leftover == 0 {
		return err
	}
	var failed []string
	removed := 0
	for _, row := range rows {
		if !row.Remove {
			continue
		}
		if err := approvecred.Forget(row.Account); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", row.Account, err))
			continue
		}
		removed++
	}
	fmt.Fprintf(a.Out, "Removed %d.\n", removed)
	if len(failed) > 0 {
		return fmt.Errorf("could not remove %d item(s):\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	return nil
}
