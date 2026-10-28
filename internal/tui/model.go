package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/gehariharan/wasync/internal/accessmode"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/mdp/qrterminal/v3"
)

type backend interface {
	Connect(func(string)) error
	Refresh() error
	Snapshot() (Snapshot, error)
	Toggle(int64, string, bool) error
	Purge(int64, string) error
	Messages(int64, string, storage.MessageQuery) (storage.MessagePage, error)
	Logout() error
	RequestHistory(int64, string) error
}
type snapshotMsg struct {
	snapshot Snapshot
	err      error
}
type operationMsg struct {
	kind string
	err  error
}
type qrMsg string
type tickMsg time.Time
type pageMsg struct {
	group, search string
	before        storage.Cursor
	page          storage.MessagePage
	err           error
}

type Model struct {
	mediaPolling                     bool
	expanded                         bool
	session                          backend
	qrSink                           func(string)
	width, height                    int
	snapshot                         Snapshot
	cursor                           int
	filter                           string
	enabledOnly                      bool
	chatType                         string
	mode                             string
	editing                          string
	search                           string
	draft                            string
	page                             storage.MessagePage
	pageCursor                       int
	before                           storage.Cursor
	previous                         []storage.Cursor
	groupID, groupName               string
	detailOffset                     int
	networkBusy, writeBusy, pageBusy bool
	notice                           string
	qr                               string
	confirm, confirmID               string
	confirmIDs                       []string
	confirmDraft                     string
	confirmRequest                   *approval.Request
	confirmChoice                    int    // chat access: index of the chosen candidate
	confirmHistory                   string // chat access: approval.HistoryNow or HistoryFull
	modeStage                        int    // access mode screen: 0 choose, 1 history depth, 2 typed confirmation
	modeHistory                      string // access mode screen: history depth picked for sync all
	modePrompted                     bool   // the first-run screen was shown this session
	help                             bool
}

