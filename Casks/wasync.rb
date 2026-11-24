# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261124084532"
  sha256 arm:   "e85741a33aec6acbfbe7a01e0d09edb54682a80db025c03506719cbef2cb7f4b",
         intel: "8a542a37ad0f101f665ccd4f4ba9c35a86cf80750405523d222d6d9f850e7c3b"

  url "https://github.com/gehariharan/wasync/releases/download/v#{version}/wasync_#{version}_darwin_#{arch}.tar.gz"
  name "wasync"
  desc "Local WhatsApp group archive with a terminal UI and agent query CLI"
  homepage "https://github.com/gehariharan/wasync"

  depends_on macos: :sequoia


  binary "wasync"

  caveats <<~EOS
    wasync is installed. To let AI agents such as Claude Code, Codex or OpenCode use it,
    also install the optional AXI for agents tool (it changes no agent settings by itself):
      brew install gehariharan/tap/whatsapp-axi
  EOS
end
