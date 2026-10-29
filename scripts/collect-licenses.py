#!/usr/bin/env python3
"""Collect notices from the modules used by the release binaries."""
import json
from pathlib import Path
import shutil
import subprocess
import sys

output = Path(sys.argv[1])
output.mkdir(parents=True, exist_ok=True)
# The release ships whatsapp-axi next to wasync, so both binaries' modules are listed.
root = Path(__file__).resolve().parent.parent
modules = {}
for directory, package, tags in ((root, "./cmd/wasync", ["-tags", "sqlite_fts5"]), (root / "axi", "./cmd/whatsapp-axi", [])):
    raw = subprocess.check_output(["go", "list", *tags, "-deps", "-json", package], text=True, encoding="utf-8", cwd=directory)
    decoder = json.JSONDecoder()
    while raw.strip():
        entry, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        module = entry.get("Module", {})
        if module.get("Dir") and not module.get("Main"):
            modules[module["Path"]] = module
index = ["Dependencies bundled in wasync and whatsapp-axi; versions and upstream source locations:"]
for name, module in sorted(modules.items()):
    version = module.get("Version", "unknown")
    index.append(f"{name} {version} — https://pkg.go.dev/{name}@{version}")
    destination = output / (name.replace("/", "_") + "@" + version)
    destination.mkdir()
    for source in Path(module["Dir"]).iterdir():
        if source.is_file() and source.name.upper().startswith(("LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT")):
            shutil.copyfile(source, destination / source.name)
goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True, encoding="utf-8").strip())
shutil.copyfile(goroot / "LICENSE", output / "GO_LICENSE")
(output / "DEPENDENCIES.txt").write_text("\n".join(index) + "\n", encoding="utf-8")
