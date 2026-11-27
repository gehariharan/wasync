module github.com/gehariharan/wasync

go 1.27.1

toolchain go1.27.2

require (
	charm.land/bubbletea/v2 v2.1.0
	charm.land/lipgloss/v2 v2.0.6
	github.com/BurntSushi/toml v1.5.0
	github.com/charmbracelet/x/ansi v0.11.9
	github.com/go-webauthn/webauthn v0.18.2
	// Builds the Mac disk image (packaging/macos/mkdmg); the version Wails v3.1.0 uses.
	github.com/leaanthony/dmg v0.0.0-20260731074841-5c28840cf819
	github.com/mattn/go-sqlite3 v1.14.53
	github.com/mdp/qrterminal/v3 v3.2.1
	// The wasync app (cmd/wasync-desktop). Pinned exactly: v3.1.0 is the first
	// stable minor after Wails 3 left alpha and beta (v3.0.0, 2026-10-26), and
	// its window, tray and single-instance API is what the app is built and
	// tested against, so move it only deliberately and retest the app.
	github.com/wailsapp/wails/v3 v3.1.0
	go.mau.fi/libsignal v0.2.3
	go.mau.fi/whatsmeow v0.0.0-20261126231607-ae388b0429be
	golang.org/x/sys v0.50.0
	golang.org/x/term v0.48.0
	google.golang.org/protobuf v1.36.12
	rsc.io/qr v0.2.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/beeper/argo-go v1.1.2 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/elliotchance/orderedmap/v3 v3.1.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.4 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.30 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/petermattis/goid v0.0.0-20261109124353-7cfa343ad0ab // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/vektah/gqlparser/v2 v2.5.27 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	go.mau.fi/util v0.11.1 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/exp v0.0.0-20261113113407-e3970619a1a4 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	howett.net/plist v1.0.2-0.20250314012144-ee69052608d9 // indirect
)
