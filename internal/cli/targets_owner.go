package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The caller has already validated command syntax before acquiring the lock.
func (a *App) targetsThroughOwner(ctx context.Context, cfg config.Config, args []string) error {
	if cfg.NoAPI {
		return fmt.Errorf("owner is running without a command channel; stop it first")
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	base := "/api/" + args[0]
	if args[1] != "list" {
		path := base + "/" + args[2] + "/" + args[1]
		if args[2] == "--all" {
			path = base + "/" + args[1] + "-all"
		}
		return a.ownerCommand(ctx, cfg, "POST", path, nil, "api.token")
	}
	for _, arg := range args[2:] {
		if arg == "--refresh" || arg == "--refresh=true" {
			silent := *a
			silent.Out = io.Discard
			if err := silent.ownerCommand(ctx, cfg, "POST", "/api/sync", []byte(`{"wait_seconds":0}`), "send.token"); err != nil {
				return err
			}
		}
	}
	tokenPath := filepath.Join(cfg.DataDir, "api.token")
	if err := localfs.CheckPrivateFile(tokenPath); err != nil {
		return err
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return err
	}
	response, err := commandRequest(ctx, cfg, "GET", base, nil, token)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("cannot list chats: HTTP %d", response.StatusCode)
	}
	var page struct {
		Items []query.Group `json:"items"`
	}
	if err = json.NewDecoder(response.Body).Decode(&page); err != nil {
		return err
	}
	groups := make([]storage.Group, 0, len(page.Items))
	for _, g := range page.Items {
		groups = append(groups, storage.Group{ID: g.ID, Name: g.Name, Enabled: g.Enabled, MediaPolicy: g.MediaPolicy, MessageCount: g.MessageCount, LastMessage: g.LastMessage, HistoryState: g.HistoryState, HistoryRequest: g.HistoryRequest, GapCount: g.PotentialChatGaps, AccountGapCount: g.PotentialAccountGaps})
	}
	return a.printGroups(groups, strings.Contains(strings.Join(args[2:], " "), "--json") && !strings.Contains(strings.Join(args[2:], " "), "--json=false"))
}
