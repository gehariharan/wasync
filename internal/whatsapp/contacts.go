package whatsapp

import (
	"context"
	"strings"
	"unicode"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// nameSource is the part of whatsmeow's contact and LID stores that name
// resolution reads. Names are never read from the session database directly.
type nameSource interface {
	GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error)
	GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error)
	GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error)
}

type storeNames struct {
	contacts interface {
		GetContact(context.Context, types.JID) (types.ContactInfo, error)
	}
	lids interface {
		GetPNForLID(context.Context, types.JID) (types.JID, error)
		GetLIDForPN(context.Context, types.JID) (types.JID, error)
	}
}

func (s storeNames) GetContact(ctx context.Context, u types.JID) (types.ContactInfo, error) {
	return s.contacts.GetContact(ctx, u)
}
func (s storeNames) GetPNForLID(ctx context.Context, u types.JID) (types.JID, error) {
	return s.lids.GetPNForLID(ctx, u)
}
func (s storeNames) GetLIDForPN(ctx context.Context, u types.JID) (types.JID, error) {
	return s.lids.GetLIDForPN(ctx, u)
}

func (c *Client) nameSource() nameSource {
	if c.names != nil {
		return c.names
	}
	if c.client == nil || c.client.Store == nil || c.client.Store.Contacts == nil || c.client.Store.LIDs == nil {
		return nil
	}
	return storeNames{c.client.Store.Contacts, c.client.Store.LIDs}
}

const maxContactNameRunes = 256

// cleanName trims a name and drops control characters so a hostile push name
// cannot carry terminal sequences or newlines into lists.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxContactNameRunes {
		s = string(r[:maxContactNameRunes])
	}
	return s
}

// directAliases returns the chat's own JID plus its phone-number or LID
// counterpart, because WhatsApp may key a contact under either one.
func directAliases(ctx context.Context, src nameSource, jid types.JID) []types.JID {
	jid = jid.ToNonAD()
	out := []types.JID{jid}
	var other types.JID
	var err error
	switch jid.Server {
	case types.HiddenUserServer:
		other, err = src.GetPNForLID(ctx, jid)
	case types.DefaultUserServer:
		other, err = src.GetLIDForPN(ctx, jid)
	}
	if err == nil && !other.IsEmpty() && other.User != "" && other != jid {
		out = append(out, other.ToNonAD())
	}
	return out
}

// resolveContactName picks the name WhatsApp would show for a direct chat:
// address-book full name, address-book first name, the person's own push name,
// business name. It returns "" when none is known so the caller keeps the
// number/JID. Lookup failures count as "no name".
func resolveContactName(ctx context.Context, src nameSource, jid types.JID) string {
	if src == nil || jid.Server == types.GroupServer {
		return ""
	}
	var infos []types.ContactInfo
	for _, a := range directAliases(ctx, src, jid) {
		if info, err := src.GetContact(ctx, a); err == nil && info.Found {
			infos = append(infos, info)
		}
	}
	for _, pick := range []func(types.ContactInfo) string{
		func(i types.ContactInfo) string { return i.FullName },
		func(i types.ContactInfo) string { return i.FirstName },
		func(i types.ContactInfo) string { return i.PushName },
		func(i types.ContactInfo) string { return i.BusinessName },
	} {
		for _, info := range infos {
			if name := cleanName(pick(info)); name != "" {
				return name
			}
		}
	}
	return ""
}

// refreshAllDirectNames re-resolves every stored direct chat. It runs after
// connecting (so archives from before this feature are filled) and after the
// contact app-state sync completes, which fills whatsmeow's contact store in
// bulk without per-contact events.
func (c *Client) refreshAllDirectNames(ctx context.Context, account int64) error {
	src := c.nameSource()
	if src == nil {
		return nil
	}
	chats, err := c.capture.store.Groups(ctx, account)
	if err != nil {
		return err
	}
	names := make(map[string]string)
	for _, g := range chats {
		jid, err := types.ParseJID(g.ID)
		if err != nil || !SupportedChat(jid) || jid.Server == types.GroupServer {
			continue
		}
		if name := resolveContactName(ctx, src, jid); name != "" && name != g.Name {
			names[g.ID] = name
		}
	}
	return c.capture.store.SaveDirectNames(ctx, account, names)
}

// refreshDirectNamesFor updates the chats a single contact event can affect.
func (c *Client) refreshDirectNamesFor(ctx context.Context, account int64, jids ...types.JID) error {
	src := c.nameSource()
	if src == nil {
		return nil
	}
	names := make(map[string]string)
	for _, jid := range jids {
		if jid.IsEmpty() || !SupportedChat(jid) || jid.Server == types.GroupServer {
			continue
		}
		for _, a := range directAliases(ctx, src, jid) {
			if name := resolveContactName(ctx, src, a); name != "" {
				names[a.String()] = name
			}
		}
	}
	return c.capture.store.SaveDirectNames(ctx, account, names)
}

// syncContactNames keeps direct-chat names current from whatsmeow's contact
// events. Names are personal data and are never logged.
func (c *Client) syncContactNames(evt any, account int64) error {
	ctx := c.capture.ctx
	switch e := evt.(type) {
	case *events.Connected:
		return c.refreshAllDirectNames(ctx, account)
	case *events.AppStateSyncComplete:
		if e.Name == appstate.WAPatchCriticalUnblockLow {
			return c.refreshAllDirectNames(ctx, account)
		}
	case *events.Contact:
		return c.refreshDirectNamesFor(ctx, account, e.JID)
	case *events.PushName:
		return c.refreshDirectNamesFor(ctx, account, e.JID, e.JIDAlt)
	case *events.BusinessName:
		return c.refreshDirectNamesFor(ctx, account, e.JID)
	}
	return nil
}
