# Generated from the wasync monorepo release. Do not edit by hand.
class Wasync < Formula
  desc "Local WhatsApp archive, terminal UI, and API for people and agents"
  homepage "https://github.com/gehariharan/wasync"
  url "https://api.github.com/repos/gehariharan/wasync/tarball/v0.1.0-preview.20261124095536"
  version "0.1.0-preview.20261124095536"
  sha256 "ce3f0de52f42a2a048bdf5cc43933322397f0dd0a73ddd5a2a758a4f7fe8639c"
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
      -X github.com/gehariharan/wasync/internal/buildinfo.Commit=76c6d26241d3
      -X github.com/gehariharan/wasync/internal/buildinfo.BuiltAt=#{time.iso8601}
    ]
    system "go", "build", *std_go_args(ldflags:), "-tags", "sqlite_fts5", "./cmd/wasync"
  end

  test do
    assert_match "wasync #{version} ", shell_output("#{bin}/wasync version")
    system bin/"wasync", "query", "version", "--v1", "--json"
  end
end