func NewModel(s backend) Model {
	return Model{session: s, width: 100, height: 30, mode: "groups", chatType: "group", notice: "Connecting using your saved session…"}
}
func tick() tea.Cmd { return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m Model) loadSnapshot() tea.Cmd {
	return func() tea.Msg { s, e := m.session.Snapshot(); return snapshotMsg{s, e} }
}
func (m Model) connect() tea.Cmd {
	return func() tea.Msg { return operationMsg{"connect", m.session.Connect(m.qrSink)} }
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.loadSnapshot(), m.connect()) }
func (m Model) loadPage() tea.Cmd {
	group, search, before, account := m.groupID, m.search, m.before, m.snapshot.Account
	return func() tea.Msg {
		page, err := m.session.Messages(account, group, storage.MessageQuery{Before: before, Search: search, Limit: 50})
		return pageMsg{group, search, before, page, err}
	}
}
func (m Model) filtered() []storage.Group {
	groups := make([]storage.Group, 0)
	for _, g := range m.snapshot.Groups {
		if m.chatType != "all" && storage.ChatKind(g.ID) != m.chatType {
			continue
		}
		if m.enabledOnly && !g.Enabled {
			continue
		}
		if m.filter != "" && !strings.Contains(strings.ToLower(g.Name+" "+g.ID), strings.ToLower(m.filter)) {
			continue
		}
		groups = append(groups, g)
	}
	return groups
}
func (m Model) selected() (storage.Group, bool) {
	groups := m.filtered()
	if len(groups) == 0 {
		return storage.Group{}, false
	}
	return groups[min(max(m.cursor, 0), len(groups)-1)], true
}
func (m *Model) move(delta int) {
	if m.expanded {
		m.detailOffset = max(0, m.detailOffset+delta)
		return
	}
	m.detailOffset = 0
	if m.mode == "messages" {
		m.pageCursor = min(max(m.pageCursor+delta, 0), max(len(m.page.Items)-1, 0))
	} else {
		m.cursor = min(max(m.cursor+delta, 0), max(len(m.filtered())-1, 0))
	}
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
	case qrMsg:
		var b bytes.Buffer
		qrterminal.GenerateHalfBlock(string(v), qrterminal.L, &b)
		m.qr = strings.TrimRight(b.String(), "\n")
		m.notice = "Scan with WhatsApp → Linked Devices → Link a Device."
	case snapshotMsg:
		selected, _ := m.selected()
		m.snapshot = v.snapshot
		if m.confirm == "" && !m.writeBusy && !m.networkBusy && m.editing == "" && v.snapshot.Pending != nil {
			m.confirm = "agent-action"
			m.confirmID = v.snapshot.Pending.ID
			m.confirmDraft = ""
			m.confirmRequest = v.snapshot.Pending
			m.confirmChoice, m.confirmHistory = 0, approval.HistoryNow
		}
		if m.confirm == "" && !m.writeBusy && !m.networkBusy && m.editing == "" && !m.modePrompted && v.snapshot.Account != 0 && !v.snapshot.ModeChosen && v.snapshot.Pending == nil {
			// First run: ask once how agents should get chats.
			m.modePrompted = true
			m.openAccessMode()
		}
		if v.err != nil {
			m.notice = "Archive read failed: " + v.err.Error()
		}
		groups := m.filtered()
		for i, g := range groups {
			if g.ID == selected.ID {
				m.cursor = i
				break
			}
		}
		m.cursor = min(m.cursor, max(len(groups)-1, 0))
		if m.snapshot.Status.Connection == "online" {
			m.qr = ""
		}
		var cmd tea.Cmd
		if m.mode == "messages" && (m.before.ID == 0 || m.mediaPolling) && !m.pageBusy {
			m.pageBusy = true
			cmd = m.loadPage()
		}
		return m, tea.Batch(tick(), cmd)
	case tickMsg:
		return m, m.loadSnapshot()
	case operationMsg:
		switch v.kind {
		case "connect", "refresh":
			m.networkBusy = false
		case "media-policy", "media-download", "toggle", "bulk", "purge", "history", "clear-message", "agent-action", "access-mode", "revoke-message", "delete-whatsapp-chat", "action-rejected":
			m.writeBusy = false
		}
		if v.err != nil {
			m.notice = v.err.Error()
			if v.kind == "connect" {
				m.qr = ""
			}
		} else {
			switch v.kind {
			case "connect":
				m.qr = ""
				m.notice = "Capture is active for enabled groups. History completeness is unknown."
			case "agent-action", "revoke-message", "delete-whatsapp-chat":
				m.notice = "Confirmed action finished. Inspect WhatsApp for remote changes; local chat deletion retains the archive."
			case "action-rejected":
				m.notice = "Request rejected; nothing was granted or changed."
			case "access-mode":
				m.notice = "Saved. Switching back to ask-each-time stops future automatic approvals; access already given stays until you revoke the agent."
			case "clear-message":
				m.notice = "Message content removed locally. WhatsApp was not changed."
				m.pageBusy = true
				return m, m.loadPage()
			case "media-policy":
				m.notice = "Media policy saved; applies across restarts. Auto affects future attachment deliveries."
			case "media-download":
				m.mediaPolling = true
				m.notice = "One-time media request processed. Details show the current file state; R refreshes this page."
				m.pageBusy = true
				return m, m.loadPage()
			case "bulk":
				m.notice = "Group preferences saved. Enabled groups queue available history automatically."
			case "toggle":
				m.notice = "Preference saved. Enabling queues older history automatically."
			case "history":
				m.notice = "Older history queued; the phone must respond. Use h to retry after a failure."
			case "refresh":
				m.notice = "Group metadata refreshed."
			case "purge":
				m.notice = "Group disabled and archived data purged. Backups may retain copies."
				m.mode = "groups"
				m.page = storage.MessagePage{}
			case "logout":
				return m, tea.Quit
			}
		}
		// The regular poll owns the sole snapshot loop; do not start another here.
	case pageMsg:
		if v.group != m.groupID || v.search != m.search || v.before != m.before {
			return m, nil
		}
		m.pageBusy = false
		if v.err != nil {
			m.notice = "Message query failed: " + v.err.Error()
		} else {
			selectedID := int64(0)
			if m.pageCursor < len(m.page.Items) {
				selectedID = m.page.Items[m.pageCursor].ID
			}
			m.page = v.page
			if m.mediaPolling {
				pending := false
				for _, item := range v.page.Items {
					var meta struct {
						State string `json:"download_state"`
					}
					_ = json.Unmarshal([]byte(item.Media), &meta)
					if meta.State == "queued" || meta.State == "downloading" {
						pending = true
					}
				}
				m.mediaPolling = pending
			}
			m.pageCursor = min(m.pageCursor, max(len(m.page.Items)-1, 0))
			for i, item := range m.page.Items {
				if item.ID == selectedID {
					m.pageCursor = i
					break
				}
			}
		}
	case tea.MouseClickMsg:
		if m.help || m.qr != "" || m.confirm != "" || m.editing != "" {
			return m, nil
		}
		mouse := v.Mouse()
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		listWidth := m.width
		if m.width >= 90 {
			if m.mode == "groups" {
				listWidth = m.width * 3 / 5
			} else {
				listWidth = m.width / 2
			}
		}
		if mouse.X >= listWidth || mouse.Y < 5 {
			return m, nil
		}
		firstRow := 5
		rows := max(m.height-14, 1)
		if m.mode == "groups" {
			firstRow = 8
			rows = max(m.height-17, 1)
			if mouse.Y == 5 {
				if mouse.X >= 2 && mouse.X < 16 {
					return m.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
				}
				if mouse.X >= 17 && mouse.X < 27 {
					return m.Update(tea.KeyPressMsg{Code: 'N', Text: "N"})
				}
			}
			if mouse.Y == 6 {
				return m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
			}
		}
		if mouse.Y < firstRow {
			return m, nil
		}
		cursor, count := m.cursor, len(m.filtered())
		if m.mode == "messages" {
			cursor = m.pageCursor
			count = len(m.page.Items)
		}
		start := max(cursor-rows+1, 0)
		index := start + mouse.Y - firstRow
		if index < 0 || index >= count || mouse.Y-firstRow >= rows {
			return m, nil
		}
		if m.mode == "messages" {
			m.pageCursor = index
			m.detailOffset = 0
		} else {
			m.cursor = index
			if mouse.X >= 4 && mouse.X <= 6 {
				return m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
			}
		}
	case tea.MouseWheelMsg:
		if m.help || m.qr != "" || m.confirm != "" || m.editing != "" {
			return m, nil
		}
		switch v.Mouse().Button {
		case tea.MouseWheelUp:
			m.move(-3)
		case tea.MouseWheelDown:
			m.move(3)
		}
	case tea.PasteMsg:
		if m.editing != "" {
			m.draft += clean(v.Content)
			m.draft = truncateRunes(m.draft, 256)
			if m.editing == "groups" {
				m.filter = m.draft
				m.cursor = 0
			}
		}
	case tea.KeyPressMsg:
		key := v.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.confirm == "access-mode" {
			return m.accessModeKey(v, key)
		}
		if m.confirm != "" {
			if m.isNoMatch() && key != "n" && key != "esc" {
				return m, nil // nothing to approve; it can only be rejected
			}
			if m.isAccessRequest() && (key == "up" || key == "down" || key == "tab") {
				// Choices are shown above the typed confirmation, so they can be
				// changed until the exact phrase is entered.
				if candidates := m.confirmRequest.Access.Candidates; key == "tab" {
					m.confirmHistory = map[string]string{approval.HistoryNow: approval.HistoryFull, approval.HistoryFull: approval.HistoryNow}[m.confirmHistory]
				} else if key == "down" {
					m.confirmChoice = min(m.confirmChoice+1, len(candidates)-1)
				} else {
					m.confirmChoice = max(m.confirmChoice-1, 0)
				}
				m.confirmDraft = ""
				return m, nil
			}
			if m.confirm == "agent-action" && key != "n" && key != "esc" {
				expected := "DELETE " + m.confirmID[max(0, len(m.confirmID)-6):]
				if m.isAccessRequest() {
					expected = "ALLOW " + m.confirmID[max(0, len(m.confirmID)-6):]
				}
				if key == "enter" && m.confirmDraft == expected {
					key = "y"
				} else {
					if key == "backspace" && len(m.confirmDraft) > 0 {
						m.confirmDraft = m.confirmDraft[:len(m.confirmDraft)-1]
					} else if key == "space" || key == " " {
						m.confirmDraft += " "
					} else if v.Text != "" {
						m.confirmDraft = truncateRunes(m.confirmDraft+oneLine(v.Text), 64)
					}
					return m, nil
				}
			}
			if key == "y" {
				kind, id, account := m.confirm, m.confirmID, m.snapshot.Account
				var decide func() error
				if m.isAccessRequest() {
					chat, history := m.confirmRequest.Access.Candidates[m.confirmChoice].Chat, m.confirmHistory
					decide = func() error {
						decider, ok := m.session.(interface {
							DecideAccess(int64, string, string, string) error
						})
						if !ok {
							return fmt.Errorf("access approval unavailable")
						}
						return decider.DecideAccess(account, id, chat, history)
					}
				}
				m.confirm = ""
				m.writeBusy = true
				return m, func() tea.Msg {
					if decide != nil {
						if err := decide(); err != nil {
							return operationMsg{kind, err}
						}
					}
					if kind == "enable-all" || kind == "disable-all" {
						bulk, ok := m.session.(interface {
							ToggleMany(int64, []string, bool) error
						})
						if !ok {
							return operationMsg{"bulk", fmt.Errorf("bulk selection unavailable")}
						}
						return operationMsg{"bulk", bulk.ToggleMany(account, m.confirmIDs, kind == "enable-all")}
					}
					if kind == "agent-action" {
						approver, ok := m.session.(interface{ ApproveAction(int64, string) error })
						if !ok {
							return operationMsg{kind, fmt.Errorf("approval unavailable")}
						}
						return operationMsg{kind, approver.ApproveAction(account, id)}
					}
					if kind == "revoke-message" {
						remote, ok := m.session.(interface {
							RevokeMessage(int64, string, string) error
						})
						if !ok {
							return operationMsg{kind, fmt.Errorf("revocation unavailable")}
						}
						return operationMsg{kind, remote.RevokeMessage(account, m.groupID, id)}
					}
					if kind == "delete-whatsapp-chat" {
						remote, ok := m.session.(interface{ DeleteWhatsAppChat(int64, string) error })
						if !ok {
							return operationMsg{kind, fmt.Errorf("remote deletion unavailable")}
						}
						return operationMsg{kind, remote.DeleteWhatsAppChat(account, id)}
					}
					if kind == "clear-message" {
						cleaner, ok := m.session.(interface {
							ClearMessage(int64, string, string) error
						})
						if !ok {
							return operationMsg{kind, fmt.Errorf("local message cleanup unavailable")}
						}
						return operationMsg{kind, cleaner.ClearMessage(account, m.groupID, id)}
					}
					if kind == "logout" {
						return operationMsg{kind, m.session.Logout()}
					}
					return operationMsg{"purge", m.session.Purge(account, id)}
				}
			}
			if key == "n" || key == "esc" {
				if m.confirm == "agent-action" {
					id, account := m.confirmID, m.snapshot.Account
					m.confirm = ""
					m.writeBusy = true
					return m, func() tea.Msg {
						rejector, ok := m.session.(interface{ RejectAction(int64, string) error })
						if !ok {
							return operationMsg{"action-rejected", fmt.Errorf("rejection unavailable")}
						}
						return operationMsg{"action-rejected", rejector.RejectAction(account, id)}
					}
				}
				m.confirm = ""
			}
			return m, nil
		}
		if m.editing != "" {
			switch key {
			case "esc":
				m.editing = ""
				m.draft = ""
			case "enter":
				editing := m.editing
				m.editing = ""
				if editing == "messages" {
					m.search = m.draft
					m.before = storage.Cursor{}
					m.previous = nil
					m.pageCursor = 0
					m.pageBusy = true
					return m, m.loadPage()
				}
			case "backspace":
				if len(m.draft) > 0 {
					_, n := utf8.DecodeLastRuneInString(m.draft)
					m.draft = m.draft[:len(m.draft)-n]
				}
			default:
				if v.Text != "" {
					m.draft = truncateRunes(m.draft+clean(v.Text), 256)
				}
			}
			if m.editing == "groups" {
				m.filter = m.draft
				m.cursor = 0
			}
			return m, nil
		}
		switch key {
		case "q":
			return m, tea.Quit
		case "?":
			m.help = !m.help
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "pgup":
			m.move(-max(m.height-14, 3))
		case "pgdown":
			m.move(max(m.height-14, 3))
		case "home", "g":
			m.move(-1000000)
		case "end", "G":
			m.move(1000000)
		case "ctrl+d":
			m.detailOffset += max(m.height/3, 1)
		case "ctrl+u":
			m.detailOffset = max(0, m.detailOffset-max(m.height/3, 1))
		case "esc":
			if m.expanded {
				m.expanded = false
			} else if m.mode == "messages" {
				m.mode = "groups"
				m.pageBusy = false
			} else {
				m.filter = ""
				m.enabledOnly = false
				m.cursor = 0
			}
		case "/":
			m.editing = m.mode
			if m.mode == "groups" {
				m.draft = m.filter
			} else {
				m.draft = m.search
			}
		case "tab":
			if m.mode == "groups" {
				switch m.chatType {
				case "group":
					m.chatType = "direct"
				case "direct":
					m.chatType = "all"
				default:
					m.chatType = "group"
				}
				m.cursor = 0
			}
		case "e":
			if m.mode == "groups" {
				m.enabledOnly = !m.enabledOnly
				m.cursor = 0
			}
		case "enter":
			if m.mode == "messages" {
				m.expanded = !m.expanded
				m.detailOffset = 0
			}
			if m.mode == "groups" {
				if g, ok := m.selected(); ok {
					m.groupID = g.ID
					m.groupName = g.Name
					m.mode = "messages"
					m.search = ""
					m.before = storage.Cursor{}
					m.previous = nil
					m.page = storage.MessagePage{}
					m.pageCursor = 0
					m.pageBusy = true
					return m, m.loadPage()
				}
			}
		case "space", " ":
			if m.mode == "groups" && !m.writeBusy {
				if g, ok := m.selected(); ok {
					m.writeBusy = true
					account := m.snapshot.Account
					// Optimistic visual feedback; the next snapshot restores DB truth on failure.
					for i := range m.snapshot.Groups {
						if m.snapshot.Groups[i].ID == g.ID {
							m.snapshot.Groups[i].Enabled = !g.Enabled
						}
					}
					return m, func() tea.Msg { return operationMsg{"toggle", m.session.Toggle(account, g.ID, !g.Enabled)} }
				}
			}
		case "m":
			if m.writeBusy {
				return m, nil
			}
			account := m.snapshot.Account
			if m.mode == "groups" {
				if g, ok := m.selected(); ok {
					policy, ok := m.session.(interface {
						SetMediaPolicy(int64, string, string) error
					})
					if !ok {
						m.notice = "Media preferences unavailable"
						return m, nil
					}
					next := "manual"
					if g.MediaPolicy == "manual" {
						next = "auto"
					} else if g.MediaPolicy == "auto" {
						next = "inherit"
					}
					m.writeBusy = true
					for i := range m.snapshot.Groups {
						if m.snapshot.Groups[i].ID == g.ID {
							m.snapshot.Groups[i].MediaPolicy = next
						}
					}
					return m, func() tea.Msg { return operationMsg{"media-policy", policy.SetMediaPolicy(account, g.ID, next)} }
				}
			} else if m.mode == "messages" && !m.pageBusy && len(m.page.Items) > m.pageCursor {
				downloader, ok := m.session.(interface {
					DownloadMedia(int64, string, string) error
				})
				if !ok {
					m.notice = "Media downloads unavailable"
					return m, nil
				}
				item := m.page.Items[m.pageCursor]
				m.writeBusy = true
				return m, func() tea.Msg {
					return operationMsg{"media-download", downloader.DownloadMedia(account, item.GroupID, item.MessageID)}
				}
			}
		case "h":
			if !m.writeBusy {
				group := ""
				if m.mode == "messages" {
					group = m.groupID
				} else if g, ok := m.selected(); ok {
					group = g.ID
				}
				if group != "" {
					m.writeBusy = true
					account := m.snapshot.Account
					return m, func() tea.Msg { return operationMsg{"history", m.session.RequestHistory(account, group)} }
				}
			}
		case "r":
			if !m.networkBusy {
				m.networkBusy = true
				return m, func() tea.Msg { return operationMsg{"refresh", m.session.Refresh()} }
			}
		case "c":
			if !m.networkBusy {
				m.networkBusy = true
				return m, m.connect()
			}
		case "X":
			if m.mode == "messages" && !m.writeBusy && !m.networkBusy && m.pageCursor < len(m.page.Items) {
				if !m.page.Items[m.pageCursor].FromMe {
					m.notice = "Only your own archived messages can be revoked"
				} else {
					m.confirm = "revoke-message"
					m.confirmID = m.page.Items[m.pageCursor].MessageID
				}
			}
		case "W":
			if m.mode == "groups" && !m.writeBusy && !m.networkBusy {
				if chat, ok := m.selected(); ok {
					m.confirm = "delete-whatsapp-chat"
					m.confirmID = chat.ID
				}
			}
		case "d":
			if m.mode == "messages" && !m.writeBusy && m.pageCursor < len(m.page.Items) {
				m.confirm = "clear-message"
				m.confirmID = m.page.Items[m.pageCursor].MessageID
			}
		case "D":
			if g, ok := m.selected(); ok && m.mode == "groups" && !m.writeBusy {
				m.confirm = "purge"
				m.confirmID = g.ID
			}
		case "A", "N":
			if m.mode == "groups" && !m.writeBusy && !m.networkBusy {
				m.confirmIDs = nil
				for _, group := range m.snapshot.Groups {
					if m.chatType != "all" && storage.ChatKind(group.ID) != m.chatType {
						continue
					}
					m.confirmIDs = append(m.confirmIDs, group.ID)
				}
				if len(m.confirmIDs) > 0 {
					m.confirm = "enable-all"
					if key == "N" {
						m.confirm = "disable-all"
					}
				}
			}
		case "L":
			if !m.networkBusy && !m.writeBusy {
				m.confirm = "logout"
			}
		case "M":
			if m.mode == "groups" && !m.writeBusy && !m.networkBusy && m.snapshot.Account != 0 {
				m.openAccessMode()
			}
		case "n", "right":
			if m.mode == "messages" && !m.pageBusy && m.page.Next.ID != 0 {
				m.previous = append(m.previous, m.before)
				m.before = m.page.Next
				m.pageCursor = 0
				m.detailOffset = 0
				m.pageBusy = true
				return m, m.loadPage()
			}
		case "p", "left":
			if m.mode == "messages" && !m.pageBusy && len(m.previous) > 0 {
				m.before = m.previous[len(m.previous)-1]
				m.previous = m.previous[:len(m.previous)-1]
				m.pageCursor = 0
				m.detailOffset = 0
				m.pageBusy = true
				return m, m.loadPage()
			}
		case "R":
			if m.mode == "messages" {
				m.before = storage.Cursor{}
				m.previous = nil
				m.pageCursor = 0
				m.pageBusy = true
				return m, m.loadPage()
			}
		}
	}
	return m, nil
}

