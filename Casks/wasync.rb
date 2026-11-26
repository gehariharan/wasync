# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261126105548"
  sha256 arm:   "a778e12115ceefc289e655f9c0fcea8edacf9462968381d9a83f4128d7f22bb6",
         intel: "5dc5daaf48be1a63d3bab2dc95aebd972bd9598e089fef18f4bf7172e854983d"

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
