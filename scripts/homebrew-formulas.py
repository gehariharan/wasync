#!/usr/bin/env python3
"""Generate the source formulas from one immutable monorepo release archive:
wasync, wasync-axi and whatsapp-axi (wasync-axi's old name)."""
import argparse
from pathlib import Path
import re
import runpy

ROOT = Path(__file__).resolve().parent.parent


def render_wasync(version, checksum, commit):
    flags = f"\n      -X github.com/gehariharan/wasync/internal/buildinfo.Commit={commit}" if commit else ""
    return f'''# Generated from the wasync monorepo release. Do not edit by hand.
class Wasync < Formula
  desc "Local WhatsApp archive, terminal UI, and API for people and agents"
  homepage "https://github.com/gehariharan/wasync"
  url "https://api.github.com/repos/gehariharan/wasync/tarball/v{version}"
  version "{version}"
  sha256 "{checksum}"
  license "MIT"

  depends_on :linux
  depends_on "go" => :build

  def install
    ENV["CGO_ENABLED"] = "1"
    ENV["GOWORK"] = "off"
    ENV["GOTOOLCHAIN"] = "auto"
    ldflags = %W[
      -s -w
      -X github.com/gehariharan/wasync/internal/buildinfo.Version=#{{version}}{flags}
      -X github.com/gehariharan/wasync/internal/buildinfo.BuiltAt=#{{time.iso8601}}
    ]
    system "go", "build", *std_go_args(ldflags:), "-tags", "sqlite_fts5", "./cmd/wasync"
  end

  test do
    assert_match "wasync #{{version}} ", shell_output("#{{bin}}/wasync version")
    system bin/"wasync", "query", "version", "--v1", "--json"
  end
end
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("source_sha256")
    parser.add_argument("directory", type=Path)
    parser.add_argument("--commit")
    args = parser.parse_args()
    axi = runpy.run_path(str(ROOT / "axi/scripts/homebrew-formula.py"))
    if not re.fullmatch(axi["VERSION_RE"], args.version):
        parser.error("invalid release version")
    if not re.fullmatch(r"[0-9a-f]{64}", args.source_sha256):
        parser.error("source checksum must be 64 lowercase hex digits")
    if args.commit is not None and not re.fullmatch(r"[0-9a-f]{7,40}", args.commit):
        parser.error("invalid commit")
    args.directory.mkdir(parents=True, exist_ok=True)
    (args.directory / "wasync-formula.rb").write_text(render_wasync(args.version, args.source_sha256, args.commit))
    (args.directory / "wasync-axi.rb").write_text(axi["render"](args.version, args.source_sha256, args.commit))
    (args.directory / "whatsapp-axi.rb").write_text(axi["render_legacy"](args.version, args.source_sha256))


if __name__ == "__main__":
    main()
