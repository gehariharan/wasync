# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261126084438"
  sha256 arm:   "7eb8ca6ae775d7c1d024d37377a835eb916c05b8d8a1e7ef7f003f3731910aff",
         intel: "26af170d787ad8e5ebd4c29aacb72449a12126012bd4361f41637eb965e6e337"

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
