#!/usr/bin/env python3
"""Generate a binary-only Homebrew cask from both native macOS archives."""
import argparse
import hashlib
from pathlib import Path
import re

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("version")
parser.add_argument("directory")
parser.add_argument("output")
parser.add_argument("--private", action="store_true", help="use GitHub CLI authentication for private assets")
args = parser.parse_args()
version, directory, output = args.version, args.directory, args.output
if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?", version):
    parser.error("invalid version")
require = 'require_relative "../lib/wasync_github_release_download_strategy"\n\n' if args.private else ""
strategy = ",\n      using: WasyncGitHubReleaseDownloadStrategy" if args.private else ""
dependency = '  depends_on formula: "gh"\n' if args.private else ""
hashes = {}
for architecture in ("arm64", "amd64"):
    artifact = Path(directory) / f"wasync_{version}_darwin_{architecture}.tar.gz"
    hashes[architecture] = hashlib.sha256(artifact.read_bytes()).hexdigest()
Path(output).write_text(f'''# Generated from release archive checksums. Installs a prebuilt executable.
{require}cask "wasync" do
  arch arm: "arm64", intel: "amd64"

  version "{version}"
  sha256 arm:   "{hashes['arm64']}",
         intel: "{hashes['amd64']}"

  url "https://github.com/gehariharan/wasync/releases/download/v#{{version}}/wasync_#{{version}}_darwin_#{{arch}}.tar.gz"{strategy}
  name "wasync"
  desc "Local WhatsApp group archive with a terminal UI and agent query CLI"
  homepage "https://github.com/gehariharan/wasync"

  depends_on macos: :sequoia

{dependency}
  binary "wasync"

  caveats <<~EOS
    wasync is installed. To let AI agents such as Claude Code, Codex or OpenCode use it,
    also install the optional AXI for agents tool (it changes no agent settings by itself):
      brew install gehariharan/tap/whatsapp-axi
  EOS
end
''', encoding="utf-8")
