package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/storage"
)

// A connected agent uses every chat synced in wasync, now and later, with no
// prompt for each chat. Its credential still names its chats, because reply
// and react need a named list; wasync keeps that list equal to the synced
// chats. The archive is the one record of which chats are synced, and the
// TUI, the CLI and these pages all change it, so the list is brought up to
// date here, before each request from such an agent, when the owner starts
// and when the Agents page opens. A chat synced or unsynced anywhere applies
// at the agent's next request. A chat switched on only so an agent's approved
// list can see replies is not shared: the list agent reads it through its
// list, and other agents get it once the person syncs it themselves.

// syncAgentChats brings every synced-chats agent's list up to date, and does
// nothing when there is no such agent.
func (h *handler) syncAgentChats(ctx context.Context) error {
	if has, err := agentauth.HasSyncedChats(h.dataDir); err != nil || !has {
		return err
	}
	return SyncAgentChats(ctx, h.store, h.dataDir)
}

// SyncAgentChats sets the chat list of every synced-chats agent in dir to the
// chats the person synced in store, and records each change in the agent audit log. With
// no paired account no chat is synced, so such agents use none.
func SyncAgentChats(ctx context.Context, store *storage.Store, dir string) error {
	chats, err := syncedChats(ctx, store, dir)
	if err != nil {
		return err
	}
	changed, err := agentauth.SetSyncedChats(dir, chats)
	if err != nil {
		return err
	}
	audit := agentauth.NewAuditLog(dir)
	for _, c := range changed {
		_ = audit.Append(agentauth.AuditEntry{Event: "synced_chats", ClientID: c.ID, ClientName: c.Name, Ref: fmt.Sprintf("%d chats", len(c.Chats)), Outcome: agentauth.OutcomeAllowed})
	}
	return nil
}

func syncedChats(ctx context.Context, store *storage.Store, dir string) ([]string, error) {
	if err := markEarlierListChats(ctx, store, dir); err != nil {
		return nil, err
	}
	account, err := store.Account(ctx)
	if err != nil || account == nil {
		return nil, err
	}
	groups, err := store.Groups(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	chats := []string{}
	for _, g := range groups {
		if g.PersonSynced() {
			chats = append(chats, g.ID)
		}
	}
	return chats, nil
}

// listCaptureMarker is written to the data directory once
// markEarlierListChats has run, so it runs only once.
const listCaptureMarker = "list-capture-backfill.done"

// markEarlierListChats marks, once, the synced chats of every number an
// approved list holds or ever messaged as list capture. Releases before
// list_capture switched those chats on like any other, so after an upgrade
// they would otherwise go to every connected agent. A chat the person synced
// that is also on a list is marked too; ticking it again in Choose chats
// makes it theirs.
func markEarlierListChats(ctx context.Context, store *storage.Store, dir string) error {
	marker := filepath.Join(dir, listCaptureMarker)
	if _, err := os.Lstat(marker); err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	account, err := store.Account(ctx)
	if err != nil || account == nil {
		return err
	}
	chats, err := sendlist.AllNumbers(dir)
	if err != nil {
		return err
	}
	if err = store.MarkListCapture(ctx, account.ID, chats); err != nil {
		return err
	}
	return localfs.EnsureFile(marker)
}
