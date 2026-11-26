package whatsapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/mediafiles"
	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func activityOf(t *testing.T, s *storage.Store, account int64, chat string) *time.Time {
	t.Helper()
	c, ok, err := s.PickerChat(context.Background(), account, chat)
	if err != nil || !ok {
		t.Fatalf("chat %s: %v %v", chat, ok, err)
	}
	return c.LastActive
}

// A chat that is not synced keeps none of its messages, but when it was last
// active is remembered, so the chat picker can list recent chats first.
func TestUnsyncedChatsRememberWhenTheyWereActiveButNotTheirMessages(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(privateTestDir(t), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account, err := s.UseAccount(ctx, "test@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	group, quiet := "120363000000000021@g.us", "120363000000000022@g.us"
	if err = s.SaveGroups(ctx, account, []storage.Group{{ID: group, Name: "Not synced"}, {ID: quiet, Name: "Quiet"}}); err != nil {
		t.Fatal(err)
	}
	c, err := Open(ctx, filepath.Join(privateTestDir(t), "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.capture = &capture{ctx: ctx, store: s}
	c.capture.account.Store(account)

	// The link-time chat list names when each conversation was last active.
	listed := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	direct := "15550002222@s.whatsapp.net"
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_RECENT.Enum(), Conversations: []*waHistorySync.Conversation{
		{ID: proto.String(group), ConversationTimestamp: proto.Uint64(uint64(listed.Unix())), Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String(group), ID: proto.String("H1")}, MessageTimestamp: proto.Uint64(uint64(listed.Add(-time.Hour).Unix())), Message: &waE2E.Message{Conversation: proto.String("not kept")}}}}},
		{ID: proto.String(direct), Name: proto.String("Ravi"), LastMsgTimestamp: proto.Uint64(uint64(listed.Add(time.Hour).Unix()))},
		{ID: proto.String(quiet), ConversationTimestamp: proto.Uint64(uint64(time.Now().Add(72 * time.Hour).Unix()))},
	}}}
	if err = c.captureEvent(history); err != nil {
		t.Fatal(err)
	}
	if got := activityOf(t, s, account, group); got == nil || !got.Equal(listed) {
		t.Fatalf("group activity: %v", got)
	}
	if got := activityOf(t, s, account, direct); got == nil || !got.Equal(listed.Add(time.Hour)) {
		t.Fatalf("direct chat activity: %v", got)
	}
	if got := activityOf(t, s, account, quiet); got != nil {
		t.Fatalf("a time days in the future was kept: %v", got)
	}
	// A live message moves it forward; an older time never moves it back.
	live := time.Now().Add(-time.Minute).Truncate(time.Second)
	for _, at := range []time.Time{live, listed.Add(-24 * time.Hour)} {
		msg := &waE2E.Message{Conversation: proto.String("still not kept")}
		chat := types.NewJID("120363000000000021", types.GroupServer)
		if err = c.captureEvent(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: types.NewJID("456", types.HiddenUserServer), IsGroup: true}, ID: "L1", Timestamp: at}, Message: msg, RawMessage: msg}); err != nil {
			t.Fatal(err)
		}
	}
	if got := activityOf(t, s, account, group); got == nil || !got.Equal(live) {
		t.Fatalf("live activity: %v", got)
	}
	if n, err := s.CountMessages(ctx, account, group, storage.MessageQuery{}, nil); err != nil || n != 0 {
		t.Fatalf("an unsynced chat kept %d messages: %v", n, err)
	}
	// Purging a chat forgets its activity too.
	if err = s.PurgeGroup(ctx, account, group); err != nil {
		t.Fatal(err)
	}
	if got := activityOf(t, s, account, group); got != nil {
		t.Fatalf("activity survived a purge: %v", got)
	}
}

var jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}

func TestProfilePictureAsksForThePreviewAndAcceptsOnlyJPEG(t *testing.T) {
	ctx := context.Background()
	chat := "120363000000000031@g.us"
	var asked *whatsmeow.GetProfilePictureParams
	var answer *types.ProfilePictureInfo
	var answerErr error
	body := jpegBytes
	c := &Client{
		pictureInfo: func(_ context.Context, jid types.JID, p *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error) {
			asked = p
			return answer, answerErr
		},
		pictureGet: func(_ context.Context, address string) ([]byte, error) { return body, nil },
	}
	answer = &types.ProfilePictureInfo{URL: "https://pps.whatsapp.net/v/x.jpg", ID: "pic-1"}
	got, err := c.ProfilePicture(ctx, chat, "")
	if err != nil || got.ID != "pic-1" || string(got.Data) != string(jpegBytes) || got.Unchanged || !asked.Preview {
		t.Fatalf("picture: %+v %v asked %+v", got, err, asked)
	}
	answer = nil
	if got, err = c.ProfilePicture(ctx, chat, "pic-1"); err != nil || !got.Unchanged || got.ID != "pic-1" || got.Data != nil || asked.ExistingID != "pic-1" {
		t.Fatalf("unchanged: %+v %v", got, err)
	}
	for _, none := range []error{whatsmeow.ErrProfilePictureNotSet, whatsmeow.ErrProfilePictureUnauthorized} {
		answerErr = none
		if got, err = c.ProfilePicture(ctx, chat, "pic-1"); err != nil || got.ID != "" || got.Data != nil || got.Unchanged {
			t.Fatalf("%v: %+v %v", none, got, err)
		}
	}
	answerErr = errors.New("network down")
	if _, err = c.ProfilePicture(ctx, chat, ""); err == nil {
		t.Fatal("a failed request was not an error")
	}
	answer, answerErr, body = &types.ProfilePictureInfo{URL: "https://pps.whatsapp.net/v/x.jpg", ID: "pic-2"}, nil, []byte("<html>")
	if _, err = c.ProfilePicture(ctx, chat, ""); err == nil {
		t.Fatal("a picture that is not a JPEG was accepted")
	}
	if _, err = c.ProfilePicture(ctx, "status@broadcast", ""); err == nil {
		t.Fatal("an unsupported chat was asked about")
	}
}

