# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261125063515"
  sha256 arm:   "5d1450a2dd79d61a035d30c4d9af6f3c6b97e3cfa1f8ce30eb3c62e0a4786e68",
         intel: "dd33fd67b26d4c5aa2332b2f00743562e38e53a8f0fee45b4720c7ab2ed70a5a"

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
