#!/usr/bin/env python3
"""Exercise an installed release using temporary synthetic data, never WhatsApp."""
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import signal
import sqlite3
import struct
import subprocess
import sys
import tempfile
import termios
import time

binary, version, architecture = sys.argv[1:]
reported = subprocess.check_output([binary, "version"], text=True, timeout=10)
assert f"wasync {version} " in reported, reported
assert f"{'darwin' if sys.platform == 'darwin' else 'linux'}/{architecture}" in reported, reported

with tempfile.TemporaryDirectory(prefix="wasync-installed-smoke-") as directory:
    data = str(Path(directory) / "data")
    service = subprocess.Popen(
        [binary, "--data-dir", data, "--api-addr", "127.0.0.1:0", "serve", "--offline"],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    try:
        ready, _, _ = select.select([service.stdout], [], [], 15)
        assert ready, "offline service did not start"
        first = service.stdout.readline()
        assert first.startswith("Local API: "), first
        db = sqlite3.connect(str(Path(data) / "archive.db"))
        db.execute("INSERT INTO accounts(id,jid,created_at) VALUES(1,'synthetic@lid',1)")
        db.execute("INSERT INTO app_state(id,account_id) VALUES(1,1)")
        db.execute("INSERT INTO groups(account_id,group_id,name,sync_enabled,created_at,updated_at) VALUES(1,'synthetic@g.us','Synthetic Group',1,1,1)")
        for number in range(3):
            db.execute("INSERT INTO messages(account_id,group_id,message_id,sender_id,timestamp,message_type,text_content,source,has_original,received_at) VALUES(1,'synthetic@g.us',?,'synthetic@lid',1700000000000,'text','synthetic content','live',1,1700000000000)", (str(number),))
        db.commit()
        db.close()

        def query(*args):
            result = subprocess.run([binary, "--data-dir", data, "query", *args], check=True, capture_output=True, text=True, timeout=10)
            return json.loads(result.stdout)

        assert len(query("groups", "--enabled")["items"]) == 1
        first_page = query("messages", "--group", "synthetic@g.us", "--limit", "1")
        next_page = query("messages", "--group", "synthetic@g.us", "--limit", "1", "--cursor", first_page["next_cursor"])
        assert next_page["items"][0]["id"] < first_page["items"][0]["id"]
        assert len(query("search", "synthetic")["items"]) == 3
        assert query("status")["connection"] == "unknown"
        assert not (Path(data) / "session.db").exists()
    finally:
        service.send_signal(signal.SIGTERM)
        _, errors = service.communicate(timeout=10)
        assert service.returncode == 0, errors

    # A real terminal is required to validate offline TUI startup and exit.
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 36, 120, 0, 0))
    environment = os.environ.copy()
    environment["TERM"] = "xterm-256color"
    ui = subprocess.Popen([binary, "--data-dir", data, "--no-api", "tui", "--offline"],
                          stdin=slave, stdout=slave, stderr=slave, env=environment, start_new_session=True)
    os.close(slave)
    captured = b""

    def wait_for(text):
        """Read terminal output until text appears; False if it never does."""
        global captured
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline and text not in captured:
            ready, _, _ = select.select([master], [], [], 0.25)
            if ready:
                chunk = os.read(master, 65536)
                captured += chunk
                # Reply to terminal cursor/color queries without changing app state.
                if b"\x1b[6n" in chunk:
                    os.write(master, b"\x1b[1;1R")
            if ui.poll() is not None:
                break
        return text in captured

    try:
        # A fresh data directory has no access mode yet, so the first screen asks
        # how agents get chats and covers the chat list until it is dismissed.
        # Only the Homebrew e2e job sets this opt-in, because it tests the last
        # published release, which may predate that screen. Remove the opt-in
        # once a release containing the first-run screen is published.
        allow_missing = os.environ.get("WASYNC_SMOKE_ALLOW_MISSING_FIRST_RUN") == "1"
        if wait_for(b"HOW SHOULD AGENTS GET CHATS"):
            captured = b""
            os.write(master, b"\x1b")  # Esc: decide later, which saves nothing
        else:
            assert allow_missing, "offline TUI did not show the first-run access mode screen"
        assert wait_for(b"Synthetic Group"), "offline TUI did not render the synthetic group"
        os.write(master, b"q")
        # Drain the terminal while the renderer restores terminal state; a full
        # PTY output buffer otherwise blocks shutdown even after q is handled.
        deadline = time.monotonic() + 10
        next_quit = time.monotonic() + 1
        while ui.poll() is None and time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                try:
                    os.read(master, 65536)
                except OSError:
                    break
            if time.monotonic() > next_quit:
                os.write(master, b"q")
                next_quit = time.monotonic() + 1
        ui.wait(timeout=2)
        assert ui.returncode == 0, f"TUI exit status {ui.returncode}"
    finally:
        if ui.poll() is None:
            ui.terminate()
            try:
                ui.wait(timeout=5)
            except subprocess.TimeoutExpired:
                ui.kill()
                ui.wait(timeout=5)
        os.close(master)

print(f"Installed {version} ({architecture}): version, concurrent JSON queries, FTS5 search, pagination, shutdown and offline TUI passed.")
