package cli

import (
	"context"
	"net/url"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

const reactUsage = "whatsapp-axi react REF (EMOJI | --remove) [--json]"

var reactCommand = &command{
	name:        "react",
	usage:       reactUsage,
	description: "React to the message REF with an emoji, or clear your reaction (running wasync, scope react and an allowlisted chat); sends once, never retries",
	flags: []flagSpec{
		{name: "--remove", description: "clear your reaction instead of setting one"},
		jsonFlag,
	},
	minArgs:  1,
	maxArgs:  2,
	argsName: "REF",
	examples: []string{"whatsapp-axi react m1_... 👍", "whatsapp-axi react m1_... --remove"},
	run:      (*App).runReact,
}

// reactionPutter and reactionDeleter are the API's reaction writes; `wasync query` has none.
type reactionPutter interface {
	Put(ctx context.Context, path string, body any, out any) error
}
type reactionDeleter interface {
	Delete(ctx context.Context, path string, out any) error
}

func (a *App) runReact(ctx context.Context, inv invocation) error {
	ref, err := refArg(inv)
	if err != nil {
		return err
	}
	remove := inv.has("--remove")
	if remove == (len(inv.args) == 2) {
		return usageErr("usage: "+reactUsage, "`react` needs exactly one of EMOJI or --remove")
	}
	_, t, err := a.writeTransport(ctx, "react", "reacting")
	if err != nil {
		return err
	}
	var res contract.WriteResult
	var resp contract.Response
	path := "/messages/" + url.PathEscape(ref) + "/reaction"
	if remove {
		d, ok := t.(reactionDeleter)
		if !ok {
			return writeNeedsOwner("reacting")
		}
		resp, err = ack(t, &res, func(r *contract.Response) error { return d.Delete(ctx, path, r) })
	} else {
		p, ok := t.(reactionPutter)
		if !ok {
			return writeNeedsOwner("reacting")
		}
		resp, err = ack(t, &res, func(r *contract.Response) error { return p.Put(ctx, path, map[string]string{"emoji": inv.args[1]}, r) })
	}
	if err != nil {
		return writeExplain(err, "react")
	}
	return a.writeResult(inv, t, ref, "reaction", resp, res)
}