var accent = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
var dim = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
var warning = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
var selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(lipgloss.Color("236"))

func clean(s string) string {
	// Strip escape sequences, then reject controls (including bidi overrides).
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, s)
}
func oneLine(s string) string { return strings.Join(strings.Fields(clean(s)), " ") }
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func clip(s string, width int) string { return ansi.Truncate(s, max(width, 0), "…") }
func panel(title, body string, width, height int) string {
	title = accent.Render(clip(title, width-4))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1).Width(width).Height(height).MaxWidth(width).MaxHeight(height).Render(title + "\n\n" + body)
}
func (m Model) View() tea.View {
	w, h := m.width, m.height
	content := ""
	if m.qr != "" {
		qrW, qrH := lipgloss.Size(m.qr)
		if qrW <= w && qrH+2 <= h {
			content = accent.Render(clip("WhatsApp → Linked Devices → Link a Device", w)) + "\n" + m.qr + "\n" + dim.Render(clip("Scan this QR · q cancel · QR refreshes automatically", w))
		} else {
			content = fmt.Sprintf("Link your device\nQR needs %d columns × %d rows.\nReduce terminal font size or enlarge the window.\nq cancels pairing.", qrW, qrH+2)
		}
	} else if w < 40 || h < 12 {
		content = "wasync\nResize terminal to at least 40 columns × 12 rows.\nq quit"
	} else {
		enabled, total := 0, int64(0)
		for _, g := range m.snapshot.Groups {
			if g.Enabled {
				enabled++
			}
			total += g.MessageCount
		}
		state := m.snapshot.Status.Connection
		if state == "" {
			state = "connecting"
		}
		header := accent.Render("  wasync") + dim.Render("  /  personal chat archive")
		status := fmt.Sprintf("  %s   ·   %d enabled   ·   %d archived   ·   +%d writes this session", state, enabled, total, m.snapshot.Status.Stored)
		if m.snapshot.APIURL != "" {
			status += "   ·   API " + m.snapshot.APIURL
		}
		top := header + "\n" + dim.Render(clip(status, w)) + "\n"
		bodyHeight := h - 9
		if m.help {
			content = top + panel("KEYBOARD", "↑/↓ or j/k  Move selection\nMouse wheel  Scroll list\nPgUp/PgDn, Home/End  Jump\nSpace  Enable/disable selected chat\nm  Cycle chat media: manual / auto / inherit\nTab  Groups / Direct / All\nA/N  Enable/disable category (confirmation)\nEnter  Browse archived messages\n/  Filter groups or search message text\ne  Show enabled groups only\nr  Refresh group metadata\nh  Retry older history for this group\nc  Connect/pair again\nd  Clear selected message locally (confirmation)\nX  Revoke your message in WhatsApp (confirmation)\nW  Delete WhatsApp chat state (confirmation)\nD  Purge local chat archive (confirmation)\nM  How agents get chats: ask each time or sync all\nL  Unlink device and quit (confirmation)\n\nMessages: m download selected attachment once · n/p older/newer · R latest\nCtrl+D / Ctrl+U scroll selected message body\nEsc  Back/clear filter · q quit (keeps session linked)\n?  Close help", w, bodyHeight)
		} else if m.confirm != "" {
			description := m.confirmDescription()
			title := "CONFIRM DESTRUCTIVE ACTION"
			if m.isAccessRequest() || m.isNoMatch() {
				title = "AGENT ASKS FOR CHAT ACCESS"
			}
			if m.confirm == "access-mode" {
				title = "HOW SHOULD AGENTS GET CHATS?"
			}
			content = top + panel(title, ansi.Hardwrap(description, w-6, false), w, bodyHeight)
		} else if m.mode == "messages" {
			content = top + m.messageView(w, bodyHeight)
		} else {
			content = top + m.groupsView(w, bodyHeight)
		}
		prompt := m.notice
		if m.confirm == "agent-action" {
			prompt = "Type the exact confirmation shown above · n / Esc reject"
			if m.isAccessRequest() {
				prompt = "↑/↓ chat · Tab history depth · type the exact confirmation · n / Esc reject (nothing is granted)"
			}
			if m.isNoMatch() {
				prompt = "n / Esc reject · nothing can be granted for this request"
			}
		}
		if m.confirm == "access-mode" {
			prompt = m.accessModePrompt()
		}
		if m.snapshot.Status.Error != "" {
			prompt = m.snapshot.Status.Error
		}
		if m.confirm == "revoke-message" || m.confirm == "delete-whatsapp-chat" {
			prompt = "y approve this exact action · n / Esc reject"
		}
		if m.confirm == "clear-message" {
			prompt = "Delete this message content and download from wasync only? y confirm · n cancel"
		}
		if m.confirm == "purge" {
			prompt = "Disable capture and delete this chat's local archive and downloads? y confirm · n cancel"
		}
		if m.confirm == "logout" {
			prompt = "Unlink this WhatsApp device and quit? y confirm · n cancel"
		}
		if m.confirm == "enable-all" || m.confirm == "disable-all" {
			action := "Enable capture and queue available history for"
			if m.confirm == "disable-all" {
				action = "Disable capture for"
			}
			prompt = fmt.Sprintf("%s all %d chats in this category? y confirm · n cancel", action, len(m.confirmIDs))
		}
		if m.editing != "" {
			prompt = "/ " + clean(m.draft) + "▏   Enter apply · Esc done"
		}
		hint := "m media policy · Space toggle · A/N all/none · L logout · Enter messages · ? help · q quit"
		if m.mode == "messages" {
			hint = "q quit · L logout · Enter full text · d clear locally · / search · n/p page · Esc back"
		}
		content += "\n" + warning.Render(clip("  "+oneLine(prompt), w)) + "\n" + dim.Render(clip("  "+hint, w)) + "\n" + dim.Render(clip("  History is best-effort. Reconnecting does not prove missed messages were recovered.", w))
	}
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = clip(lines[i], w)
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "wasync — local chat archive"
	return v
}
func (m Model) groupsView(width, height int) string {
	groups := m.filtered()
	listWidth := width
	if width >= 90 {
		listWidth = width * 3 / 5
	}
	rows := max(height-8, 1)
	start := 0
	if m.cursor >= rows {
		start = m.cursor - rows + 1
	}
	lines := make([]string, 0, rows)
	for i := start; i < min(start+rows, len(groups)); i++ {
		g := groups[i]
		mark := "[ ]"
		if g.Enabled {
			mark = "[✓]"
		}
		cursor := "  "
		if i == m.cursor {
			cursor = "› "
		}
		line := clip(fmt.Sprintf("%s%s  %s", cursor, mark, oneLine(g.Name)), listWidth-16) + fmt.Sprintf("  %d", g.MessageCount)
		if i == m.cursor {
			line = selectedStyle.Render(clip(line, listWidth-4))
		}
		lines = append(lines, line)
	}
	if len(groups) == 0 {
		lines = append(lines, "No matching chats.", "Direct chats appear from history/live events.")
	}
	title := fmt.Sprintf("%s CHATS  %d", strings.ToUpper(m.chatType), len(groups))
	if m.filter != "" {
		title += "  / " + oneLine(m.filter)
	}
	toolbar := accent.Render("[A Select all]  [N None]") + "\n" + dim.Render("Tab: Groups / Direct / All · "+m.chatType) + "\n\n"
	left := panel(title, toolbar+strings.Join(lines, "\n"), listWidth, height)
	if width < 90 {
		return left
	}
	details := "Choose a group to inspect capture state.\n\nSpace enables only that group."
	if g, ok := m.selected(); ok {
		enabled := "Disabled"
		if g.Enabled {
			enabled = "Enabled — capturing when online"
		}
		last := "No messages archived yet"
		if g.LastMessage != nil {
			last = g.LastMessage.Local().Format("02 Jan 2006 · 15:04:05")
		}
		history := g.HistoryState
		if history == "not_observed" {
			history = "No history delivery observed"
		} else {
			history = "History received (partial)"
		}
		if g.HistoryRequest != "" {
			history = historyRequestLabel(g.HistoryRequest)
			if g.HistoryRequestError != "" {
				history += "\n" + oneLine(g.HistoryRequestError)
			}
		}
		details = accent.Render(clip(oneLine(g.Name), width-listWidth-5)) + "\n\n" + oneLine(g.ID) + "\n\n" + enabled + "\nMedia: " + oneLine(g.MediaPolicy) + " (m to change)" + fmt.Sprintf("\n%d messages archived\n\nLast message\n%s\n\n%s\n%d potential account gaps\n\nEnter to browse messages.\nm to cycle saved media policy.\nh to retry older history.\nD to purge this archive.", g.MessageCount, last, history, g.GapCount)
	}
	details = ansi.Hardwrap(details, width-listWidth-5, false)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, panel("CAPTURE DETAILS", details, width-listWidth, height))
}
func (m Model) messageView(width, height int) string {
	if m.expanded {
		return panel("MESSAGE · ↑↓ scroll · Esc back", m.detailText(width), width, height)
	}
	listWidth := width
	if width >= 90 {
		listWidth = width / 2
	}
	rows := max(height-5, 1)
	start := 0
	if m.pageCursor >= rows {
		start = m.pageCursor - rows + 1
	}
	lines := []string{}
	for i := start; i < min(start+rows, len(m.page.Items)); i++ {
		item := m.page.Items[i]
		text := oneLine(item.Text)
		if item.DeletedAt != nil {
			text = "[deleted]"
		} else if text == "" {
			text = "[" + item.Type + "]"
		}
		line := item.Timestamp.Local().Format("Jan02 15:04") + "  " + text
		if i == m.pageCursor {
			line = selectedStyle.Render(clip("› "+line, listWidth-4))
		} else {
			line = clip("  "+line, listWidth-4)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		if m.pageBusy {
			lines = append(lines, "Loading archive…")
		} else {
			lines = append(lines, "No archived messages match.", "New messages arrive only while capture runs.")
		}
	}
	title := oneLine(m.groupName)
	if m.search != "" {
		title += " / " + oneLine(m.search)
	}
	title = fmt.Sprintf("MESSAGES · page %d · %s", len(m.previous)+1, title)
	left := panel(title, strings.Join(lines, "\n"), listWidth, height)
	if width < 90 {
		return left
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, panel("MESSAGE · Enter full text", m.detailText(width-listWidth), width-listWidth, height))
}
func (m Model) detailText(width int) string {
	detail := "Select a message."
	if len(m.page.Items) > 0 && m.pageCursor < len(m.page.Items) {
		item := m.page.Items[m.pageCursor]
		sender := oneLine(item.SenderID)
		if item.FromMe {
			sender = "You · " + sender
		}
		flags := item.Type + " · " + item.Source
		if item.EditedAt != nil {
			flags += " · edited"
		}
		if item.ExpiresAt != nil {
			flags += " · expires " + item.ExpiresAt.Local().Format("Jan02 15:04")
		}
		body := clean(item.Text)
		if item.DeletedAt != nil {
			body = "[Message deleted]"
		} else if body == "" {
			body = "[" + item.Type + "]"
		}
		wrapped := strings.Split(ansi.Hardwrap(body, width-4, false), "\n")
		offset := min(m.detailOffset, max(len(wrapped)-1, 0))
		body = strings.Join(wrapped[offset:], "\n")
		detail = oneLine(item.Timestamp.Local().Format("02 Jan 2006 · 15:04:05")) + "\n" + sender + "\n" + oneLine(flags) + "\n" + oneLine(item.MessageID) + "\n\n" + body
		if item.ReplyTo != "" {
			detail += "\n\nReply to " + oneLine(item.ReplyTo)
		}
		if item.Media != "" && item.DeletedAt == nil {
			detail += "\n\n" + mediaDetails(item.Type, item.Media)
		}
	}
	return ansi.Hardwrap(detail, width-4, false)
}
func Run(ctx context.Context, s backend, in io.Reader, out io.Writer) error {
	model := NewModel(s)
	var program *tea.Program
	model.networkBusy = true
	model.qrSink = func(code string) { program.Send(qrMsg(code)) }
	program = tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out))
	_, err := program.Run()
	if ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return ctx.Err()
	}
	return err
}

