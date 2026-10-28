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

const accessUsage = "whatsapp-axi access request (--chat JID | --match TEXT) [--send] [--edit] [--json]; whatsapp-axi access status REQUEST_ID [--json]"

var accessCommand = &command{
	name:        "access",
	usage:       accessUsage,
	description: "Ask the person for access to a chat this credential may not use yet, and check the answer (running wasync only). Asking grants nothing unless the person set wasync to sync all chats: otherwise they pick the chat, the access and the history depth in wasync and confirm, and the request expires after five minutes. wasync never says whether a name matched anything",
	flags: []flagSpec{
		{name: "--chat", value: "JID", description: "request: the exact chat JID"},
		{name: "--match", value: "TEXT", description: "request: a phone number or contact/group name; wasync resolves it to candidate chats that only the person sees"},
		{name: "--send", description: "request: also ask to send, reply and react, not only read; needs a send or react scope on the credential"},
		{name: "--edit", description: "request: also ask to edit your own messages; needs the edit.own scope and is always approved by hand"},
		jsonFlag,
	},
	minArgs:  1,
	maxArgs:  2,
	argsName: "request | status REQUEST_ID",
	examples: []string{
		"whatsapp-axi access request --chat 120363000000000000@g.us",
		"whatsapp-axi access request --match +15551234567 --send",
		"whatsapp-axi access status 0123456789abcdef0123456789abcdef",
	},
	run: (*App).runAccess,
}

// accessData is the data of an access request or status response.
type accessData struct {
	RequestID   string   `json:"request_id"`
	Status      string   `json:"status"`
	Access      []string `json:"access"`
	ExpiresAt   string   `json:"expires_at"`
	Chat        string   `json:"chat"`
	History     string   `json:"history"`
	ApproveWith string   `json:"approve_with"`
}

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type accessGetter interface {
	Get(ctx context.Context, path string, query url.Values, out any) error
}

func (a *App) runAccess(ctx context.Context, inv invocation) error {
	switch inv.args[0] {
	case "request":
		if len(inv.args) != 1 {
			return usageErr("usage: "+accessUsage, "`access request` takes flags only")
		}
		return a.runAccessRequest(ctx, inv)
	case "status":
		if len(inv.args) != 2 || !requestIDPattern.MatchString(inv.args[1]) {
			return usageErr("usage: "+accessUsage, "`access status` needs the 32-character request_id printed by `access request`")
		}
		return a.runAccessStatus(ctx, inv)
	}
	return usageErr("usage: "+accessUsage, "unknown access action %q", inv.args[0])
}

func (a *App) runAccessRequest(ctx context.Context, inv invocation) error {
	chat, hasChat := inv.flag("--chat")
	match, hasMatch := inv.flag("--match")
	if hasChat == hasMatch || strings.TrimSpace(chat+match) == "" {
		return usageErr("usage: "+accessUsage, "`access request` needs exactly one of --chat or --match")
	}
	body := map[string]any{"send": inv.has("--send"), "edit": inv.has("--edit")}
	if hasChat {
		body["chat"] = chat
	} else {
		body["match"] = match
	}
	w, t, err := a.writeTransport(ctx, "access", "requesting access")
	if err != nil {
		return err
	}
	var data accessData
	resp, err := post(ctx, w, t, "/access-requests", body, &data)
	if err != nil {
		return writeExplain(err, "read.chats,read.messages")
	}
	return a.accessResult(inv, t.Name(), resp, data)
}

func (a *App) runAccessStatus(ctx context.Context, inv invocation) error {
	_, t, err := a.writeTransport(ctx, "access", "checking an access request")
	if err != nil {
		return err
	}
	g, ok := t.(accessGetter)
	if !ok {
		return writeNeedsOwner("checking an access request")
	}
	var data accessData
	resp, err := ack(t, &data, func(r *contract.Response) error {
		return g.Get(ctx, "/access-requests/"+url.PathEscape(inv.args[1]), nil, r)
	})
	if err != nil {
		return err
	}
	return a.accessResult(inv, t.Name(), resp, data)
}

// accessResult prints the request state with the next step for the agent. The
// hints only say what to ask the person and what to run next; they never grant.
func (a *App) accessResult(inv invocation, transportName string, resp contract.Response, d accessData) error {
	if inv.json {
		return output.JSON(a.Out, result{Transport: transportName, Data: json.RawMessage(resp.Data)})
	}
	o := output.New(a.Out)
	o.Field(0, "transport", transportName)
	o.Object(0, "access_request")
	if d.RequestID != "" {
		o.Field(1, "request_id", d.RequestID)
	}
	o.Field(1, "status", d.Status)
	o.Field(1, "access", strings.Join(d.Access, "+"))
	if d.Chat != "" {
		o.Field(1, "chat", d.Chat)
	}
	if d.History != "" {
		o.Field(1, "history", map[string]string{"now": "from now on only", "full": "full history sync requested"}[d.History])
	}
	if d.ExpiresAt != "" {
		o.Field(1, "expires_at", d.ExpiresAt)
	}
	switch d.Status {
	case "pending":
		o.Help(
			"Nothing is granted yet. Ask the person to open the wasync terminal UI (a dialog appears) or to run `"+d.ApproveWith+"` in a terminal",
			"Check the answer with `whatsapp-axi access status "+d.RequestID+"`; do not retry the read until it says approved",
			"The request expires after five minutes; ask again if it does. wasync does not say whether the name matched anything, so if it expires try the phone number or exact chat JID")
	case "approved":
		o.Help("Access was granted for this chat only; retry your read, e.g. `whatsapp-axi messages " + d.Chat + "`")
	case "already_allowed":
		o.Help("This credential can already use this chat; no approval is needed")
	case "rejected":
		o.Help("The person declined. Do not ask again unless they ask you to")
	case "expired", "failed":
		o.Help("No access was granted. If it is still needed, run `whatsapp-axi access request` again and ask the person to answer promptly")
	}
	return o.Err()
}
