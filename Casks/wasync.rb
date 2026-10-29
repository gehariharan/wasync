# Generated from release archive checksums. Installs a prebuilt executable.
cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "0.1.0-preview.20261029001709"
  sha256 arm:   "17076d8ebb9d19354ee32ed61c9431b82c5a6e2fb6d12147bc87e55610b5a007",
         intel: "f40b2d52acfafedb71148b1573d3cd85153a6374f6afb7651215561e91b3b735"

  url "https://github.com/gehariharan/wasync/releases/download/v#{version}/wasync_#{version}_darwin_#{arch}.tar.gz"
  name "wasync"
  desc "Local WhatsApp group archive with a terminal UI and agent query CLI"
  homepage "https://github.com/gehariharan/wasync"

  depends_on macos: :sequoia


  binary "wasync"
end
