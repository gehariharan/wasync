package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/output"
	"github.com/gehariharan/wasync/axi/internal/transport"
)

// Writes act on WhatsApp as the user and exist only on the running wasync
// API. whatsapp-axi adds no policy of its own: wasync decides, per grant,
// whether a credential holds the scope and may touch the chat, and says why
// when it refuses. Every write is one attempt; an unknown outcome is never
// retried.

// messageWriter is the API's message write; `wasync query` has none.
type messageWriter interface {
	Post(ctx context.Context, path string, body any, out any) error
}

const writeRetryHint = "A server acknowledgement is not proof of delivery; if the outcome is unknown, check WhatsApp and never retry automatically"

// writeNeedsOwner explains that WhatsApp writes need the running API.
func writeNeedsOwner(action string) error {
	return &contract.Error{Code: contract.CodeOwnerOffline, Message: action + " needs the running wasync API; offline reads never write",
		Help: "ask the user to start wasync (the TUI or `wasync serve`); whatsapp-axi never starts it or connects to WhatsApp", Transport: transport.NameQuery}
}

// writeTransport selects the API transport for a write, or explains why there is none.
func (a *App) writeTransport(ctx context.Context, cmd, action string) (messageWriter, transport.Transport, error) {
	t, err := a.connect(ctx, cmd)
	if err != nil {
		return nil, nil, err
	}
	w, ok := t.(messageWriter)
	if !ok {
		return nil, nil, writeNeedsOwner(action)
	}
	return w, t, nil
}

// ack runs one write call and decodes WhatsApp's acknowledgement into out.
func ack(t transport.Transport, out any, call func(*contract.Response) error) (contract.Response, error) {
	var resp contract.Response
	if err := call(&resp); err != nil {
		return resp, err
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return resp, &contract.Error{RequestID: resp.RequestID, Code: contract.CodeInvalidResponse, Message: "wasync response data has an unexpected shape: " + err.Error(), Transport: t.Name()}
	}
	return resp, nil
}

// post sends one write and decodes WhatsApp's acknowledgement into out.
func post(ctx context.Context, w messageWriter, t transport.Transport, path string, body, out any) (contract.Response, error) {
	return ack(t, out, func(r *contract.Response) error { return w.Post(ctx, path, body, r) })
}

// writeExplain points a missing credential at the scope the command needs.
func writeExplain(err error, scope string) error {
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != contract.CodeCredentialAbsent {
		return err
	}
	e := *ce
	e.Help = "ask a human to run `wasync agents grant <name> --scope read.chats,read.messages," + scope + " --on-demand`, then ask for each chat by name and they approve it" + config.SaveHelp
	return &e
}

// writeResult prints WhatsApp's acknowledgement of a write.
func (a *App) writeResult(inv invocation, t transport.Transport, ref, label string, resp contract.Response, res contract.WriteResult) error {
	if inv.json {
		return output.JSON(a.Out, result{Transport: t.Name(), Data: resp.Data})
	}
	o := output.New(a.Out)
	o.Field(0, "transport", t.Name())
	o.Object(0, label)
	o.Field(1, "target", ref)
	o.Field(1, "chat", res.ChatID)
	o.Field(1, "message_id", res.MessageID)
	o.Field(1, "status", res.Status)
	if res.Timestamp != "" {
		o.Field(1, "time", res.Timestamp)
	}
	if res.ArchiveWarning != "" {
		o.Field(1, "archive_warning", res.ArchiveWarning)
	}
	o.Help(writeRetryHint)
	return o.Err()
}

const replyUsage = "whatsapp-axi reply REF --text TEXT [--mentions JID,JID] [--json]"

var replyCommand = &command{
	name:        "reply",
	usage:       replyUsage,
	description: "Quote the message REF in a reply (running wasync, scope send and an allowlisted chat); sends once, never retries",
	flags: []flagSpec{
		{name: "--text", value: "TEXT", description: "the reply text; WhatsApp text, at most 16000 bytes"},
		{name: "--mentions", value: "JID,JID", description: "user JIDs to mention (up to 20); the text must contain @NUMBER for each so the mention is visible"},
		jsonFlag,
	},
	minArgs:  1,
	maxArgs:  1,
	argsName: "REF",
	examples: []string{`whatsapp-axi reply m1_... --text "On my way"`, `whatsapp-axi reply m1_... --text "@15551234567 can you check?" --mentions 15551234567@s.whatsapp.net`},
	run:      (*App).runReply,
}

func (a *App) runReply(ctx context.Context, inv invocation) error {
	ref, err := refArg(inv)
	if err != nil {
		return err
	}
	text, ok := inv.flag("--text")
	if !ok {
		return usageErr("usage: "+replyUsage, "`reply` needs --text")
	}
	mentions := []string{}
	if v, ok := inv.flag("--mentions"); ok {
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				mentions = append(mentions, m)
			}
		}
	}
	w, t, err := a.writeTransport(ctx, "reply", "replying")
	if err != nil {
		return err
	}
	var res contract.WriteResult
	resp, err := post(ctx, w, t, "/messages/"+url.PathEscape(ref)+"/reply", map[string]any{"text": text, "mentions": mentions}, &res)
	if err != nil {
		return writeExplain(err, "send")
	}
	return a.writeResult(inv, t, ref, "reply", resp, res)
}
