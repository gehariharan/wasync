package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/output"
	"github.com/gehariharan/wasync/axi/internal/transport"
)

// Writes act on WhatsApp as the user and exist only on the running wasync
// API. wasync-axi adds no policy of its own: wasync decides, per grant,
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
		Help: "ask the user to start wasync (the TUI or `wasync serve`); wasync-axi never starts it or connects to WhatsApp", Transport: transport.NameQuery}
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
	e.Help = "ask a human to connect this agent in wasync, or to run `wasync agents grant <name> --scope read.chats,read.messages," + scope + " --synced-chats`; the agent then uses every chat synced in wasync" + config.SaveHelp
	return &e
}

// syncHelp says how a chat becomes usable for reading, replying and reacting.
const syncHelp = "Reading, replying and reacting work in every chat synced in wasync; for a chat that is not synced, ask the person to sync it in wasync."

// refusalHint adds the next step after wasync's own help for a write it
// refused with forbidden, unless that help already names it (marker). It
// never changes the refusal or wasync's words.
func refusalHint(err error, marker, hint string) error {
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != contract.CodeForbidden || strings.Contains(ce.Help, marker) {
		return err
	}
	e := *ce
	switch own := strings.TrimSpace(e.Help); {
	case own == "":
		e.Help = hint
	case strings.HasSuffix(own, "."):
		e.Help = own + " " + hint
	default:
		e.Help = own + ". " + hint
	}
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

const replyUsage = "wasync-axi reply REF --text TEXT [--mentions JID,JID] [--json]"

var replyCommand = &command{
	name:        "reply",
	usage:       replyUsage,
	description: "Quote the message REF in a reply (running wasync, scope send, in a chat synced in wasync); sends once, never retries",
	flags: []flagSpec{
		{name: "--text", value: "TEXT", description: "the reply text; WhatsApp text, at most 16000 bytes"},
		{name: "--mentions", value: "JID,JID", description: "user JIDs to mention (up to 20); the text must contain @NUMBER for each so the mention is visible"},
		jsonFlag,
	},
	minArgs:  1,
	maxArgs:  1,
	argsName: "REF",
	examples: []string{`wasync-axi reply m1_... --text "On my way"`, `wasync-axi reply m1_... --text "@15551234567 can you check?" --mentions 15551234567@s.whatsapp.net`},
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
		return refusalHint(writeExplain(err, "send"), "sync", syncHelp)
	}
	return a.writeResult(inv, t, ref, "reply", resp, res)
}

const sendUsage = "wasync-axi send (--chat CHAT_JID | --to PHONE_NUMBER) (--text TEXT | --video FILE.mp4 [--text CAPTION]) [--json]"

var sendCommand = &command{
	name:        "send",
	usage:       sendUsage,
	description: "Send a new message or a video to a chat, or to a phone number (running wasync, scope send; a chat synced in wasync, or a number on the list the person approved for you or while open sending is on; for anything else use `send-request`); sends once, never retries",
	flags: []flagSpec{
		{name: "--chat", value: "CHAT_JID", description: "the chat to send to, as listed by `chats`; it must be synced in wasync, unless open sending is on"},
		{name: "--to", value: "PHONE_NUMBER", description: "a phone number with its country code, such as +15551230000; wasync refuses it unless it is on the list the person approved for you (paced: new numbers at random intervals, so retry at the time wasync gives; one message until it replies) or open sending is on"},
		{name: "--text", value: "TEXT", description: "the message text, or the video's caption; WhatsApp text, at most 16000 bytes"},
		{name: "--video", value: "FILE", description: "an MP4 video (H.264 video, AAC audio, up to 64 MiB) to send as a playable video; its preview is a frame taken with ffmpeg when it is installed, otherwise a plain one; the same approval and pacing as text apply"},
		jsonFlag,
	},
	examples: []string{`wasync-axi send --chat 120363000000000001@g.us --text "Rent sent today"`, `wasync-axi send --to +15551230000 --text "Hi, it is Sam from the flat"`, `wasync-axi send --to +15551230000 --video explainer.mp4 --text "A 75-second look at how it works"`},
	run:      (*App).runSend,
}

// transferWriter is the API's write for a request that carries an attachment.
type transferWriter interface {
	PostTransfer(ctx context.Context, path string, body any, out any) error
}

// runSend posts one new message. It is not a quoted reply: it needs no earlier
// message to anchor on. The chat must be synced in wasync, the number on the
// list the person approved for this agent, or open sending must be on; wasync
// refuses anything else and says why, and `send-request` asks the person to
// approve one message instead. A video goes as its bytes, with the text as
// its caption.
func (a *App) runSend(ctx context.Context, inv invocation) error {
	chat, hasChat := inv.flag("--chat")
	to, hasTo := inv.flag("--to")
	chat, to = strings.TrimSpace(chat), strings.TrimSpace(to)
	switch {
	case hasChat && hasTo:
		return usageErr("usage: "+sendUsage, "give --chat or --to, not both")
	case (!hasChat || chat == "") && (!hasTo || to == ""):
		return usageErr("usage: "+sendUsage, "`send` needs --chat (the chat JID from `chats`) or --to (a phone number)")
	}
	text, hasText := inv.flag("--text")
	videoPath, hasVideo := inv.flag("--video")
	switch {
	case !hasText && !hasVideo:
		return usageErr("usage: "+sendUsage, "`send` needs --text, or --video")
	case hasVideo && strings.TrimSpace(videoPath) == "":
		return usageErr("usage: "+sendUsage, "--video needs a file")
	}
	body, target, command := map[string]any{}, chat, "send"
	if hasText {
		body["text"] = text
	}
	if hasTo {
		body["to"], target, command = to, to, "send.to"
	} else {
		body["chat"] = chat
	}
	note := ""
	if hasVideo {
		command += ".video"
		video, err := readAttachment("--video", videoPath, maxVideoBytes)
		if err != nil {
			return err
		}
		body["video"] = video
		if thumb, err := videoFrame(ctx, videoPath); err != nil {
			note = "no preview frame (" + err.Error() + "), so the video has a plain preview"
		} else {
			body["thumbnail"] = thumb
		}
	}
	w, t, err := a.writeTransport(ctx, command, "sending")
	if err != nil {
		return err
	}
	var res contract.WriteResult
	var resp contract.Response
	if hasVideo {
		tw, ok := w.(transferWriter)
		if !ok {
			return writeNeedsOwner("sending a video")
		}
		resp, err = ack(t, &res, func(r *contract.Response) error { return tw.PostTransfer(ctx, "/messages", body, r) })
	} else {
		resp, err = post(ctx, w, t, "/messages", body, &res)
	}
	if err != nil {
		return writeExplain(err, "send")
	}
	if note != "" && !inv.json {
		fmt.Fprintln(a.Out, "note: "+note)
	}
	return a.writeResult(inv, t, target, "send", resp, res)
}
