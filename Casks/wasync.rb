# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261028003906"
  sha256 arm:   "a64ddd80b88e7db6aaa8759558924d3ce6b4f9397cd15edd6a55c71697466455",
         intel: "02d84dc68c4c0e9b697fb18702a1517be8ab5109165c5a8015b50c8404ad8a22"

  url "https://github.com/gehariharan/wasync/releases/download/v#{version}/wasync_#{version}_darwin_#{arch}.tar.gz"
  name "wasync"
  desc "Local WhatsApp group archive with a terminal UI and agent query CLI"
  homepage "https://github.com/gehariharan/wasync"

  depends_on macos: :sequoia


  binary "wasync"
end
