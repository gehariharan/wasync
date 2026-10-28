# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261028063901"
  sha256 arm:   "1507af0bfd53f92d6cc839418211861d8bb2f66f4d3221b2f4925840b16978e3",
         intel: "c578dcc727751d6b6466df2520904ace8b0f686bda80e0b58ad63a8c2276fe46"

  url "https://github.com/gehariharan/wasync/releases/download/v#{version}/wasync_#{version}_darwin_#{arch}.tar.gz"
  name "wasync"
  desc "Local WhatsApp group archive with a terminal UI and agent query CLI"
  homepage "https://github.com/gehariharan/wasync"

  depends_on macos: :sequoia


  binary "wasync"
end
