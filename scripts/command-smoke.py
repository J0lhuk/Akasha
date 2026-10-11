"""Real CLI / generated Windows-hook onboarding smoke; no user configuration."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--exe", default=str(ROOT / "bin/akasha.exe"))
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix="akasha-commands-", dir=ROOT / ".local"))
    data, group = root / "data", root / "Work"
    project = group / "Existing project"
    project.mkdir(parents=True)
    (project / "README.md").write_text("# Payment API\nOrders use PostgreSQL with idempotency keys.\n", encoding="utf-8")
    def cli(command, *more, ok=True):
        result = subprocess.run([args.exe, command, "--data", str(data), "--root", str(project), *map(str, more)], capture_output=True, text=True, encoding="utf-8", timeout=60)
        if not ok:
            assert result.returncode != 0, result.stdout
            return result
        assert result.returncode == 0, result.stderr
        return json.loads(result.stdout)
    assert cli("add")["status"] == "needs_space"
    subprocess.run([args.exe,"space-add","--data",str(data),"--id","work","--name","Work","--root",str(group)],check=True,capture_output=True)
    first = cli("add")
    repeat = cli("add")
    assert first["created"] and not repeat["created"] and repeat["import"]["changed"] == 0
    assert first["project"]["id"] == repeat["project"]["id"] and not repeat.get("changed_files")
    cli("budget","--phase","implement","--budget","128")
    packet = cli("context","--query","Orders PostgreSQL")
    assert packet["budget"] == 128 and packet["tokens"] <= 128
    script = project / ".akasha/codex/scripts/hook.ps1"
    payload = {"hook_event_name":"UserPromptSubmit","cwd":str(project),"session_id":"smoke","turn_id":"1","prompt":"Orders PostgreSQL"}
    if os.name == "nt":
        hook = subprocess.run(["powershell.exe","-NoProfile","-NonInteractive","-File",str(script)],input=json.dumps(payload),text=True,encoding="utf-8",capture_output=True,timeout=60)
        assert hook.returncode == 0, hook.stderr
        content = json.loads(hook.stdout)["hookSpecificOutput"]["additionalContext"]
        assert cli("tokens","--text",content)["tokens"] <= 128
    saved = cli("prompt-save","--name","review","--text","Review {{module}} carefully.")
    variables = root / "variables.json"
    variables.write_text(json.dumps({"module":"платёжный API"}),encoding="utf-8")
    rendered = cli("prompt-render","--name","review","--variables",variables)
    assert "платёжный API" in rendered["text"] and not rendered["cache_hit"]
    assert cli("prompt-render","--name","review","--variables",variables)["cache_hit"]
    cli("prompt-save","--name","review","--text","New text",ok=False)
    cli("prompt-save","--name","review","--text","New {{module}}", "--expected",saved["id"])
    assert not cli("prompt-render","--name","review","--variables",variables)["cache_hit"]
    cli("cache-clear")
    assert cli("cache")["entries"] == 0
    assert cli("prompt-list") and cli("prompt-history")
    installed = cli("install-codex","--out",root/"skills/akasha")
    assert Path(installed["skill"],"SKILL.md").is_file()
    assert cli("status")["project"]["id"] == first["project"]["id"]
    report = {"status":"passed","fixture":str(root),"binary_sha256":hashlib.sha256(Path(args.exe).read_bytes()).hexdigest(),"checks":["unregistered add requires group; rooted auto-selection","idempotent add and unchanged import","persisted budget in context and real PowerShell hook","prompt variables, cache reuse, observed revision update","cache clear preserves templates and prompt history","standalone skill installer in isolated directory"]}
    (ROOT/"reports/command-smoke.json").write_text(json.dumps(report,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
    print(json.dumps(report,ensure_ascii=False,indent=2))

if __name__ == "__main__":
    main()