func TestPicturesDownloadOnlyFromWhatsAppOverHTTPS(t *testing.T) {
	for address, ok := range map[string]bool{
		"https://pps.whatsapp.net/v/t61/x.jpg": true,
		"https://whatsapp.net/x.jpg":           true,
		"http://pps.whatsapp.net/x.jpg":        false,
		"https://whatsapp.net.example.com/x":   false,
		"https://example.com/whatsapp.net":     false,
		"https://user@pps.whatsapp.net/x.jpg":  false,
	} {
		u, err := url.Parse(address)
		if err != nil || pictureHost(u) != ok {
			t.Fatalf("%s: want %v", address, ok)
		}
	}
	if _, err := downloadPicture(context.Background(), "https://example.com/x.jpg"); err == nil {
		t.Fatal("downloaded from another server")
	}
}

func TestPhoneNumberNeverRevealsAHiddenAddress(t *testing.T) {
	ctx := context.Background()
	names := newFakeNames()
	names.link(lidOne, pnOne)
	c := &Client{names: names}
	for chat, want := range map[string]string{
		pnOne.String():                 "+15550000001",
		lidOne.String():                "+15550000001",
		"90000000009@lid":              "",
		"120363000000000031@g.us":      "",
		"not a chat":                   "",
		"15550000001:3@s.whatsapp.net": "",
	} {
		if got := c.PhoneNumber(ctx, chat); got != want {
			t.Fatalf("%s: %q, want %q", chat, got, want)
		}
	}
}

func TestMediaDefaultFollowsTheOwnersSetting(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(privateTestDir(t), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account, err := s.UseAccount(ctx, "test@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	chat, manual := "120363000000000041@g.us", "120363000000000042@g.us"
	if err = s.SaveGroups(ctx, account, []storage.Group{{ID: chat, Name: "Default"}, {ID: manual, Name: "Manual"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetMediaPolicy(ctx, account, manual, "manual"); err != nil {
		t.Fatal(err)
	}
	on := true
	c := &Client{capture: &capture{ctx: ctx, store: s}}
	c.ConfigureSaveMedia(func() bool { return on })
	check := func(chat string, want bool) {
		t.Helper()
		if got, err := c.autoMedia(ctx, account, chat); err != nil || got != want {
			t.Fatalf("%s: %v %v, want %v", chat, got, err, want)
		}
	}
	check(chat, true)
	check(manual, false)
	on = false
	check(chat, false)
	// The --download-media flag still turns the default on.
	c.ConfigureMedia(c.mediaRoot, true)
	check(chat, true)
	check(manual, false)
}

func TestMediaSweepRunsOnlyWhileThereAreFiles(t *testing.T) {
	ctx := context.Background()
	dir := privateTestDir(t)
	s, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &Client{capture: &capture{ctx: ctx, store: s}, mediaRoot: filepath.Join(dir, "media"), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if !c.mediaFilesMayExist() {
		t.Fatal("a sweep was skipped before the folders were ever looked at")
	}
	c.sweepMedia(ctx)
	if c.mediaFilesMayExist() {
		t.Fatal("empty media folders are still swept")
	}
	// A download key written for a message that is not archived: the next
	// sweep must look, remove it, and only the sweep after that may rest.
	c.noteMediaWrite()
	if !c.mediaFilesMayExist() {
		t.Fatal("a written file did not wake the sweep")
	}
	root := ReferenceRoot(c.mediaRoot)
	chat := "120363000000000051@g.us"
	if _, err = mediafiles.Ensure(root, 1, chat); err != nil {
		t.Fatal(err)
	}
	path := referencePath(root, 1, chat, "M1")
	f, err := localfs.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{}")
	f.Close()
	c.sweepMedia(ctx)
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the orphaned key was kept: %v", err)
	}
	if !c.mediaFilesMayExist() {
		t.Fatal("a sweep that found files let the next one rest")
	}
	c.sweepMedia(ctx)
	if c.mediaFilesMayExist() {
		t.Fatal("the folders are empty again but still swept")
	}
}
