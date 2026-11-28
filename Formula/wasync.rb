# Generated from the wasync monorepo release. Do not edit by hand.
class Wasync < Formula
  desc "Local WhatsApp archive, terminal UI, and API for people and agents"
  homepage "https://github.com/gehariharan/wasync"
  url "https://api.github.com/repos/gehariharan/wasync/tarball/v0.1.0-preview.20261128063630"
  version "0.1.0-preview.20261128063630"
  sha256 "d9b3eb0d3aac976fcbd2c1e70959a6dcf0cd5d2a951a661457ae1bec37a6c5f2"
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
      -X github.com/gehariharan/wasync/internal/buildinfo.Commit=e6367fd7e40f
      -X github.com/gehariharan/wasync/internal/buildinfo.BuiltAt=#{time.iso8601}
    ]
    system "go", "build", *std_go_args(ldflags:), "-tags", "sqlite_fts5", "./cmd/wasync"
  end

  test do
    assert_match "wasync #{version} ", shell_output("#{bin}/wasync version")
    system bin/"wasync", "query", "version", "--v1", "--json"
  end
end
