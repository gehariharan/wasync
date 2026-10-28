package whatsapp

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// SupportedChat excludes status broadcasts, newsletters and protocol destinations.
func SupportedChat(jid types.JID) bool {
	return jid.User != "" && jid.User != "0" && (jid.Server == types.GroupServer || jid.Server == types.DefaultUserServer || jid.Server == types.HiddenUserServer)
}
func ParseChat(value string) (types.JID, error) {
	jid, err := types.ParseJID(value)
	if strings.ContainsAny(value, "/?#%\\") || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 || err != nil || !SupportedChat(jid) || jid.String() != value || jid.Device != 0 {
		return types.EmptyJID, fmt.Errorf("expected an exact group or direct chat JID")
	}
	return jid, nil
}
func (c *Client) discoverDirect(ctx context.Context, account int64, jid types.JID, name string) error {
	if !SupportedChat(jid) || jid.Server == types.GroupServer {
		return nil
	}
	// The contact store (address book, then push, then business name) outranks
	// the name carried by the event or history entry.
	if resolved := resolveContactName(ctx, c.nameSource(), jid); resolved != "" {
		name = resolved
	}
	if name == "" {
		name = jid.String()
	}
	return c.capture.store.SaveDiscoveredChat(ctx, account, storage.Group{ID: jid.String(), Name: name})
}

// ConfigurePairingHistory must run before connecting. This is an upstream pairing
// hint, not a full-history fetch API and not a replay for an already linked device.
func (c *Client) ConfigurePairingHistory(full bool) {
	store.DeviceProps.RequireFullSync = proto.Bool(full)
}
