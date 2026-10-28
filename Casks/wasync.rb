# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261028005855"
  sha256 arm:   "2d4f69ca88b08d4e02c579684a98c59e0af55f42472bb2c3af088e4ab323cc62",
         intel: "6a519e878fd8c70af43edc70f8f7a12bb154d0f770a4458786745e079a4461fe"

  url "https://github.com/gehariharan/wasync/releases/download/v#{version}/wasync_#{version}_darwin_#{arch}.tar.gz"
  name "wasync"
  desc "Local WhatsApp group archive with a terminal UI and agent query CLI"
  homepage "https://github.com/gehariharan/wasync"

  depends_on macos: :sequoia


  binary "wasync"
end
