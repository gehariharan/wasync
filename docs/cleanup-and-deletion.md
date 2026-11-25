# Cleanup and deletion

wasync separates two very different things: removing content from **your local archive**, and
asking **WhatsApp** to delete something. Neither happens without a person's explicit
confirmation, and agents can only ask.

## The four operations

| Operation | What it does | Terminal UI key | Command |
| --- | --- | --- | --- |
| Clear one message locally | Clears that message's local archived content and download, retaining a tombstone so history replay cannot restore its body | **d**, while browsing messages | `wasync clean --chat CHAT_ID --message-id MESSAGE_ID` |
| Purge a chat locally | Disables capture and purges that chat's local archive and downloaded files. Re-enabling later can import history again | **D**, in the chat list | `wasync clean --chat CHAT_ID --all` |
| Delete your message for everyone | Requests WhatsApp deletion of your own message, subject to WhatsApp permissions/time limits. Local content is cleared after WhatsApp acknowledges the revocation. Other participants' messages cannot be revoked this way | **X**, on your own archived message | `wasync delete --chat CHAT_ID --scope everyone --message-id MY_MESSAGE_ID` |
| Delete the chat in your WhatsApp | Deletes WhatsApp chat state for your account through the last locally known message. This does not leave a group or delete participants' copies. The local archive is retained | **W**, in the chat list | `wasync delete --chat CHAT_ID --scope my-whatsapp-chat` |

Each operation shows an explicit scope and target confirmation. Local cleanup
never sends a WhatsApp deletion operation. Backups and filesystem remnants may
retain copies; cleanup is not a secure erase guarantee. Purging removes content and
disables capture; discovery rows (chat ID/name) remain for chat selection and may be
refreshed by later WhatsApp events. Media policies survive a purge (see [Media](media.md)).

## Commands only request; a person approves

`wasync clean` and `wasync delete` **only request** destructive operations:

```sh
wasync clean --chat '123456789@g.us' --message-id MESSAGE_ID
wasync clean --chat '123456789@g.us' --all
wasync delete --chat '123456789@g.us' --scope everyone --message-id MY_MESSAGE_ID
wasync delete --chat '123456789@g.us' --scope my-whatsapp-chat
wasync actions status REQUEST_ID
```

They need a running wasync (`wasync serve`, the setup page or the terminal UI) with its command
channel enabled. Requests return JSON `pending_human_confirmation`; submitting a request changes
no archived or WhatsApp messages. A person then approves it in one of three places:

- the web app's **Approvals** page (`wasync approvals`), which asks your computer to confirm
  with Touch ID, your password or Windows Hello;
- the [terminal UI](tui.md), which displays the exact action and requires typing the
  `DELETE CODE` shown in the dialog, then Enter (**n** rejects);
- an interactive terminal: `wasync actions approve REQUEST_ID` shows the exact scope and target
  and asks you to type the displayed confirmation.

There is no `--yes`. Requests wait 30 minutes and are kept in a private
file (`pending-approvals.json`), so restarting wasync does not lose one that is still waiting; a
request that was already being carried out is never restored. Approval cannot be replayed.
Inspect request status before retrying; `failed_or_unknown` can include an ambiguous remote
outcome.

Pending requests use the signed `POST /api/actions` route and status is available
through `GET /api/actions/{id}`, using the separate send credential. Approving uses the
approval credential (`approve.token`), which the interactive CLI and the running wasync hold;
the query and send credentials cannot approve a request. No agent scope can clean or delete.
Agents should receive only their own scoped credential; filesystem access as your OS user is not
a security boundary against a malicious agent. See
[Data, files and security](data-and-security.md#credentials-and-the-keychain).
