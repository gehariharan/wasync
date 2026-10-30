# Generated from the wasync monorepo release. Do not edit by hand.
class Wasync < Formula
  desc "Local WhatsApp archive, terminal UI, and API for people and agents"
  homepage "https://github.com/gehariharan/wasync"
  url "https://api.github.com/repos/gehariharan/wasync/tarball/v0.1.0-preview.20261030074136"
  version "0.1.0-preview.20261030074136"
  sha256 "bbd80d88d45303f57cfe5bee035468f9de4f39262d08675686c13b3c005b4429"
  license "MIT"

  depends_on :linux
  depends_on "go" => :build

  def install
    ENV["CGO_ENABLED"] = "1"
    ENV["GOWORK"] = "off"
    ENV["GOTOOLCHAIN"] = "auto"
    ldflags = %W[
      -s -w
      -X github.com/gehariharan/wasync/internal/buildinfo.Version=#{version}
      -X github.com/gehariharan/wasync/internal/buildinfo.Commit=decbfebb5aed
      -X github.com/gehariharan/wasync/internal/buildinfo.BuiltAt=#{time.iso8601}
    ]
    system "go", "build", *std_go_args(ldflags:), "-tags", "sqlite_fts5", "./cmd/wasync"
  end

  test do
    assert_match "wasync #{version} ", shell_output("#{bin}/wasync version")
    system bin/"wasync", "query", "version", "--v1", "--json"
  end
end
