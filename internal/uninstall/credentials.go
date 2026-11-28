package uninstall

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/agentconnect"
	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/axicred"
)

// forgetApproval removes the Keychain item that holds dataDir's approval
// credential, and any other item recorded for the same folder. Where the
// credential is a file in the data directory there is nothing to do here. It
// returns what it did, in words.
func forgetApproval(dataDir string) []string {
	const removed = "Removed the stored approval credential, if there was one."
	if !approvecred.InKeychain() {
		return []string{removed}
	}
	account, err := approvecred.Account(dataDir)
	if err == nil {
		err = forgetOne(account)
	}
	said := []string{removed}
	if err != nil {
		said = []string{"Could not remove the approval credential from the Keychain: " + err.Error()}
	}
	if items, err := approvecred.Items(); err == nil {
		for _, it := range items {
			if it.Account == account || it.Folder == "" || !equalPath(it.Folder, dataDir) {
				continue
			}
			switch err := forgetOne(it.Account); {
			case errors.Is(err, approvecred.ErrForeignItem):
				said = append(said, fmt.Sprintf("Kept the Keychain item %q for this data directory: another program made it, so delete it in Keychain Access.", it.Account))
			case err != nil:
				said = append(said, fmt.Sprintf("Could not remove the Keychain item %q: %v", it.Account, err))
			}
		}
	}
	return said
}

func forgetOne(account string) error {
	if err := approvecred.Forget(account); err != nil && !errors.Is(err, approvecred.ErrNotFound) {
		return err
	}
	return nil
}

// removeAgentCredentials removes the agent credentials wasync saved for
// wasync-axi that belong to dataDir: the credential file wasync-axi reads
// (axiPath, and the one under its old whatsapp-axi name) and the one in each
// per-app folder ("agents/<kind>" beside it) that Connect an agent makes. A
// credential belongs to dataDir when its client is in dataDir's agent
// records; one for another data directory, or a file that is not a wasync
// credential, is left alone. Folders are removed only once empty, so nothing
// else the person keeps there is touched. It returns what it did, in words.
func removeAgentCredentials(dataDir, axiPath string) []string {
	clients, err := agentauth.List(dataDir)
	if err != nil {
		return []string{"Could not read the connected agents' records, so their credentials were kept: " + err.Error()}
	}
	ours := map[string]bool{}
	for _, c := range clients {
		ours[c.ID] = true
	}
	if len(ours) == 0 {
		return nil
	}
	var said []string
	remove := func(path string) bool {
		removed, err := removeCredential(path, ours)
		switch {
		case err != nil:
			said = append(said, fmt.Sprintf("Could not remove the agent credential %s: %v", path, err))
		case removed:
			said = append(said, "Removed the agent credential "+path+".")
		}
		return removed
	}
	dir := filepath.Dir(axiPath)
	remove(axiPath)
	if filepath.Base(dir) == axicred.DirName {
		legacy := filepath.Join(filepath.Dir(dir), axicred.LegacyDirName, axicred.FileName)
		if remove(legacy) {
			_ = os.Remove(filepath.Dir(legacy))
		}
	}
	agents := filepath.Join(dir, "agents")
	for _, a := range agentconnect.Agents {
		folder := filepath.Join(agents, string(a.Kind))
		remove(filepath.Join(folder, axicred.FileName))
		// Only an empty folder goes: Connect an agent made it for wasync.
		_ = os.Remove(folder)
	}
	_ = os.Remove(agents)
	// wasync-axi's own folder goes too once nothing is left in it; a folder
	// the person chose with WASYNC_AXI_CONFIG_DIR is theirs and stays.
	if filepath.Base(dir) == axicred.DirName {
		_ = os.Remove(dir)
	}
	return said
}

// removeCredential removes path when it is a regular file holding a wasync
// agent credential for one of the clients in ours.
func removeCredential(path string, ours map[string]bool) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	b, err := io.ReadAll(io.LimitReader(f, 4<<10))
	f.Close()
	if err != nil {
		return false, err
	}
	id, _, err := agentauth.ParseCredential(strings.TrimSpace(string(b)))
	if err != nil || !ours[id] {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
