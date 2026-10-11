"""Exercise the real Windows/Linux CLI, stdio MCP, hooks and optional Docker hub.
Only creates isolated fixture data; never reads user projects or prints secrets.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import queue
import secrets
import subprocess
import threading
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--exe", default=str(ROOT / "bin" / "akasha.exe"))
    parser.add_argument("--hub-url", default="http://127.0.0.1:17342")
    parser.add_argument("--external-hub", action="store_true")
    parser.add_argument("--out", default="reports/smoke.json")
    args = parser.parse_args()
    root = ROOT / ".local" / ("smoke-" + uuid.uuid4().hex[:12])
    root.mkdir(parents=True)
    env = dict(os.environ)
    if not args.external_hub:
        env["AKASHA_HUB_TOKEN"] = secrets.token_hex(32)
    assert len(env.get("AKASHA_HUB_TOKEN", "")) >= 32, "Test hub token required"
    env["AKASHA_VAULT_PASSWORD"] = secrets.token_hex(24)
    env["AKASHA_KEY_PASSPHRASE"] = secrets.token_hex(24)
    env["AKASHA_TEST_SECRET"] = "fixture-" + secrets.token_hex(24)
    a, b = root / "device-a", root / "device-b"
    source, target = root / "source-a", root / "source-b"
    source.mkdir()
    target.mkdir()
    project = "smoke-" + uuid.uuid4().hex[:12]
    checks = []

    def cli(data, command, *flags, stdin=None, allow_fail=False, actor_env=None):
        result = subprocess.run([args.exe, command, "--data", str(data), *map(str, flags)], input=stdin, capture_output=True, text=True, encoding="utf-8", env=actor_env or env, timeout=90)
        if allow_fail:
            return result
        if result.returncode:
            raise RuntimeError(f"{command}: {result.stderr}")
        return json.loads(result.stdout)

    hub = None
    if not args.external_hub:
        hub = subprocess.Popen([args.exe, "hub", "--data", str(root / "hub"), "--listen", "127.0.0.1:17342"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        for attempt in range(50):
            try:
                with urllib.request.urlopen(args.hub_url + "/healthz", timeout=1) as response:
                    assert json.load(response)["status"] == "ok"
                break
            except OSError:
                if attempt == 49:
                    raise
                time.sleep(.1)
        cli(a, "init")
        cli(a, "space-add", "--id", project, "--name", "Smoke test")
        cli(a, "project-add", "--id", project, "--space", project, "--name", "Round trip", "--root", source)
        code = b"package fixture\nfunc RetryDelay() int { return 30 }\n"
        (source / "queue.go").write_bytes(code)
        (source / ".env").write_text("password=excluded-fixture", encoding="utf-8")
        subprocess.run(["git", "init", "--quiet", str(source)], check=True, capture_output=True)
        subprocess.run(["git", "-C", str(source), "add", "queue.go"], check=True, capture_output=True)
        subprocess.run(["git", "-C", str(source), "-c", "user.name=Akasha fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "Isolated fixture"], check=True, capture_output=True)
        imported = cli(a, "import", "--project", project)
        assert imported["files"] == 1
        assert cli(a, "import", "--project", project)["changed"] == 0
        checks.append("source import + unchanged delta + .env exclusion")
        note = cli(a, "remember", "--project", project, "--entity", "retry", "--title", "Retry policy", "--text", "Retry runs after 30 seconds.")
        secret = cli(a, "secret-set", "--project", project, "--name", "fixture credential", "--secret-env", "AKASHA_TEST_SECRET")
        cli(a, "capture", "--project", project, "--git")
        keyfile = root / "transfer.akasha-key"
        cli(a, "key-export", "--project", project, "--out", keyfile)
        cli(a, "sync-enroll", "--project", project, "--url", args.hub_url)
        first = cli(a, "sync", "--project", project, "--url", args.hub_url)
        cli(b, "sync-clone", "--project", project, "--root", target, "--file", keyfile, "--url", args.hub_url)
        cli(b, "restore", "--project", project, "--apply")
        assert (target / "queue.go").read_bytes() == code and not (target / ".env").exists()
        clearfile = root / "explicit-secret-export.txt"
        cli(b, "secret-get", "--project", project, "--id", secret["id"], "--out", clearfile)
        assert clearfile.read_text() == env["AKASHA_TEST_SECRET"]
        clearfile.unlink()  # only our explicitly generated disposable fixture
        bundle = root / "restored.bundle"
        cli(b, "git-export", "--project", project, "--out", bundle)
        subprocess.run(["git", "-C", str(source), "bundle", "verify", str(bundle)], check=True, capture_output=True)
        checks.append("encrypted source + vault + Git bundle on independent device")
        cli(a, "remember", "--project", project, "--entity", "retry", "--title", "Retry policy", "--text", "Device A: 40 seconds", "--expected", note["id"])
        cli(b, "remember", "--project", project, "--entity", "retry", "--title", "Retry policy", "--text", "Device B: 50 seconds", "--expected", note["id"])
        cli(a, "sync", "--project", project, "--url", args.hub_url)
        cli(b, "sync", "--project", project, "--url", args.hub_url)
        cli(a, "sync", "--project", project, "--url", args.hub_url)
        conflicts_a = cli(a, "conflicts", "--project", project)
        conflicts_b = cli(b, "conflicts", "--project", project)
        assert conflicts_a == conflicts_b and len(conflicts_a) == 1 and len(conflicts_a[0]["versions"]) == 2
        checks.append("offline concurrent edits converge without losing either version")
        reader = cli(a, "hub-user-add", "--name", "Fixture reader", "--url", args.hub_url)
        reader_env = dict(env, AKASHA_HUB_TOKEN=reader["token"])
        cli(a, "hub-grant", "--project", project, "--user", reader["user"]["id"], "--role", "reader", "--url", args.hub_url)
        cli(b, "sync-pull", "--project", project, "--url", args.hub_url, actor_env=reader_env)
        cli(b, "remember", "--project", project, "--entity", "unpublished", "--title", "Local pending change", "--text", "Reader must not upload this fixture.")
        denied = cli(b, "sync", "--project", project, "--url", args.hub_url, actor_env=reader_env, allow_fail=True)
        assert denied.returncode != 0
        cli(a, "hub-grant", "--project", project, "--user", reader["user"]["id"], "--role", "", "--url", args.hub_url)
        assert cli(b, "sync-pull", "--project", project, "--url", args.hub_url, actor_env=reader_env, allow_fail=True).returncode != 0
        checks.append("hub reader authorization, denied write, revoked read")
        local_reader = cli(a,"user-add","--name","Local MCP reader")
        cli(a,"grant","--project",project,"--user",local_reader["user"]["id"],"--role","reader")
        mcp_env = dict(env, AKASHA_TEST_READER=local_reader["token"])
        proc = subprocess.Popen([args.exe, "mcp", "--data", str(a), "--project", project,"--token-env","AKASHA_TEST_READER"], env=mcp_env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8")
        messages = queue.Queue()
        def consume():
            for line in proc.stdout:
                messages.put(json.loads(line))
        thread = threading.Thread(target=consume, daemon=True)
        thread.start()
        def rpc(ident, method, params):
            proc.stdin.write(json.dumps({"jsonrpc":"2.0","id":ident,"method":method,"params":params}) + "\n")
            proc.stdin.flush()
            while True:
                msg = messages.get(timeout=30)
                if msg.get("id") == ident:
                    assert "error" not in msg, msg
                    return msg["result"]
        try:
            rpc(1, "initialize", {"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"akasha-smoke","version":"1"}})
            proc.stdin.write('{"jsonrpc":"2.0","method":"notifications/initialized"}\n')
            proc.stdin.flush()
            assert len(rpc(2,"tools/list",{})["tools"]) == 9
            packet = rpc(3,"tools/call",{"name":"memory_context","arguments":{"query":"Retry policy","phase":"debug","budget":512,"shared":False}})
            assert not packet.get("isError") and packet["structuredContent"]["tokens"] <= 512
            assert "40 seconds" in packet["content"][0]["text"] and "50 seconds" in packet["content"][0]["text"]
            assert env["AKASHA_TEST_SECRET"] not in json.dumps(packet)
            denied = rpc(4,"tools/call",{"name":"memory_write","arguments":{"entity":"denied","kind":"note","title":"Denied","text":"Reader cannot write"}})
            assert denied.get("isError")
        finally:
            proc.stdin.close()
            proc.wait(timeout=20)
            assert proc.returncode == 0, proc.stderr.read()
        checks.append("native MCP stdio reader startup/list/context + budget + secret exclusion + denied write")
        hook = {"hook_event_name":"UserPromptSubmit","session_id":"fixture","turn_id":"1","cwd":str(source),"prompt":"Retry policy password=drop-this-value"}
        result = cli(a,"hook","--project",project,"--budget","512",stdin=json.dumps(hook))
        assert result["hookSpecificOutput"]["additionalContext"]
        hook.update(hook_event_name="Stop",last_assistant_message="Validated both alternatives; waiting for evidence.")
        result = cli(a,"hook","--project",project,stdin=json.dumps(hook))
        assert result["continue"] and "hookSpecificOutput" not in result
        assert "drop-this-value" not in json.dumps(cli(a,"prompt-history","--space",project))
        checks.append("real stdin/stdout Codex hooks, prompt redaction and Stop checkpoint")
        plugin = root / "plugin"
        cli(a,"integration","--project",project,"--out",plugin)
        assert json.loads((plugin / "plugin.json").read_text())["name"] == "akasha-memory"
        checks.append("generated project-bound plugin and configuration")
        report = {"status":"passed","external_hub":args.external_hub,"project":project,"fixture_data":str(root),"checks":checks,"checks_passed":len(checks),"binary_sha256":hashlib.sha256(Path(args.exe).read_bytes()).hexdigest(),"first_sync":first}
        (ROOT / args.out).write_text(json.dumps(report,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
        print(json.dumps(report,ensure_ascii=False,indent=2))
    finally:
        if hub:
            hub.terminate()
            hub.wait(timeout=15)


if __name__ == "__main__":
    main()
