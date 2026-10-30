# Threat model: agent access to wasync

Scope: the local wasync owner (TUI or `serve`), its loopback API, the `wasync query` CLI, and agent clients such as whatsapp-axi. See [ADR-001](ADR-001-repository-boundaries.md).

## Assets

- WhatsApp pairing credentials (whatsmeow session store)
- The SQLite archive: message bodies, media, chat membership, capture preferences
- Ability to act as the user on WhatsApp: send, react, edit, revoke, change chat or group state
- Legacy tokens (`api.token`, `send.token`, `approve.token`) and agent credentials

## Trust boundaries

| Boundary | Enforcement |
|---|---|
| Other machines → API | Literal loopback address only (`api.ValidateAddr`); Host and Origin checks against DNS rebinding and browsers |
| Other local users → data dir | Private file and directory permissions (`internal/localfs`, Unix modes and Windows ACLs) |
| Agent → wasync | Scoped agent credential checked by wasync on every `/api/v1` request; scopes, chat allowlist, rate limit, expiry; audit log |
| Agent → destructive actions | Not grantable; human approval queue; the owner asks the person at the computer to confirm (see "What the human check is, and is not") |
| Agent → approving its own request | The owner, not the caller, asks the person: Touch ID or the account password on macOS, Windows Hello on Windows. A signed request, a typed phrase or a click on the approval page is necessary but never sufficient where this check exists |
| Same OS user → archive | Not a boundary. `wasync query` offline reads are equivalent to the user reading their own files and are read-only |
| WhatsApp content → agent | Message text and contact or chat names are untrusted data; never rendered as instructions or hints |

## Threats and mitigations

