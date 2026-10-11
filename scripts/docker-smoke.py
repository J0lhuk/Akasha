"""Run only an isolated Akasha test container; leave other services untouched."""
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import uuid
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
name = "akasha-smoke-" + uuid.uuid4().hex[:12]
data = ROOT / ".local" / name
data.mkdir(parents=True)
env = dict(os.environ, AKASHA_HUB_TOKEN=secrets.token_hex(32))
with socket.socket() as probe:
    probe.bind(("127.0.0.1", 0))
    port = probe.getsockname()[1]
created = False
try:
    subprocess.run(["docker", "run", "-d", "--name", name, "--cpus", "2", "--memory", "512m", "--pids-limit", "100", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--env", "AKASHA_HUB_TOKEN", "--publish", f"127.0.0.1:{port}:7342", "--mount", f"type=bind,source={data},target=/data", "akasha:0.1.0"], check=True, env=env, capture_output=True)
    created = True
    subprocess.run(["python", str(ROOT / "scripts" / "smoke.py"), "--external-hub", "--hub-url", f"http://127.0.0.1:{port}", "--out", "reports/docker-smoke.json"], check=True, env=env)
    before = json.loads((ROOT / "reports/docker-smoke.json").read_text())
    # Restart exactly this fixture hub and prove its catalog survived the restart.
    subprocess.run(["docker", "restart", name], check=True, capture_output=True)
    for attempt in range(50):
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz",timeout=1) as response:
                assert json.load(response)["status"] == "ok"
            break
        except OSError:
            if attempt == 49:
                raise
            time.sleep(.1)
    proc = subprocess.run([str(ROOT / "bin/akasha.exe"), "sync-catalog", "--data", str(Path(before["fixture_data"]) / "device-a"), "--url", f"http://127.0.0.1:{port}"], env=env, check=True, capture_output=True, text=True, encoding="utf-8")
    assert any(item["project"]["id"] == before["project"] for item in json.loads(proc.stdout))
    before["restart_persistence"] = True
    before["container"] = name
    before["hub_data"] = str(data)
    before["image_id"] = subprocess.run(["docker", "image", "inspect", "akasha:0.1.0", "--format", "{{.Id}}"], check=True, capture_output=True, text=True).stdout.strip()
    (ROOT / "reports/docker-smoke.json").write_text(json.dumps(before, ensure_ascii=False, indent=2)+"\n",encoding="utf-8")
    print("Docker hub restart persistence: passed")
finally:
    if created:
        logs = subprocess.run(["docker", "logs", name], capture_output=True, text=True)
        (ROOT / "reports/docker-runtime.log").write_text(logs.stdout+logs.stderr,encoding="utf-8")
        subprocess.run(["docker", "rm", "-f", name], check=True, capture_output=True)
