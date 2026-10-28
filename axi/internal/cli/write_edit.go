package cli

import (
	"context"
	"net/url"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

const editUsage = "whatsapp-axi edit REF --text TEXT [--json]"

var editCommand = &command{
	name:        "edit",
	usage:       editUsage,
	description: "Replace the text of one of your own recent messages REF (running wasync, scope edit.own and an allowlisted chat); wasync refuses other people's messages and edits past WhatsApp's window",
	flags: []flagSpec{
		{name: "--text", value: "TEXT", description: "the new text; at most 16000 bytes"},
		jsonFlag,
	},
	minArgs:  1,
	maxArgs:  1,
	argsName: "REF",
	examples: []string{`whatsapp-axi edit m1_... --text "Meet at 7, not 6"`},
	run:      (*App).runEdit,
}

func (a *App) runEdit(ctx context.Context, inv invocation) error {
	ref, err := refArg(inv)
	if err != nil {
		return err
	}
	text, ok := inv.flag("--text")
	if !ok {
		return usageErr("usage: "+editUsage, "`edit` needs --text")
	}
	w, t, err := a.writeTransport(ctx, "edit", "editing")
	if err != nil {
		return err
	}
	var res contract.WriteResult
	resp, err := post(ctx, w, t, "/messages/"+url.PathEscape(ref)+"/edit", map[string]string{"text": text}, &res)
	if err != nil {
		return writeExplain(err, "edit.own")
	}
	return a.writeResult(inv, t, ref, "edit", resp, res)
}
