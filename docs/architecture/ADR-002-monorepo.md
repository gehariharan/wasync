# ADR-002: One wasync source and distribution repository

Status: chosen by Hari on 2026-10-20. Supersedes ADR-001's separate repository
and release ownership for wasync-axi; its process and authorization boundaries
remain in force.

The project is named **wasync**, at `gehariharan/wasync`. It contains:

- The existing core Go module at the repository root.
- The independently buildable agent module under `axi/`, keeping the
  `wasync-axi` executable name and its existing configuration directory.
- The Homebrew definitions under root `Formula/` and `Casks/`.

The AXI and tap histories are imported without squashing. Old releases and
repositories remain available for installation compatibility. The former
repositories become historical locations after the consolidated release is
verified; they are no longer release targets.
Do not delete historical releases or reuse the former `WhatsappTuiCli` name;
GitHub redirects preserve old source and download links.

Root `vVERSION` and nested-module `axi/vVERSION` tags identify the same commit.
The common release contains both binaries, checksums and generated Homebrew
definitions. Source formulas build from that same immutable root source tag.
The existing signed/notarized core Mac packages remain; AXI's Mac binaries now
use the same Developer ID certificate and their own stable signing identifier.
The original core installer identifier, data directories, device lock and
paired session remain unchanged.

After verified publication, automation updates only the owned Homebrew files
on this repository's main branch. These generated commits do not start another
preview release. No deploy key or write access to a second repository is needed.

The logical tap and fully qualified formula/cask names remain unchanged.
A repository rename alone does not migrate another tap's remote; see
[Homebrew installation](../../README.md#homebrew-installation) for migration
and platform-specific commands. Old signed release assets are kept intact.

Consolidation changes packaging and coordination, not capture policy, agent
permissions, archive schema or the user's WhatsApp pairing.
