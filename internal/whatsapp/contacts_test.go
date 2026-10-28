package whatsapp

import (
	"context"
	"errors"
	"testing"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// fakeNames is a synthetic stand-in for whatsmeow's contact and LID stores.
type fakeNames struct {
	contacts map[types.JID]types.ContactInfo
	pnForLID map[types.JID]types.JID
	lidForPN map[types.JID]types.JID
}

func newFakeNames() *fakeNames {
	return &fakeNames{contacts: map[types.JID]types.ContactInfo{}, pnForLID: map[types.JID]types.JID{}, lidForPN: map[types.JID]types.JID{}}
}
func (f *fakeNames) link(lid, pn types.JID) { f.pnForLID[lid] = pn; f.lidForPN[pn] = lid }
func (f *fakeNames) GetContact(_ context.Context, u types.JID) (types.ContactInfo, error) {
	return f.contacts[u], nil
}
func (f *fakeNames) GetPNForLID(_ context.Context, u types.JID) (types.JID, error) {
	if pn, ok := f.pnForLID[u]; ok {
		return pn, nil
	}
	return types.EmptyJID, errors.New("no mapping")
}
func (f *fakeNames) GetLIDForPN(_ context.Context, u types.JID) (types.JID, error) {
	if lid, ok := f.lidForPN[u]; ok {
		return lid, nil
	}
	return types.EmptyJID, errors.New("no mapping")
}

var (
	pnOne  = types.NewJID("15550000001", types.DefaultUserServer)
	lidOne = types.NewJID("90000000001", types.HiddenUserServer)
)

func TestResolveContactNamePrecedence(t *testing.T) {
	ctx := context.Background()
	full := types.ContactInfo{Found: true, FullName: "Full Name", FirstName: "First", PushName: "Push", BusinessName: "Biz"}
	cases := []struct {
		name string
		info types.ContactInfo
		want string
	}{
		{"full name wins", full, "Full Name"},
		{"first name next", types.ContactInfo{Found: true, FirstName: "First", PushName: "Push", BusinessName: "Biz"}, "First"},
		{"push name next", types.ContactInfo{Found: true, PushName: "Push", BusinessName: "Biz"}, "Push"},
		{"business name next", types.ContactInfo{Found: true, BusinessName: "Biz"}, "Biz"},
		{"nothing known", types.ContactInfo{Found: true}, ""},
		{"not found", types.ContactInfo{}, ""},
	}
	for _, tc := range cases {
		f := newFakeNames()
		f.contacts[pnOne] = tc.info
		if got := resolveContactName(ctx, f, pnOne); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestResolveContactNameFollowsLIDMapping(t *testing.T) {
	ctx := context.Background()
	f := newFakeNames()
	f.link(lidOne, pnOne)
	// The address book entry sits under the phone JID; the person's push name under the LID.
	f.contacts[pnOne] = types.ContactInfo{Found: true, FullName: "Saved Name"}
	f.contacts[lidOne] = types.ContactInfo{Found: true, PushName: "Their Own Name"}
	if got := resolveContactName(ctx, f, lidOne); got != "Saved Name" {
		t.Fatalf("LID chat: got %q", got)
	}
	delete(f.contacts, pnOne)
	if got := resolveContactName(ctx, f, pnOne); got != "Their Own Name" {
		t.Fatalf("phone chat falls back to the LID's push name: got %q", got)
	}
	if got := resolveContactName(ctx, f, types.NewJID("123", types.GroupServer)); got != "" {
		t.Fatalf("groups never get a contact name: got %q", got)
	}
	if got := resolveContactName(ctx, nil, pnOne); got != "" {
		t.Fatalf("no store: got %q", got)
	}
}

func TestCleanNameStripsControlCharactersAndLimitsLength(t *testing.T) {
	if got := cleanName("  A\x1b[31mB\nC\t D  "); got != "A [31mB C D" {
		t.Fatalf("got %q", got)
	}
	long := make([]rune, 1000)
	for i := range long {
		long[i] = 'é'
	}
	if got := cleanName(string(long)); len([]rune(got)) != maxContactNameRunes {
		t.Fatalf("length %d", len([]rune(got)))
	}
}

func nameOf(t *testing.T, s *storage.Store, account int64, id string) (string, bool) {
	t.Helper()
	groups, err := s.Groups(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID == id {
			return g.Name, true
		}
	}
	return "", false
}

func TestEventsKeepDirectChatNamesCurrent(t *testing.T) {
	c, s, account := historyClient(t)
	ctx := context.Background()
	f := newFakeNames()
	f.link(lidOne, pnOne)
	c.names = f
	otherLID := types.NewJID("90000000002", types.HiddenUserServer)
	for _, g := range []storage.Group{{ID: pnOne.String(), Name: pnOne.String()}, {ID: lidOne.String(), Name: "masked"}, {ID: otherLID.String(), Name: otherLID.String()}} {
		if err := s.SaveDiscoveredChat(ctx, account, g); err != nil {
			t.Fatal(err)
		}
	}

	// Existing chats are filled after connecting, including the first run after an upgrade.
	f.contacts[pnOne] = types.ContactInfo{Found: true, FullName: "Alex Example", PushName: "alex"}
	if !c.handleEvent(&events.Connected{}) {
		t.Fatal("event rejected")
	}
	for _, id := range []string{pnOne.String(), lidOne.String()} {
		if got, _ := nameOf(t, s, account, id); got != "Alex Example" {
			t.Fatalf("%s: got %q after connect", id, got)
		}
	}
	if got, _ := nameOf(t, s, account, otherLID.String()); got != otherLID.String() {
		t.Fatalf("unknown contact must keep its JID name, got %q", got)
	}
	if got, _ := nameOf(t, s, account, "123@g.us"); got != "enabled" {
		t.Fatalf("group renamed to %q", got)
	}

	// A contact change renames both the phone and LID chat of the same person.
	f.contacts[pnOne] = types.ContactInfo{Found: true, FullName: "Alex Renamed"}
	c.handleEvent(&events.Contact{JID: pnOne})
	for _, id := range []string{pnOne.String(), lidOne.String()} {
		if got, _ := nameOf(t, s, account, id); got != "Alex Renamed" {
			t.Fatalf("%s: got %q after contact event", id, got)
		}
	}

	// A push name fills a chat that had no better name.
	f.contacts[otherLID] = types.ContactInfo{Found: true, PushName: "Pat Push"}
	c.handleEvent(&events.PushName{JID: otherLID})
	if got, _ := nameOf(t, s, account, otherLID.String()); got != "Pat Push" {
		t.Fatalf("push name: got %q", got)
	}
	// ...and a business name only when nothing outranks it.
	f.contacts[otherLID] = types.ContactInfo{Found: true, PushName: "Pat Push", BusinessName: "Pat Shop"}
	c.handleEvent(&events.BusinessName{JID: otherLID})
	if got, _ := nameOf(t, s, account, otherLID.String()); got != "Pat Push" {
		t.Fatalf("business name outranked push name: %q", got)
	}

	// The bulk contact sync fills the store without per-contact events.
	f.contacts[otherLID] = types.ContactInfo{Found: true, FullName: "Pat Saved"}
	c.handleEvent(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
	if got, _ := nameOf(t, s, account, otherLID.String()); got != "Pat Push" {
		t.Fatalf("unrelated sync should not refresh names, got %q", got)
	}
	c.handleEvent(&events.AppStateSyncComplete{Name: appstate.WAPatchCriticalUnblockLow})
	if got, _ := nameOf(t, s, account, otherLID.String()); got != "Pat Saved" {
		t.Fatalf("contact sync: got %q", got)
	}

	// Contact events never create chats.
	stranger := types.NewJID("15550000099", types.DefaultUserServer)
	f.contacts[stranger] = types.ContactInfo{Found: true, FullName: "Not A Chat"}
	c.handleEvent(&events.Contact{JID: stranger})
	if _, ok := nameOf(t, s, account, stranger.String()); ok {
		t.Fatal("contact event created a chat")
	}
}

func TestDiscoverDirectPrefersContactNameOverEventName(t *testing.T) {
	c, s, account := historyClient(t)
	ctx := context.Background()
	f := newFakeNames()
	c.names = f
	f.contacts[pnOne] = types.ContactInfo{Found: true, FullName: "Saved Name"}
	if err := c.discoverDirect(ctx, account, pnOne, "history display name"); err != nil {
		t.Fatal(err)
	}
	if got, _ := nameOf(t, s, account, pnOne.String()); got != "Saved Name" {
		t.Fatalf("got %q", got)
	}
	// Without a contact entry the event's name is still used, then the JID.
	other := types.NewJID("15550000002", types.DefaultUserServer)
	if err := c.discoverDirect(ctx, account, other, "pushed"); err != nil {
		t.Fatal(err)
	}
	if got, _ := nameOf(t, s, account, other.String()); got != "pushed" {
		t.Fatalf("got %q", got)
	}
	third := types.NewJID("15550000003", types.DefaultUserServer)
	if err := c.discoverDirect(ctx, account, third, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := nameOf(t, s, account, third.String()); got != third.String() {
		t.Fatalf("got %q", got)
	}
}

func TestNameRefreshFailureDoesNotStopCapture(t *testing.T) {
	c, s, account := historyClient(t)
	f := newFakeNames()
	c.names = f
	f.contacts[pnOne] = types.ContactInfo{Found: true, FullName: "Alex Example"}
	if err := s.SaveDiscoveredChat(context.Background(), account, storage.Group{ID: pnOne.String(), Name: pnOne.String()}); err != nil {
		t.Fatal(err)
	}
	c.capture.metadataOnly = true

	live := c.capture.ctx
	cancelled, cancel := context.WithCancel(live)
	cancel()
	c.capture.ctx = cancelled
	if !c.handleEvent(&events.Connected{}) {
		t.Fatal("a failed name refresh stopped capture")
	}
	if c.capture.failed.Load() {
		t.Fatal("capture marked failed by a name refresh error")
	}
	if got, _ := nameOf(t, s, account, pnOne.String()); got != pnOne.String() {
		t.Fatalf("name changed despite failed refresh: %q", got)
	}

	c.capture.ctx = live
	if !c.handleEvent(&events.Contact{JID: pnOne}) {
		t.Fatal("event rejected after recovery")
	}
	if got, _ := nameOf(t, s, account, pnOne.String()); got != "Alex Example" {
		t.Fatalf("refresh did not retry on the next event: %q", got)
	}
}
