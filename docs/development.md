# Development

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the contribution rules and
[releasing](releasing.md) for release procedures.

## Build and launch

Requires macOS, Linux, or Windows x64, Go 1.27.1 or newer (the project selects Go 1.27.2), and a
C compiler. On macOS, install the Xcode Command Line Tools if needed.

```sh
git clone https://github.com/gehariharan/wasync.git
cd wasync
go build -tags sqlite_fts5 -o bin/wasync ./cmd/wasync
(cd axi && go build -o ../bin/wasync-axi ./cmd/wasync-axi)
./bin/wasync setup
```

The archive migrates automatically; do not delete your existing data directory. To use a
throwaway archive while developing, pass `--data-dir` (see
[Data, files and security](data-and-security.md#the-data-directory)).

To install the built executable for commands from any directory:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 bin/wasync "$HOME/.local/bin/wasync"
export PATH="$HOME/.local/bin:$PATH"
```

Add the PATH export to your shell configuration if needed. Reinstall the binary
after rebuilding.

## Web app styles

The web app's look lives in two shared stylesheets in `internal/api/webassets`:
`tokens.css` holds the colours, type, spacing and shapes (light and dark), and
`components.css` builds every page from them, including the icon rail. Pages
carry no `<style>` blocks or `style` attributes, and their content policy only
allows wasync's own stylesheets (`style-src 'self'`), so a new look is a class
in `components.css`. `TestNoTemplateCarriesItsOwnStyles` fails on any inline
style. The Inter typeface is bundled as a latin subset of Inter 4.1
(`inter.woff2`, weights 400 to 700) with its licence beside it.

## Validation

```sh
bash scripts/check.sh
```

runs both modules' tests, vet checks, packaging helper checks, a real API/offline integration
check using a disposable synthetic archive, and a terminal UI smoke check. For the core module
alone:

```sh
go test -tags sqlite_fts5 ./...
go test -race -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
```

Tests never touch your real login Keychain: a test binary is given no Keychain
unless you set `WASYNC_TEST_KEYCHAIN=1`, which only the Keychain's own round-trip
test in `internal/approvecred` uses (`WASYNC_TEST_KEYCHAIN=1 go test -tags
sqlite_fts5 ./internal/approvecred`); the other Keychain tests use a throwaway
keychain file. A `wasync` you build from source keeps the private `approve.token`
file unless `WASYNC_TEST_KEYCHAIN=1` is set. Anything that starts a signed release
(or a source build with that variable) with a throwaway `--data-dir` does make a
real Keychain item, because each data directory has its own (`wasync keychain
prune` removes leftovers).

Tests use synthetic events and temporary files. They cover archive migration,
consent checks at commit time, live/history deduplication, account isolation,
original timestamps, out-of-order edits/revokes, search index updates, expiry,
purge, pagination, interrupted batch state, process locks, private files,
terminal controls, filtering, keyboard actions, narrow terminal rendering, automatic
history queueing, cursor encoding, response correlation, pagination, timeout and
cancellation behavior. API tests cover authentication, loopback binding, origin checks,
account isolation, signed pagination, date/sender filters, concurrent reads during
capture, edited/revoked/expired/purged content, the web app pages, and headless shutdown. CLI
query tests cover reads while the capture lock is held, active WAL visibility, pagination,
filters, account isolation, missing archives, read-only enforcement, schema checks,
and default command dispatch. History request tests use an injected sender; they do not
send protocol requests to a real account.

## Manual account checks

Real account delivery and historical completeness cannot be established by the
synthetic tests. With a paired test account:

1. Start wasync (`wasync serve`) and confirm it reuses your paired session and enabled chats.
2. Have someone send a text in an enabled and a disabled chat. Only the enabled
   chat's message should appear in the archive (`wasync web`). No conversation messages are
   sent unless you explicitly send one.
3. Toggle capture in the web app's chat picker; confirm subsequent messages follow the preference.
4. Browse and search in the web app and with `wasync query`.
5. Restart and verify the archive and preferences persist without duplicate rows.
6. Interrupt and restore the network; verify `wasync query status` reports the
   uncertainty. History may or may not be delivered.
7. Test purge on disposable archived data; confirm the chat is disabled and its
   messages disappear from browsing and search.

Do not re-pair simply to obtain history without understanding that replay is not guaranteed.

The original design and risk review are saved in
[the HTML design artifact](../.lavish/whatsapp-sync-design.html).