| Threat | Mitigation |
|---|---|
| Agent instructed by message content (prompt injection) to send or delete | Writes need explicit scopes and allowlisted chats granted by a human in wasync; destructive actions are never grantable; AXI renders content as delimited data |
| Agent or skill passes flags to bypass confirmation | Flags are UX only; wasync enforces scopes server-side |
| Leaked agent credential | Hash-only storage, per-client revoke and expiry, rate limits, audit trail |
| Agent credential replayed on legacy routes, or legacy token on v1 | The two credential types are isolated; each route family accepts only its own |
| Legacy read token mutating capture policy (existing behaviour) | Recorded in the baseline audit; unchanged for compatibility; agent credentials do not inherit it |
| Duplicate WhatsApp session | Only wasync pairs; `device.lock`; AXI never connects to WhatsApp |
| Data loss on upgrade | Additive-only migrations, automatic pre-migration backup, schema upgrade tests from every released schema |
| Contact names in logs, or hostile names reaching an agent | Direct-chat names (address-book, push and business names) come from whatsmeow's contact store and events and are stored only in the local archive; they are never written to logs or diagnostics. Names are trimmed of control characters and capped at 256 characters when stored, shown in `whatsapp-axi` as sanitized `untrusted_` values, and visible to an agent only through the same `read.chats` scope and chat allowlist as the chat itself. Push names are chosen by other people, so a name can impersonate a contact or carry instruction-like text; agents must treat it as data, and a name is never an authorization input (allowlists use chat IDs). Residual risk: an agent with `read.chats` learns the owner's saved contact names for the chats it may see |
| Secrets or bodies in logs | Audit and diagnostics record identifiers and outcomes only |
| Change feed skipping events | Transactional journal with sequence cursors and an epoch; no timestamp cursors |
| Agent talks its way into a chat it was not given | By default access is never granted by asking. A request becomes a pending human approval (TUI dialog, the approval page, or `wasync actions approve` with a typed `ALLOW CODE`; five-minute expiry, no replay, approve token separate from every agent credential). Where the computer has a system confirmation, the owner then asks the person to confirm with it, whichever way the approval arrived. The person sees which agent, which chat and whether read, send/reply/react or also edit-own, and picks the history depth before confirming. Approval enables capture for that one chat and adds a per-chat grant limited to scopes the credential already holds (read, send and edit are separate; capture, group and export scopes are never requestable). Rejection, expiry, a revoked agent or any failure changes nothing; grants and denials are audited |
| Agent floods the person with prompts, hides what it wants, or probes which chats exist | Rate limit, one prompt per agent and query, at most three unanswered per agent, 32 queued overall. The dialog shows wasync's resolved candidates, not text the agent supplied; the agent's query is shown as sanitized display text and chat names have control characters stripped. The reply to an access request is `pending` whether zero, one or many chats matched (a request that matched nothing is queued, shown to the person as "no chat matched", cannot be approved, and expires), and candidates are never returned, so asking reveals nothing about chats the agent was not given. Headless, `serve` announces each new request on its console and log with the agent name and request ID only; the chats appear only in `wasync actions list`, which needs an interactive terminal and the send credential |
| Owner picks "sync all chats" and an agent then misuses it | A deliberate, human-only trade-off: `wasync access mode all` (or the TUI setup screen / `M`, or the approval page) needs a typed `SYNC ALL` in a terminal where one is used, then the system confirmation where the computer has one, and the screen says plainly that agents can then discover and access any chat by name or number (a matching request is granted at once, so a name probe both reveals and grants that chat) and read, send, reply and react in any chat without asking; ask-each-time mode reveals nothing. It enables capture for all chats with the history depth the owner picks, and approves only requests for the one chat a request matched; edit-own and destructive actions stay manual, requests that match nothing or several chats still wait, and per-agent rate limits, expiry, revocation and the credential's own scopes still apply. Every automatic approval is recorded as an approval request and as `access_grant_auto` in the audit log. The mode is a private file with no agent-reachable route; a damaged file means ask each time. Switching back to ask stops future automatic approvals but does not revoke grants already given; revoke the agent for that. Residual risk while the mode is on: a prompt-injected agent that holds a send scope can send in any chat it names, so use `--scope` narrowly and prefer ask-each-time for agents that read untrusted content |
| Webhook SSRF | Endpoints are registered only by a human (interactive terminal, typed confirmation; no scope, no API route). HTTPS only. The dialer resolves each name once, refuses the whole answer if any address is loopback, private, link-local (incl. cloud metadata), carrier-grade NAT, multicast, reserved or an IPv4-embedding IPv6 range, and connects to the vetted address, so DNS rebinding cannot change the target. Redirects are never followed and proxy variables are ignored |
| Webhook data egress | Opt-in per endpoint; events carry identifiers only (journal sequence, op, chat ID, message ID, message ref), never bodies or media, so a receiver must hold a scoped read credential to see content. Endpoints can be limited by chat and operation. Residual risk: chat IDs (phone numbers, group IDs) and the fact and timing of activity reach the receiver |
| Forged or replayed webhook requests | HMAC-SHA256 over timestamp and body with a per-endpoint secret, a fresh timestamp on every attempt, a stable event ID for deduplication. Receivers must enforce the five-minute window. The secret is stored in `webhooks.json` (0600) because signing needs it; same trust level as `api.token` |
| Lost or duplicated webhook events | Per-endpoint checkpoint in `webhook-state.json`, advanced only after a 2xx or a recorded dead letter; at-least-once, in order. A position that can no longer be served (restored archive, retention) resets to the journal head and is audited |

## What the human check is, and is not

Until this check existed, "an agent can never approve itself" rested on two soft barriers, and both were tested and found to give way:

- `approve.token` is a 0600 file that any program running as the same user can read. In Codex's default sandbox the loopback API is unreachable, but with network access on, or in full-access mode, a short script that signed a request with that token was accepted by the owner with no terminal and no typed phrase.
- The "interactive terminal" test lives in the command-line client. A pseudo-terminal (for example `script`) satisfies it, so a shell-capable agent could type the phrase itself.

The owner now closes both on macOS and Windows. Every approval of an agent's request, and every choice of "sync all chats", ends in a prompt drawn by the operating system, raised by the owner process whichever way the approval arrived (command line, TUI key press, approval page, direct API call). Creating or revoking an agent credential (`wasync agents grant`, `agents revoke`) asks the same prompt from the command itself after the typed name, because those commands edit `agents.json` directly and work with no owner running. The prompt is the one thing a program running as the user cannot answer: it needs a fingerprint, the account password, or Windows Hello.

