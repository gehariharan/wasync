package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/gehariharan/wasync/internal/storage"
)

type fakeBackend struct {
	toggled, purged string
	requested       string
	enabled         bool
	snapshot        Snapshot
}

func (f *fakeBackend) Connect(func(string)) error  { return nil }
func (f *fakeBackend) Refresh() error              { return nil }
func (f *fakeBackend) Snapshot() (Snapshot, error) { return f.snapshot, nil }
func (f *fakeBackend) Toggle(_ int64, g string, e bool) error {
	f.toggled = g
	f.enabled = e
	return nil
}
func (f *fakeBackend) Purge(_ int64, g string) error { f.purged = g; return nil }
func (f *fakeBackend) Messages(int64, string, storage.MessageQuery) (storage.MessagePage, error) {
	return storage.MessagePage{}, nil
}
func (f *fakeBackend) Logout() error                              { return nil }
func (f *fakeBackend) RequestHistory(_ int64, group string) error { f.requested = group; return nil }
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}
func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	model, cmd := m.Update(msg)
	return model.(Model), cmd
}
func fixtureModel() (Model, *fakeBackend) {
	f := &fakeBackend{snapshot: Snapshot{Account: 1, Groups: []storage.Group{{ID: "1@g.us", Name: "Alpha"}, {ID: "2@g.us", Name: "Beta"}}}}
	m := NewModel(f)
	m.snapshot = f.snapshot
	m.qrSink = func(string) {}
	return m, f
}
func TestTUIToggleFilterAndConfirmation(t *testing.T) {
	m, f := fixtureModel()
	m, _ = update(m, key("down"))
	m, cmd := update(m, key("space"))
	if cmd == nil {
		t.Fatal("toggle did not generate command")
	}
	cmd()
	if f.toggled != "2@g.us" || !f.enabled {
		t.Fatal("wrong group toggled")
	}
	m, _ = update(m, operationMsg{kind: "toggle"})
	m, _ = update(m, key("/"))
	m, _ = update(m, key("A"))
	m, _ = update(m, key("l"))
	if len(m.filtered()) != 1 {
		t.Fatal("group filter failed")
	}
	m, _ = update(m, key("enter"))
	m, _ = update(m, key("D"))
	if m.confirm != "purge" {
		t.Fatal("no purge confirmation")
	}
	m, _ = update(m, key("n"))
	if f.purged != "" {
		t.Fatal("purge happened without confirmation")
	}
	m, _ = update(m, key("D"))
	_, cmd = update(m, key("y"))
	if cmd == nil {
		t.Fatal("confirmed purge not scheduled")
	}
	cmd()
	if f.purged != "1@g.us" {
		t.Fatal("purged wrong group")
	}
}
func TestTUIRejectsStalePagesAndPreservesSelection(t *testing.T) {
	m, _ := fixtureModel()
	m.cursor = 1
	reordered := m.snapshot
	reordered.Groups = []storage.Group{m.snapshot.Groups[1], m.snapshot.Groups[0]}
	m, _ = update(m, snapshotMsg{snapshot: reordered})
	g, _ := m.selected()
	if g.ID != "2@g.us" {
		t.Fatal("selection jumped during refresh")
	}
	m.mode = "messages"
	m.groupID = "2@g.us"
	m.search = "new query"
	m.pageBusy = true
	m, _ = update(m, pageMsg{group: "2@g.us", search: "old query", page: storage.MessagePage{Items: []storage.Message{{Text: "stale"}}}})
	if len(m.page.Items) != 0 || !m.pageBusy {
		t.Fatal("stale asynchronous result replaced current query")
	}
}
func TestNarrowTUIAllowsFullTextAndSanitizesControls(t *testing.T) {
	m, _ := fixtureModel()
	m.mode = "messages"
	m.groupID = "1@g.us"
	m.groupName = "Alpha"
	m.page = storage.MessagePage{Items: []storage.Message{{Text: "real body\x1b]52;c;stolen\a\nsecond line", SenderID: "sender\x1b[2J"}}}
	m.width = 60
	m.height = 20
	m, _ = update(m, key("enter"))
	view := m.View().Content
	if !m.expanded || !strings.Contains(view, "real body") || strings.Contains(view, "stolen") || strings.Contains(view, "\x1b]52") {
		t.Fatal("full text inaccessible or terminal escape leaked")
	}
	for _, size := range [][2]int{{40, 12}, {60, 20}, {100, 30}, {160, 45}} {
		m.width = size[0]
		m.height = size[1]
		view = m.View().Content
		if len(strings.Split(view, "\n")) > size[1] {
			t.Fatal("view taller than terminal")
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > size[0] {
				t.Fatalf("view overflows width %d", size[0])
			}
		}
	}
}
func TestFilteringQDoesNotQuitAndPasteIsBounded(t *testing.T) {
	m, _ := fixtureModel()
	m, _ = update(m, key("/"))
	m, cmd := update(m, key("q"))
	if cmd != nil || m.filter != "q" {
		t.Fatal("q in filter quit application")
	}
	m, _ = update(m, tea.PasteMsg{Content: strings.Repeat("x", 10000)})
	if len([]rune(m.draft)) > 256 {
		t.Fatal("paste exceeded input limit")
	}
}

