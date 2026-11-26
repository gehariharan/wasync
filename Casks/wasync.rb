# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261126094649"
  sha256 arm:   "2f41aa30b435f34d4d590874dffb33b80c782a94d9f13bf9ea08637f0b5dbbfb",
         intel: "b4f701cd18e466fef4e146a0ef217aad917d487ee6973ac3b251b52f593fb882"

  url "https://github.com/gehariharan/wasync/releases/download/v#{version}/wasync_#{version}_darwin_#{arch}.tar.gz"
  name "wasync"
  desc "Local WhatsApp group archive with a terminal UI and agent query CLI"
  homepage "https://github.com/gehariharan/wasync"

  depends_on macos: :sequoia


  binary "wasync"

  caveats <<~EOS
    wasync is installed. To let AI agents such as Claude Code, Codex or OpenCode use it,
    also install the optional AXI for agents tool (it changes no agent settings by itself):
      brew install gehariharan/tap/wasync-axi
  EOS
end