| Property | How it holds |
|---|---|
| The prompt says what is being approved | The sentence is built by the owner from its own data: agent name, chosen chat's name from the archive, read or send, history depth, or the destructive action. The agent's own query text is never in it. Names are stripped of control characters and shortened |
| An agent cannot wear the person out with prompts | One prompt at a time; three unconfirmed prompts in ten minutes lock further prompts for five minutes; a request must exist (or the call must be a valid mode change) before any prompt shows |
| "Sync all chats" is still a person's choice | Setting it needs the prompt. Requests it then approves automatically do not prompt again, as before |
| Narrowing mostly does not need the check | Choosing ask-each-time and rejecting a request are safe for an agent to cause, so they do not prompt. Revoking a credential does prompt, because revoking one agent and granting another is how a credential is swapped |
| A notification is only a nudge | It names the agent and says to look; it carries no code, no chat name and no button, and seeing or dismissing it approves nothing |
| The approval page is not a boundary | Anything that can reach the loopback listener can open the page (its sign-in link is one-time and short-lived, which is hygiene). The page forwards an approval to the same owner path, so it still ends in the prompt. Where the computer has no system confirmation the page lists requests but cannot approve |

Not covered, stated plainly:

- **Code running as the same user that can write the data folder** can still change `access-mode.json` or the agent registry directly, or attach a debugger to the owner. The same-OS-user line above still applies; the sandbox an agent runs in (Codex's default workspace sandbox denies writes outside the workspace) is what holds there. `webhooks add` and `webhooks remove` write their own file from the terminal and still rely on a typed phrase; putting them behind the prompt is planned. The grant and revoke prompts protect the command-line path; they do not stop a program that edits `agents.json` itself, for the same reason.
- **A computer with no system confirmation** (Linux, or a session with no screen) keeps the typed phrase in a terminal as the only check. The owner says so on its first console line, and the approval page shows requests without offering to approve them. Running agents with access to the data folder is not safe there.
- **A person who confirms a prompt without reading it** has approved it. The sentence is deliberately short and specific for that reason.
- **Windows.** The check asks for Windows Hello first (through the PowerShell that ships with Windows). On a computer where Hello is not set up it shows the standard Windows security dialog and accepts only the password of the account wasync runs as (checked with `LogonUser`, and the account that logs on must be the current one). If a dialog cannot be shown or the answer is wrong the approval is refused, never allowed; the typed-phrase-only mode applies only when there is no desktop session at all (for example wasync running as a service). The notification is a tray balloon. All of this builds and is checked by the Windows build, but none of it has been run on a real Windows computer by the author, so treat Windows as untested until someone has walked through an approval there. A Windows Hello PIN is covered by the Hello path; the password dialog does not accept a PIN.

## Setup page and start-at-login

| Property | How it holds |
|---|---|
| The pairing QR code is shown only as an image | The setup page draws it as an image from the code and never prints the code as text, in the page or the logs. The code is useless without the phone it is scanned by |
| A connected agent's secret is never displayed | The owner creates the credential and writes it to the file whatsapp-axi reads, atomically and privately, then forgets it. If it cannot be saved, the credential is revoked at once. It never replaces an existing saved credential without an explicit tick |
| Connecting an agent, and installing start-at-login, need the person | Both end in the system prompt, with a sentence built by the owner. Where the computer has no prompt the page offers neither and shows the terminal command, whose own typed confirmation is then the only check (see above) |
| The login item is per user and visible | A macOS LaunchAgent plist or a Windows `Run` value, both for the current account, no administrator rights, removed by `wasync service uninstall` or the page. Installing only writes the item; it starts at the next login, so it never races a wasync the person is already running |
| The page opens by a link nobody reads | `wasync setup` and `wasync approvals` ask the running owner for a one-time, two-minute link and hand it straight to the browser. It is never printed, so a program that ran the command does not learn it. Anything that can sign with `approve.token` can still ask for one, which is why the page is hygiene and the system prompt is the gate |
| Choosing chats to capture is not agent access | It changes only what is archived on this computer. An agent sees nothing it has not been granted and approved |

Not covered: the Windows login item and prompts are untested on a real Windows computer; Linux has no login item.