func TestMouseCheckboxSelectsAndTogglesIntendedGroup(t *testing.T) {
	m, f := fixtureModel()
	_, cmd := update(m, tea.MouseClickMsg{X: 5, Y: 9, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("checkbox click did not toggle capture")
	}
	cmd()
	if f.toggled != "2@g.us" || !f.enabled {
		t.Fatal("mouse toggled wrong group")
	}
}

func TestHistoryRetryTargetsOpenGroup(t *testing.T) {
	m, f := fixtureModel()
	m.mode = "messages"
	m.groupID = "2@g.us"
	_, cmd := update(m, key("h"))
	if cmd == nil {
		t.Fatal("no history action")
	}
	cmd()
	if f.requested != "2@g.us" {
		t.Fatal("history action targeted a different group")
	}
	if !strings.Contains(historyRequestLabel("waiting_anchor"), "known message") {
		t.Fatal("missing-anchor state is not explained")
	}
	if !strings.Contains(historyRequestLabel("no_progress"), "unknown") {
		t.Fatal("empty response claims complete history")
	}
}

func (f *fakeBackend) ToggleMany(_ int64, ids []string, enabled bool) error {
	f.requested = strings.Join(ids, ",")
	f.enabled = enabled
	return nil
}
func TestBulkSelectionRequiresConfirmationAndScopesCategory(t *testing.T) {
	m, f := fixtureModel()
	m.snapshot.Groups = append(m.snapshot.Groups, storage.Group{ID: "555@lid", Name: "Direct"})
	m, cmd := update(m, tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if cmd != nil || m.confirm != "enable-all" || len(m.confirmIDs) != 2 {
		t.Fatal("bulk mouse control did not prepare group-only confirmation")
	}
	m, cmd = update(m, key("y"))
	if cmd == nil {
		t.Fatal("confirmed bulk not scheduled")
	}
	cmd()
	if f.requested != "1@g.us,2@g.us" || !f.enabled {
		t.Fatal("bulk included a direct chat or missed groups")
	}
	m, _ = update(m, operationMsg{kind: "bulk"})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = update(m, key("N"))
	if len(m.confirmIDs) != 1 || m.confirmIDs[0] != "555@lid" {
		t.Fatal("direct category did not scope bulk change")
	}
	m, _ = update(m, key("n"))
	if m.confirm != "" {
		t.Fatal("bulk cancel failed")
	}
}
func TestPairingQRFitsWithoutArchiveChrome(t *testing.T) {
	m, _ := fixtureModel()
	m.width = 80
	m.height = 40
	// A 35-row code fits 40 terminal rows when unused archive chrome is removed.
	m.qr = strings.Repeat(strings.Repeat("█", 69)+"\n", 34) + strings.Repeat("█", 69)
	screen := m.View().Content
	if !strings.Contains(screen, "Scan this QR") || strings.Contains(screen, "QR needs") {
		t.Fatal("fitting QR was hidden")
	}
	if strings.Contains(screen, "Alpha") {
		t.Fatal("cached groups shown on pairing screen")
	}
}

func (f *fakeBackend) ApproveAction(_ int64, id string) error { f.requested = id; return nil }
func TestAgentRequestNeedsTypedConfirmation(t *testing.T) {
	m, f := fixtureModel()
	m.confirm = "agent-action"
	m.confirmID = "00000000000000000000000000123456"
	m, cmd := update(m, key("y"))
	if cmd != nil || f.requested != "" {
		t.Fatal("one keystroke approved an agent request")
	}
	m.confirmDraft = ""
	for _, r := range "DELETE 123456" {
		m, _ = update(m, key(string(r)))
	}
	_, cmd = update(m, key("enter"))
	if cmd == nil {
		t.Fatal("typed confirmation did not schedule approval")
	}
	cmd()
	if f.requested != "00000000000000000000000000123456" {
		t.Fatal("wrong approval request executed")
	}
}

func tabFixture() Model {
	m, _ := fixtureModel()
	m.snapshot.Groups = []storage.Group{
		{ID: "1@g.us", Name: "Alpha", Enabled: true},
		{ID: "2@g.us", Name: "Beta"},
		{ID: "491@s.whatsapp.net", Name: "Dana"},
		{ID: "492@s.whatsapp.net", Name: "Eli", Enabled: true},
		{ID: "493@s.whatsapp.net", Name: "Fay", Enabled: true},
	}
	return m
}

func TestChatTypeKeysSwitchTabs(t *testing.T) {
	steps := []struct {
		press string
		want  string
	}{
		{"tab", "direct"}, {"tab", "all"}, {"tab", "group"},
		{"shift+tab", "all"}, {"shift+tab", "direct"},
		{"right", "all"}, {"left", "direct"}, {"left", "group"},
		{"3", "all"}, {"2", "direct"}, {"1", "group"},
	}
	m := tabFixture()
	for _, step := range steps {
		var msg tea.KeyPressMsg
		switch step.press {
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		default:
			msg = key(step.press)
		}
		m, _ = update(m, msg)
		if m.chatType != step.want {
			t.Fatalf("%s: chat type = %q, want %q", step.press, m.chatType, step.want)
		}
	}
}

func TestSwitchingTabsResetsCursorAndKeepsMessagePagingKeys(t *testing.T) {
	m := tabFixture()
	m, _ = update(m, key("down"))
	if m.cursor != 1 {
		t.Fatal("cursor did not move")
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.cursor != 0 || len(m.filtered()) != 3 {
		t.Fatalf("direct tab: cursor %d, %d chats", m.cursor, len(m.filtered()))
	}
	// In the message view the arrows still page and must not change the tab.
	m, _ = update(m, key("enter"))
	if m.mode != "messages" {
		t.Fatal("did not open messages")
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = update(m, key("1"))
	if m.chatType != "direct" {
		t.Fatalf("message view changed chat type to %q", m.chatType)
	}
}

func TestTabStripIsAlwaysVisibleAndMarksActiveTab(t *testing.T) {
	m := tabFixture()
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "[ Groups 2 · 1 on ]") || !strings.Contains(view, "Direct 3 · 2 on") || !strings.Contains(view, "All 5 · 3 on") {
		t.Fatalf("tab strip missing or wrong:\n%s", view)
	}
	if strings.Contains(view, "[ Direct") {
		t.Fatal("inactive tab is marked active")
	}
	if !strings.Contains(view, "Tab or ← → switch list") {
		t.Fatal("key hint missing next to the tabs")
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyTab})
	view = ansi.Strip(m.View().Content)
	if !strings.Contains(view, "[ Direct 3 · 2 on ]") || strings.Contains(view, "[ Groups") {
		t.Fatalf("active tab did not follow Tab:\n%s", view)
	}
	// Filtering narrows the list but not the per-tab counts.
	m, _ = update(m, key("e"))
	view = ansi.Strip(m.View().Content)
	if !strings.Contains(view, "[ Direct 3 · 2 on ]") || !strings.Contains(view, "showing 2") {
		t.Fatalf("counts or filter note wrong:\n%s", view)
	}
}

func TestTabStripShrinksInNarrowPanes(t *testing.T) {
	m := tabFixture()
	m.width = 50
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "[ Groups 2 ]") || !strings.Contains(view, "All 5") {
		t.Fatalf("narrow tab strip missing:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 50 {
			t.Fatalf("line wider than the terminal: %q", line)
		}
	}
}

func TestClickingATabSelectsIt(t *testing.T) {
	m := tabFixture()
	// Panel border + padding put the strip's first column at x=2, on row 3.
	m, _ = update(m, tea.MouseClickMsg{X: 2 + 24, Y: 3, Button: tea.MouseLeft})
	if m.chatType != "direct" {
		t.Fatalf("click did not select Direct, got %q", m.chatType)
	}
	m, _ = update(m, tea.MouseClickMsg{X: 3, Y: 3, Button: tea.MouseLeft})
	if m.chatType != "group" {
		t.Fatalf("click did not select Groups, got %q", m.chatType)
	}
}

func TestTabStillCyclesHistoryDepthInAccessRequestDialog(t *testing.T) {
	m := tabFixture()
	m.confirm = "agent-action"
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.chatType != "group" {
		t.Fatalf("Tab in a confirmation dialog changed the chat list to %q", m.chatType)
	}
}

func TestUnnamedGroupsShowReadableLabelInListAndDetails(t *testing.T) {
	m, _ := fixtureModel()
	m.width, m.height = 140, 40
	m.snapshot.Groups = []storage.Group{{ID: "120363266948712648@g.us", Enabled: true}, {ID: "1@g.us", Name: "Alpha"}}
	view := m.View().Content
	if strings.Count(view, "Unnamed group …712648") < 2 {
		t.Fatalf("unnamed group missing from list or details:\n%s", view)
	}
	if strings.Contains(view, "[✓]  0") {
		t.Fatalf("blank row rendered:\n%s", view)
	}
	m, _ = update(m, key("enter"))
	if m.groupName != "Unnamed group …712648" {
		t.Fatalf("message view title = %q", m.groupName)
	}
}

func TestZeroArchivedWhileHistoryArrivesSaysSo(t *testing.T) {
	m, _ := fixtureModel()
	m.width, m.height = 200, 40
	m.snapshot.Groups = []storage.Group{{ID: "1@g.us", Name: "Alpha", Enabled: true, HistoryRequest: "queued"}}
	view := m.View().Content
	if !strings.Contains(view, "history still arriving for 1 chat") || !strings.Contains(view, "Older history is still arriving from your phone.") {
		t.Fatalf("history-pending explanation missing:\n%s", view)
	}
	m.snapshot.Groups[0].MessageCount = 3
	m.snapshot.Groups[0].HistoryRequest = ""
	view = m.View().Content
	if strings.Contains(view, "still arriving") || !strings.Contains(view, "3 messages archived") {
		t.Fatalf("explanation shown for a populated archive:\n%s", view)
	}
}

func TestChatsWaitingForAnchorAreNotReportedAsArriving(t *testing.T) {
	m, _ := fixtureModel()
	m.width, m.height = 200, 40
	m.snapshot.Groups = []storage.Group{
		{ID: "1@g.us", Name: "Alpha", Enabled: true, HistoryRequest: "waiting_anchor"},
		{ID: "2@g.us", Name: "Beta", Enabled: true, HistoryRequest: "waiting_anchor"},
	}
	view := m.View().Content
	if strings.Contains(view, "still arriving") {
		t.Fatalf("chats that have not asked the phone for history were reported as arriving:\n%s", view)
	}
	if !strings.Contains(view, "0 messages archived") || !strings.Contains(view, "known message") {
		t.Fatalf("details pane lost the waiting-for-a-known-message explanation:\n%s", view)
	}
}

func TestHistoryNoteCountsEmptyChatsEvenWhenSomeAreArchived(t *testing.T) {
	m, _ := fixtureModel()
	m.width, m.height = 200, 40
	m.snapshot.Groups = []storage.Group{
		{ID: "1@g.us", Name: "Alpha", Enabled: true, MessageCount: 87, HistoryRequest: "queued"},
		{ID: "2@g.us", Name: "Beta", Enabled: true, HistoryRequest: "queued"},
		{ID: "3@g.us", Name: "Gamma", Enabled: true, HistoryRequest: "waiting"},
		{ID: "4@g.us", Name: "Delta", HistoryRequest: "queued"},
		{ID: "5@g.us", Name: "Epsilon", Enabled: true},
	}
	view := m.View().Content
	if !strings.Contains(view, "87 archived") || !strings.Contains(view, "history still arriving for 2 chats") {
		t.Fatalf("status line does not explain the empty chats while others are archived:\n%s", view)
	}
}

func TestStatusLineReportsUndecryptableMessages(t *testing.T) {
	m, _ := fixtureModel()
	m.width, m.height = 200, 40
	m.snapshot.Status.Undecryptable = 2
	if view := m.View().Content; !strings.Contains(view, "2 message(s) could not be decrypted") {
		t.Fatalf("undecryptable count not shown:\n%s", view)
	}
}

func TestHeaderSaysTheScreenIsBeingRetiredWhereThereIsRoom(t *testing.T) {
	m, _ := fixtureModel()
	m.width, m.height = 120, 40
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "this screen is being retired: use `wasync web`") {
		t.Fatalf("no retirement notice on a wide screen:\n%s", view)
	}
	m.width = 80
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "being retired") || !strings.Contains(view, "personal chat archive") {
		t.Fatalf("a narrow screen should keep its header unchanged:\n%s", view)
	}
}
