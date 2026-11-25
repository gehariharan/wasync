# Benchmark: wasync-axi vs raw `wasync query --v1 --json`

This benchmark measures what an agent has to read, and how many commands it has to run, to do typical
WhatsApp tasks through wasync-axi compared with calling wasync's offline JSON interface directly. It
uses only synthetic data.

## How to run it

```sh
go test ./internal/bench -run TestBenchmarkReport -v          # prints the table below
go test ./internal/bench -run TestBenchmarkReport -v -short   # small corpus (8 chats x 30 messages)
go test ./internal/bench -run '^$' -bench .                   # timings, with sizes as metrics
WASYNC_AXI_BENCH_DUMP=/some/dir go test ./internal/bench -run TestBenchmarkReport   # keep every output
```

`go test ./...` (and so CI) runs the report test on the full corpus in well under a second, so the
harness cannot rot: it fails if wasync-axi ever needs more calls than raw wasync for a task, prints more
bytes or tokens, or if the session hook output grows past 200 tokens.

## Method

**Corpus.** `internal/bench/corpus_test.go` generates, from a fixed seed, 40 chats (15 groups, 25 direct
chats) of 150 messages each: invented names, sentences drawn from a small English vocabulary, roughly one
message in 12 long (40 to 100 extra words) and one in 40 mentioning "invoice". A change journal holds a
change (mostly `message.created`, some edits and deletes) for each of the newest 200 messages. Records
have the shape of wasync's v1 data, including the fields wasync-axi does not print (`account_id`,
`group_id`, `received_at`, `media_policy`, `history_*` and so on), because a raw reader receives them too.

**Serving.** The corpus is turned into fixtures and served by the fake wasync binary from
`internal/testutil` (the test binary re-executed in fixture mode). Every read goes through the real code
paths: wasync-axi runs in process with no API listening, so it selects the offline `wasync query`
transport and spawns the fake exactly as it would spawn wasync; the raw side spawns the fake with the
argument list `transport.QueryArgs` builds (`wasync query <cmd> ... --v1 --json`) and keeps its stdout.
The fake includes current owner-provided totals for both message and search pages.
Raw output is compact JSON on one line, which is the cheapest possible form of it; pretty-printed JSON
would make the raw numbers larger.

**Tasks.** Each task is done the way an agent would do it with each tool, with the same page sizes (20
messages or hits, 100 changes):

| Task | wasync-axi | raw wasync |
|---|---|---|
| orient: is WhatsApp data reachable, what chats exist | `wasync-axi` | `wasync query version`, `wasync query chats` |
| list_chats: every chat with id, kind, size, activity | `wasync-axi chats --fields id,kind,messages,last_message_at` | `wasync query chats` |
| read_chat: find "Project Falcon", read its last 20 messages | `wasync-axi messages "Project Falcon"` | `wasync query chats --match=...`, then `wasync query messages --chat=JID --limit=20` |
| search_context: 20 newest hits for "invoice", 2 messages of context each | `wasync-axi search invoice --context 2` | `wasync query search --context=2 --limit=20 -- invoice` |
| follow_changes: next 100 changes after a cursor, then the busiest changed chat's newest messages | `wasync-axi observe --after c1_...`, `wasync-axi messages JID` | `wasync query changes --after=c1_... --limit=100`, `wasync query messages --chat=JID --limit=20` |

**Measures.**

- *Agent calls*: commands the agent runs (tool invocations, each a round trip through the model).
- *wasync calls by axi*: subprocesses wasync-axi started, counted from the fake's call log. These are
  cheap local calls the agent never sees; every wasync-axi command first asks `wasync query version` to
  check compatibility.
- *Bytes*: everything the command printed to stdout, including wasync-axi's `help:` hints.
- *~Tokens*: an approximation, because no model tokenizer is available offline: a run of letters counts one
  token per 4 characters (rounded up), a run of digits one per 3, and every other non-space character
  one. On English prose this is close to the usual "4 characters per token"; it charges JSON punctuation
  and opaque identifiers (base64 refs and cursors, JIDs) more, as BPE tokenizers do. Both sides use the
  same function, so the comparison is fair even where the absolute numbers are off.

## Results

Linux amd64, Go 1.27.2, full corpus (40 chats x 150 messages):

| Task | Agent calls (axi / raw) | wasync calls by axi | Bytes (axi / raw) | Bytes saved | ~Tokens (axi / raw) | Tokens saved |
|---|---|---|---|---|---|---|
| orient | 1 / 2 | 2 | 1417 / 12218 | 88% | 504 / 6309 | 92% |
| list_chats | 1 / 1 | 2 | 2817 / 11921 | 76% | 1401 / 6149 | 77% |
| read_chat | 1 / 2 | 3 | 4897 / 11652 | 58% | 1788 / 5514 | 68% |
| search_context | 1 / 1 | 2 | 16344 / 48200 | 66% | 5399 / 23130 | 77% |
| follow_changes | 2 / 2 | 5 | 22589 / 41470 | 46% | 9469 / 20280 | 53% |
| **total** | 6 / 8 | | 48064 / 125461 | 62% | 18561 / 61382 | 70% |

Session hook (`wasync-axi --brief`, offline, no credential): 466 bytes, about 149 tokens per session.

Sizes vary by a few bytes between machines because the home view and the brief view print local paths
(the wasync binary, the credential file).

## Reading the results

- **Fewer calls where names are involved.** Reading a chat by name takes one wasync-axi command
  instead of two raw calls: wasync-axi resolves the name with wasync's `?match=` and, when the name is
  ambiguous, prints the candidates instead of guessing. Orientation takes one call because the home view
  combines the version handshake with the chat list.
- **Smaller output everywhere.** The compact [list views](../README.md#usage) omit
  JSON keys and unselected fields. The context-search task uses `hit`, `role`,
  `ref` and `untrusted_text`; the chat-list task explicitly selects last activity.
  Truncation and omission of per-change cursors also reduce output. The home view
  shows the 5 most recent chats and aggregates instead of every chat.
- **What the savings cost.** wasync-axi drops fields; `--json` on any command returns wasync's v1 data
  unchanged when an agent needs them. Truncation means a long message may need a second call. The
  `help:` hints cost a few dozen tokens per command and are counted above.
- **Identifiers dominate what remains.** Message refs are long base64 strings and make up a large part of
  the wasync-axi tables; they are opaque by contract, so wasync-axi prints them unchanged.
- **The session hook is small and contentless.** It runs at the start of every agent session, so it prints
  wasync's state, the chat count and two hints, but no chat names or message text.

## Limits

- The corpus is synthetic: real messages have emoji, other scripts, media and links, which tokenize
  differently. Ratios on real data will differ; the direction will not.
- Only the offline `wasync query` transport is measured. Through the running wasync API the data is the
  same and wasync-axi's output differs only in its `transport:` line.
- Token counts are approximate (see Measures). Re-run with a real tokenizer before quoting absolute
  numbers.
- Agent-side reasoning is not measured: the benchmark counts what is read and how many commands are run,
  not whether an agent chooses them well.
