#!/usr/bin/env python3
"""Exercise the released CLI pair with a temporary synthetic archive only."""
import base64
import hashlib
import json
import os
from pathlib import Path
import select
import signal
import sqlite3
import subprocess
import sys
import tempfile

wasync, axi = (str(Path(p).resolve()) for p in sys.argv[1:])
with tempfile.TemporaryDirectory(prefix="wasync-pair-smoke-") as temporary:
    root = Path(temporary)
    data = root / "data"
    data.mkdir(mode=0o700)
    # A fixture grant for this disposable, unpaired archive. Never use user data.
    secret = base64.urlsafe_b64encode(bytes(range(32))).decode().rstrip("=")
    credential = "wsa_01234567_" + secret
    registry = data / "agents.json"
    registry.write_text(json.dumps({"version": 1, "clients": [{
        "id": "01234567", "name": "pair-smoke", "scopes": ["read.chats", "read.messages"],
        "chats": [], "rate_per_minute": 6000, "created_at": "2026-01-01T00:00:00Z",
        "secret_sha256": hashlib.sha256(secret.encode()).hexdigest(),
    }]}))
    registry.chmod(0o600)
    environment = os.environ.copy()
    environment.update({"WASYNC_BIN": wasync, "WASYNC_DATA_DIR": str(data),
                        "WASYNC_AXI_CONFIG_DIR": str(root / "config"),
                        "WASYNC_AXI_AGENT_HOME": str(root / "agents"),
                        "WASYNC_AXI_TOKEN": credential})
    owner = subprocess.Popen([wasync, "--data-dir", str(data), "--api-addr", "127.0.0.1:0", "serve", "--offline"],
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        ready, _, _ = select.select([owner.stdout], [], [], 20)
        assert ready, "offline owner did not start"
        first = owner.stdout.readline().strip()
        assert first.startswith("Local API: http://"), first
        environment["WASYNC_API_ADDR"] = first.removeprefix("Local API: http://")
        with sqlite3.connect(data / "archive.db") as database:
            database.execute("INSERT INTO accounts(id,jid,created_at) VALUES(1,'synthetic@lid',1)")
            database.execute("INSERT INTO app_state(id,account_id) VALUES(1,1)")
            database.execute("INSERT INTO groups(account_id,group_id,name,sync_enabled,created_at,updated_at) VALUES(1,'synthetic@g.us','Synthetic Group',1,1,1)")
            database.execute("INSERT INTO messages(account_id,group_id,message_id,sender_id,timestamp,message_type,text_content,source,has_original,received_at) VALUES(1,'synthetic@g.us','one','synthetic@lid',1700000000000,'text','synthetic content','live',1,1700000000000)")

        def run(*arguments, expected=0):
            result = subprocess.run([axi, *arguments, "--json"], capture_output=True, text=True, env=environment, timeout=15)
            assert result.returncode == expected, result.stdout + result.stderr
            return json.loads(result.stdout)

        online = run("messages", "synthetic@g.us")
        assert online["transport"] == "api", online
        assert online["data"]["items"][0]["text_content"] == "synthetic content", online
        environment.pop("WASYNC_AXI_TOKEN")
        denied = run("chats", expected=1)
        assert denied["error_code"] == "credential_missing", denied
    finally:
        owner.send_signal(signal.SIGTERM)
        _, errors = owner.communicate(timeout=15)
        assert owner.returncode == 0, errors
    offline = run("messages", "synthetic@g.us")
    assert offline["transport"] == "query", offline
    assert offline["data"] == online["data"], (offline, online)
print("CLI pair: scoped API read, missing-credential refusal, and identical offline query passed")
