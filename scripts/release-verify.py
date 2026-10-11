"""Check packaged bytes/CRC and run the delivered Windows/Linux binaries."""
import hashlib
import json
from pathlib import Path
import subprocess
import uuid
import zipfile

ROOT=Path(__file__).resolve().parents[1]
out=ROOT/'.local'/('release-check-'+uuid.uuid4().hex[:10])
out.mkdir(parents=True)
checks=[]
for platform in ['windows-amd64','linux-amd64','source']:
    path=ROOT/'dist'/('akasha-0.2.0-'+platform+'.zip')
    with zipfile.ZipFile(path) as archive:
        assert archive.testzip() is None
        names=archive.namelist()
        assert all(not n.startswith(('/','.local/','research/raw/')) and '..' not in Path(n).parts for n in names)
        if platform=='source':
            for required in ['compose.yaml','migrations/versions/0001_accounts_timescale.py','internal/skillrepo/generate.go','internal/core/vector.go','third_party/onnxer/AKASHA_PATCHES.md','go.mod']:
                assert required in names,required
            assert '.env' not in names and not any(n.endswith(('.db','.token','.akasha-key')) for n in names)
        else:
            entry='akasha.exe' if platform.startswith('windows') else 'akasha'
            expected=ROOT/('bin/akasha.exe' if platform.startswith('windows') else 'dist/akasha-linux-amd64')
            data=archive.read(entry)
            assert hashlib.sha256(data).digest()==hashlib.sha256(expected.read_bytes()).digest()
            if platform.startswith('windows'):
                (out/entry).write_bytes(data)
        checks.append({'archive':path.name,'sha256':hashlib.sha256(path.read_bytes()).hexdigest(),'bytes':path.stat().st_size,'entries':len(names)})
exe=str(out/'akasha.exe')
assert subprocess.check_output([exe,'version'],text=True).strip()=='Akasha 0.2.0'
def cli(*args):
    return json.loads(subprocess.check_output([exe,args[0],'--data',str(out/'data'),*args[1:]],encoding='utf-8'))
cli('space-add','--id','release','--name','Release fixture')
project=cli('project-add','--space','release','--name','Release project')['id']
cli('remember','--project',project,'--title','Package verification','--text','Release verification preserves scoped knowledge.')
context=cli('context','--project',project,'--query','Package verification','--budget','256')
assert context['tokens']<=256 and len(context['selected'])==1
linux=subprocess.check_output(['docker','run','--rm','--name','akasha-release-'+uuid.uuid4().hex[:10],
    '--network','none','--cpus','1','--memory','128m','--mount',f'type=bind,source={ROOT / "dist/akasha-linux-amd64"},target=/opt/akasha-release,readonly',
    '--entrypoint','/opt/akasha-release','akasha:0.2.0','version'],text=True).strip()
assert linux=='Akasha 0.2.0'
report={'passed':True,'archives':checks,'windows_extracted_binary':'version + isolated write/search/context budget roundtrip','linux_exported_binary':'actual executable mounted read-only, network none','fixture':str(out)}
(ROOT/'reports/release-verification.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
