package api

import (
	"context"
	"fmt"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/storage"
)

// A connected agent uses every chat synced in wasync, now and later, with no
// prompt for each chat. Its credential still names its chats, because reply
// and react need a named list; wasync keeps that list equal to the synced
// chats. The archive is the one record of which chats are synced, and the
// TUI, the CLI and these pages all change it, so the list is brought up to
// date here, before each request from such an agent, when the owner starts
// and when the Agents page opens. A chat synced or unsynced anywhere applies
// at the agent's next request.

// syncAgentChats brings every synced-chats agent's list up to date, and does
// nothing when there is no such agent.
func (h *handler) syncAgentChats(ctx context.Context) error {
	if has, err := agentauth.HasSyncedChats(h.dataDir); err != nil || !has {
		return err
	}
	return SyncAgentChats(ctx, h.store, h.dataDir)
}

// SyncAgentChats sets the chat list of every synced-chats agent in dir to the
// chats synced in store, and records each change in the agent audit log. With
// no paired account no chat is synced, so such agents use none.
func SyncAgentChats(ctx context.Context, store *storage.Store, dir string) error {
	chats, err := syncedChats(ctx, store)
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

func syncedChats(ctx context.Context, store *storage.Store) ([]string, error) {
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
		if g.Enabled {
			chats = append(chats, g.ID)
		}
	}
	return chats, nil
}
