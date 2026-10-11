"""Assemble local release archives and dependency notices, without publication."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DIST = ROOT / "dist"
DIST.mkdir(exist_ok=True)
VERSION = '0.2.0'
raw = subprocess.run(["go","list","-m","-json","all"],cwd=ROOT,check=True,capture_output=True,text=True).stdout
decoder=json.JSONDecoder()
modules=[]
while raw.strip():
    raw=raw.lstrip()
    module,offset=decoder.raw_decode(raw)
    modules.append(module)
    raw=raw[offset:]
notices=["# Уведомления о сторонних компонентах", "", "Лицензии зависимостей из go.mod/go.sum приложены без изменения. Исследовательские материалы не являются кодовыми зависимостями.", ""]
license_files=[]
for module in modules:
    if module.get("Main") or not module.get("Dir"):
        continue
    module_dir=Path(module["Dir"])
    found=sorted(p for p in module_dir.iterdir() if p.is_file() and p.name.upper().startswith(("LICENSE","COPYING","NOTICE")))
    for source in found:
        relative=Path("licenses") / (module["Path"].replace("/","_")+"@"+module.get("Version","")) / source.name
        destination=ROOT / relative
        destination.parent.mkdir(parents=True,exist_ok=True)
        shutil.copyfile(source,destination)
        license_files.append(destination)
    notices.append(f'- `{module["Path"]}@{module.get("Version", "")}` — '+(", ".join(p.name for p in found) or "See upstream distribution for notices."))
goroot=Path(subprocess.run(["go","env","GOROOT"],check=True,capture_output=True,text=True).stdout.strip())
go_license=ROOT / "licenses" / "go" / "LICENSE"
go_license.parent.mkdir(parents=True,exist_ok=True)
shutil.copyfile(goroot / "LICENSE",go_license)
license_files.append(go_license)
notices.extend(["", "Go runtime: licenses/go/LICENSE.", "onnxer uses the included MIT-licensed local compatibility fork; see third_party/onnxer/AKASHA_PATCHES.md in the source distribution.", "Native E5/ONNX/tokenizer downloads are optional, pinned separately by model-install; their upstream licenses accompany the installation. Model weights are not included in these archives.", ""])
(ROOT / "THIRD_PARTY_NOTICES.md").write_text("\n".join(notices),encoding="utf-8")
common=[ROOT/"README.md",ROOT/"THIRD_PARTY_NOTICES.md",*sorted((ROOT/"docs").glob("*.md")),*sorted((ROOT/"reports").glob("ui-*.jpg")),*sorted((ROOT/"reports").glob("*.json")),*sorted((ROOT/"reports").glob("*.md")),*license_files]
common.extend(ROOT/"research"/n for n in ["RESEARCH_NOTES.md","METHOD.md","summary.json","CATALOG.md","GITHUB_SEARCH.md"])
common.extend(sorted((ROOT/'reports').glob('ui-*.png')))
common.extend(sorted((ROOT/'reports').glob('vector-*.txt')))
common.extend(ROOT/'reports'/name for name in ['go-test.jsonl','coverage.out','linux-race.log','build-linux.log'])
for platform,binary in [("windows-amd64",ROOT/"bin/akasha.exe"),("linux-amd64",DIST/"akasha-linux-amd64")]:
    if not binary.exists():
        raise RuntimeError(f"Build missing: {binary}")
    archive=DIST/f"akasha-{VERSION}-{platform}.zip"
    with zipfile.ZipFile(archive,"w",zipfile.ZIP_DEFLATED) as out:
        out.write(binary,"akasha.exe" if platform.startswith("windows") else "akasha")
        for path in common:
            out.write(path,path.relative_to(ROOT).as_posix())
source_files = set(common)
for directory in ['cmd','internal','third_party','migrations','scripts','testdata']:
    for path in (ROOT/directory).rglob('*'):
        if path.is_file() and '__pycache__' not in path.parts and path.suffix not in ['.pyc','.pyo']:
            source_files.add(path)
source_files.update(ROOT/name for name in ['AGENTS.md','.gitignore','.dockerignore','.env.example','Dockerfile','compose.yaml','go.mod','go.sum'])
with zipfile.ZipFile(DIST/f'akasha-{VERSION}-source.zip','w',zipfile.ZIP_DEFLATED) as out:
    for path in sorted(source_files):
        relative=path.relative_to(ROOT)
        assert relative.parts[0] not in ['.git','.local','bin','dist'] and path.name!='.env'
        out.write(path,relative.as_posix())
checksums=[]
for path in sorted(DIST.iterdir()):
    if path.is_file() and path.name!="SHA256SUMS":
        checksums.append(hashlib.sha256(path.read_bytes()).hexdigest()+"  "+path.name)
(DIST/"SHA256SUMS").write_text("\n".join(checksums)+"\n",encoding="utf-8")
print(json.dumps({"archives":[str(p) for p in DIST.glob("*.zip")],"license_files":len(license_files)},ensure_ascii=False))