func historyRequestLabel(state string) string {
	switch state {
	case "queued":
		return "Older history queued"
	case "waiting_anchor":
		return "Waiting for a known message\nHistory starts automatically when one arrives."
	case "sending":
		return "Requesting 50 older messages from phone"
	case "waiting":
		return "Waiting for history from your phone"
	case "no_progress":
		return "No older page returned (coverage unknown)"
	case "failed":
		return "History request failed — h retries"
	case "timed_out":
		return "History response timed out — h retries"
	case "disabled":
		return "History paused (capture disabled)"
	default:
		return "History state: " + state
	}
}

// isAccessRequest reports whether the open confirmation is an agent asking for
// access to a chat rather than a destructive action.
func (m Model) isAccessRequest() bool {
	return m.confirm == "agent-action" && m.confirmRequest != nil && m.confirmRequest.Kind == approval.KindChatAccess && m.confirmRequest.Access != nil && len(m.confirmRequest.Access.Candidates) > 0
}

// accessDescription spells out who gets what before the typed confirmation:
// the agent, the chat, the access level and the history depth.
func (m Model) accessDescription() string {
	a := m.confirmRequest.Access
	choice := min(max(m.confirmChoice, 0), len(a.Candidates)-1)
	access, effect := "READ the messages of", "read access to"
	switch {
	case a.Send && a.Edit:
		access, effect = "READ, SEND/REPLY/REACT and EDIT its own messages in", "read, send, reply, react and edit-own access to"
	case a.Send:
		access, effect = "READ and SEND/REPLY/REACT in", "read, send, reply and react access to"
	case a.Edit:
		access, effect = "READ and EDIT its own messages in", "read and edit-own access to"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Agent %s asks to %s one chat.\nIt asked for: %s\n\nChats it may mean (↑/↓ to choose):\n", oneLine(a.Agent), access, oneLine(a.Query))
	for i, c := range a.Candidates {
		marker := "  "
		if i == choice {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%s  %s\n", marker, oneLine(c.Name), oneLine(c.Chat))
	}
	if a.Truncated {
		b.WriteString("  (more chats matched; the agent can ask again with a more specific name)\n")
	}
	history := "FROM NOW ON ONLY: capture new messages; no older messages are requested"
	if m.confirmHistory == approval.HistoryFull {
		history = "FULL HISTORY SYNC: also ask your phone for this chat's older messages (best effort)"
	}
	chosen := a.Candidates[choice]
	fmt.Fprintf(&b, "\nHistory (Tab to change): %s\n\nApproving will: turn on capture for %s, then give %s %s only this chat. Nothing else changes.\n", history, oneLine(chosen.Chat), oneLine(a.Agent), effect)
	fmt.Fprintf(&b, "\nType ALLOW %s then Enter to approve.\n> %s\nn / Esc reject", m.confirmID[max(0, len(m.confirmID)-6):], oneLine(m.confirmDraft))
	return b.String()
}

// isNoMatch reports an open access request that matched no chat: the owner can
// only reject it, so the agent learns nothing about what exists.
func (m Model) isNoMatch() bool {
	return m.confirm == "agent-action" && m.confirmRequest != nil && m.confirmRequest.NoMatch()
}

func (m *Model) openAccessMode() {
	m.confirm, m.modeStage, m.modeHistory, m.confirmDraft = "access-mode", 0, "", ""
}

func (m Model) accessModePrompt() string {
	switch m.modeStage {
	case 0:
		return "1 or Enter: ask me each time · 2: sync all chats · Esc: decide later"
	case 1:
		return "1: from now on only · 2: full history sync · Esc: back"
	}
	return "Type the exact phrase, then Enter · Esc: back"
}

// accessModeKey drives the three steps of the access mode screen: pick, pick
// a history depth for sync all, then type SYNC ALL. Esc never changes anything.
func (m Model) accessModeKey(v tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	apply := func(mode accessmode.Mode) (tea.Model, tea.Cmd) {
		account := m.snapshot.Account
		m.confirm, m.writeBusy = "", true
		return m, func() tea.Msg {
			setter, ok := m.session.(interface {
				SetAccessMode(int64, accessmode.Mode) error
			})
			if !ok {
				return operationMsg{"access-mode", fmt.Errorf("access mode unavailable")}
			}
			return operationMsg{"access-mode", setter.SetAccessMode(account, mode)}
		}
	}
	switch m.modeStage {
	case 0:
		switch key {
		case "1", "enter":
			return apply(accessmode.Mode{Mode: accessmode.Ask})
		case "2":
			m.modeStage = 1
		case "esc":
			m.confirm = ""
		}
	case 1:
		switch key {
		case "1":
			m.modeHistory, m.modeStage, m.confirmDraft = accessmode.HistoryNow, 2, ""
		case "2":
			m.modeHistory, m.modeStage, m.confirmDraft = accessmode.HistoryFull, 2, ""
		case "esc":
			m.modeStage = 0
		}
	default:
		switch {
		case key == "esc":
			m.modeStage, m.confirmDraft = 0, ""
		case key == "enter" && m.confirmDraft == "SYNC ALL":
			return apply(accessmode.Mode{Mode: accessmode.All, History: m.modeHistory})
		case key == "backspace" && len(m.confirmDraft) > 0:
			m.confirmDraft = m.confirmDraft[:len(m.confirmDraft)-1]
		case key == "space" || key == " ":
			m.confirmDraft += " "
		case v.Text != "":
			m.confirmDraft = truncateRunes(m.confirmDraft+oneLine(v.Text), 32)
		}
	}
	return m, nil
}

func (m Model) accessModeDescription() string {
	current := "Not chosen yet."
	if m.snapshot.ModeChosen {
		current = "Now: ask me each time."
		if m.snapshot.Mode.Mode == accessmode.All {
			current = "Now: sync all chats (history: " + map[string]string{accessmode.HistoryNow: "from now on only", accessmode.HistoryFull: "full history sync"}[m.snapshot.Mode.History] + ")."
		}
	}
	switch m.modeStage {
	case 1:
		return "Sync all chats: capture every chat, and approve agents' requests without asking.\n\nHow much history should every chat get?\n\n  1) From now on only: new messages; nothing older is requested\n  2) Full history sync: also ask your phone for every chat's older messages (best effort, can be large)"
	case 2:
		depth := map[string]string{accessmode.HistoryNow: "from now on only", accessmode.HistoryFull: "full history sync"}[m.modeHistory]
		return "SYNC ALL CHATS (history: " + depth + ")\n\nThis turns on capture for every chat now, and from now on any agent that asks for a chat is approved at once, without asking you: it can find any chat by name or number (a match is granted at once, so a name probe both reveals and grants that chat) and READ, SEND, REPLY and REACT in any chat. Editing its own messages and deleting always stay manual. Every approval is still recorded and audited.\n\nType SYNC ALL then Enter.\n> " + oneLine(m.confirmDraft) + "\nEsc back"
	}
	return current + "\n\n  1) Ask me each time (default)\n     An agent that wants a chat asks; you approve it here or with wasync actions approve, choosing the chat, read or send, and how much history. Nothing is granted until you confirm.\n\n  2) Sync all chats\n     Captures every chat, and agents that ask get read, send, reply and react access to a chat right away, WITHOUT asking you: in this mode agents can discover any chat by name or number (a name probe both reveals and grants it) and send, reply and react in any chat. Editing their own messages and deleting stay manual.\n\nYou can change this any time: press M here, or run wasync access mode. Switching back to ask-each-time stops future automatic approvals; access already given stays until you revoke the agent."
}

func (m Model) confirmDescription() string {
	if m.confirm == "access-mode" {
		return m.accessModeDescription()
	}
	if m.isNoMatch() {
		a := m.confirmRequest.Access
		return "Agent " + oneLine(a.Agent) + " asked for access to a chat matching \"" + oneLine(a.Query) + "\".\n\nNo chat matched, so there is nothing to grant. It can only be rejected; otherwise it expires on its own.\n\nn / Esc reject"
	}
	if m.isAccessRequest() {
		return m.accessDescription()
	}
	kind, chat, message := m.confirm, m.groupID, m.confirmID
	if kind == "agent-action" && m.confirmRequest != nil {
		request := m.confirmRequest
		kind, chat, message = request.Kind, request.Chat, request.Message
	}
	if kind == "purge" || kind == "delete-whatsapp-chat" {
		chat = m.confirmID
	}
	scope := "Local wasync archive only. WhatsApp will not change."
	switch kind {
	case "revoke-message":
		scope = "WHATSAPP: delete your message for everyone, subject to WhatsApp permissions/time limits. Local content will also be cleared after acknowledgement."
	case "delete-whatsapp-chat":
		scope = "WHATSAPP: delete chat state for your account through the last locally known message. This does not delete other participants' copies or leave a group. Your local archive is retained."
	case "logout":
		scope = "Unlink this device in WhatsApp and quit. Local archives remain."
	case "enable-all", "disable-all":
		scope = "Change capture for the category shown. Existing archived data remains."
	}
	if m.confirm == "agent-action" {
		return oneLine(kind) + "\n\n" + scope + "\n\nChat: " + oneLine(chat) + "\nMessage: " + oneLine(message) + "\n\nType DELETE " + m.confirmID[max(0, len(m.confirmID)-6):] + " then Enter to approve.\n> " + oneLine(m.confirmDraft) + "\nn / Esc reject"
	}
	return oneLine(kind) + "\n\n" + scope + "\n\nChat: " + oneLine(chat) + "\nMessage: " + oneLine(message) + "\n\nThis action requires your confirmation.\ny approve · n reject"
}
