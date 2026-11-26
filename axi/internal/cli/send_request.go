package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/output"
)

// A connected agent already reads, replies and reacts in every chat synced in
// wasync. Sending to a chat that is not synced, or to a new number, needs the
// person to approve that one message: the agent asks with a send request, and
// on approval wasync itself sends exactly that text once. Asking sends nothing.

const sendRequestUsage = "wasync-axi send-request (--chat JID | --to PHONE) --text TEXT [--json]; wasync-axi send-request status REQUEST_ID [--json]"

// sendRequestHelp is added to a send wasync refused, for a chat or number the
// agent may not use yet.
const sendRequestHelp = "If the chat is not synced in wasync or the number is new, ask the person to approve this one message with `wasync-axi send-request (--chat JID | --to PHONE) --text TEXT`."

var sendRequestCommand = &command{
	name:        "send-request",
	usage:       sendRequestUsage,
	description: "Ask the person to approve one text message to a chat that is not synced in wasync, or to a new number, and check the answer (running wasync, scope send). Nothing is sent until the person approves it on their own computer; wasync then sends exactly that text once. A request expires after 30 minutes",
	flags: []flagSpec{
		{name: "--chat", value: "JID", description: "the exact chat JID to send to"},
		{name: "--to", value: "PHONE", description: "a phone number with its country code, such as +15551230000"},
		{name: "--text", value: "TEXT", description: "the message text the person approves and wasync sends; text only, at most 4096 bytes"},
		jsonFlag,
	},
	maxArgs:  2,
	argsName: "status REQUEST_ID",
	examples: []string{
		`wasync-axi send-request --to +15551230000 --text "Hi, it is Sam from the flat"`,
		`wasync-axi send-request --chat 120363000000000000@g.us --text "Rent sent today"`,
		"wasync-axi send-request status 0123456789abcdef0123456789abcdef",
	},
	run: (*App).runSendRequest,
}

// sendRequestData is the data of a send request or status response.
type sendRequestData struct {
	RequestID   string `json:"request_id"`
	Status      string `json:"status"`
	Chat        string `json:"chat"`
	NewNumber   *bool  `json:"new_number"`
	ExpiresAt   string `json:"expires_at"`
	MessageID   string `json:"message_id"`
	ApproveWith string `json:"approve_with"`
}

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type requestGetter interface {
	Get(ctx context.Context, path string, query url.Values, out any) error
}

func (a *App) runSendRequest(ctx context.Context, inv invocation) error {
	if len(inv.args) == 0 {
		return a.runSendRequestCreate(ctx, inv)
	}
	if inv.args[0] != "status" {
		return usageErr("usage: "+sendRequestUsage, "unknown send-request action %q", inv.args[0])
	}
	if len(inv.args) != 2 || !requestIDPattern.MatchString(inv.args[1]) {
		return usageErr("usage: "+sendRequestUsage, "`send-request status` needs the 32-character request_id printed by `send-request`")
	}
	if inv.has("--chat") || inv.has("--to") || inv.has("--text") {
		return usageErr("usage: "+sendRequestUsage, "`send-request status` takes only the request_id")
	}
	return a.runSendRequestStatus(ctx, inv)
}

func (a *App) runSendRequestCreate(ctx context.Context, inv invocation) error {
	chat, hasChat := inv.flag("--chat")
	to, hasTo := inv.flag("--to")
	chat, to = strings.TrimSpace(chat), strings.TrimSpace(to)
	switch {
	case hasChat && hasTo:
		return usageErr("usage: "+sendRequestUsage, "give --chat or --to, not both")
	case (!hasChat || chat == "") && (!hasTo || to == ""):
		return usageErr("usage: "+sendRequestUsage, "`send-request` needs --chat (a chat JID) or --to (a phone number)")
	}
	text, hasText := inv.flag("--text")
	if !hasText || text == "" {
		return usageErr("usage: "+sendRequestUsage, "`send-request` needs --text, the message the person approves")
	}
	body := map[string]any{"text": text}
	if hasTo {
		body["to"] = to
	} else {
		body["chat"] = chat
	}
	w, t, err := a.writeTransport(ctx, "send-request", "asking to send")
	if err != nil {
		return err
	}
	var data sendRequestData
	resp, err := post(ctx, w, t, "/send-requests", body, &data)
	if err != nil {
		return writeExplain(err, "send")
	}
	return a.sendRequestResult(inv, t.Name(), resp, data)
}

func (a *App) runSendRequestStatus(ctx context.Context, inv invocation) error {
	_, t, err := a.writeTransport(ctx, "send-request", "checking a send request")
	if err != nil {
		return err
	}
	g, ok := t.(requestGetter)
	if !ok {
		return writeNeedsOwner("checking a send request")
	}
	var data sendRequestData
	resp, err := ack(t, &data, func(r *contract.Response) error {
		return g.Get(ctx, "/send-requests/"+url.PathEscape(inv.args[1]), nil, r)
	})
	if err != nil {
		return err
	}
	if data.RequestID == "" {
		data.RequestID = inv.args[1]
	}
	return a.sendRequestResult(inv, t.Name(), resp, data)
}

// sendRequestResult prints the request state with the next step for the
// agent. The hints only say what to ask the person and what to run next; they
// never approve or send anything.
func (a *App) sendRequestResult(inv invocation, transportName string, resp contract.Response, d sendRequestData) error {
	if inv.json {
		return output.JSON(a.Out, result{Transport: transportName, Data: json.RawMessage(resp.Data)})
	}
	o := output.New(a.Out)
	o.Field(0, "transport", transportName)
	o.Object(0, "send_request")
	if d.RequestID != "" {
		o.Field(1, "request_id", d.RequestID)
	}
	o.Field(1, "status", d.Status)
	if d.Chat != "" {
		o.Field(1, "chat", d.Chat)
	}
	if d.NewNumber != nil {
		o.Value(1, "new_number", *d.NewNumber)
	}
	if d.ExpiresAt != "" {
		o.Field(1, "expires_at", d.ExpiresAt)
	}
	if d.MessageID != "" {
		o.Field(1, "message_id", d.MessageID)
	}
	switch d.Status {
	case "pending":
		approve := d.ApproveWith
		if approve == "" {
			approve = "wasync actions approve " + d.RequestID
		}
		o.Help(
			"Nothing is sent yet. Ask the person to approve this message in wasync: the Approvals page, or `"+approve+"` in a terminal",
			"Check the answer with `wasync-axi send-request status "+d.RequestID+"`; do not send or request this message again while it is pending",
			"The request expires after 30 minutes and then sends nothing")
	case "sent":
		o.Help("The person approved it and wasync sent it once (message_id " + d.MessageID + "). Do not send it again")
	case "declined":
		o.Help("The person declined; nothing was sent. Do not ask again unless the person asks you to")
	case "expired":
		o.Help("Nothing was sent. Ask again with `wasync-axi send-request` only if the message is still needed")
	case "failed":
		o.Help("The person approved it but wasync could not send it, so the outcome may be unknown. Check WhatsApp before you ask again, and never resend automatically")
	case "already_allowed":
		o.Help("You may already send to this chat, so no approval is needed: send it directly with `wasync-axi send --chat " + d.Chat + " --text TEXT`")
	}
	return o.Err()
}
