import argparse,json,re
from pathlib import Path
ROOT=Path(__file__).resolve().parent
p=argparse.ArgumentParser();p.add_argument('--start',type=int,default=0);p.add_argument('--count',type=int,default=10);p.add_argument('--kind',default='repository');p.add_argument('--name');a=p.parse_args()
rows={r['name']:r for r in (json.loads(x) for x in (ROOT/'deep_sources.jsonl').read_text(encoding='utf8').split('\n') if x) if r.get('status')==200}
rows=[r for r in rows.values() if r['kind']==a.kind and (not a.name or a.name in r['name'])]
for r in rows[a.start:a.start+a.count]:
 text=(ROOT/'raw'/'deep'/(r['sha256']+'.txt')).read_text(encoding='utf8')
 print('\n### '+r['name']+' | '+r['url']+' | '+str(len(text))+' chars')
 if a.kind=='repository':
  lines=[x for x in text.splitlines() if 70<len(x)<1100 and not x.startswith(('|','[!','<','![','http')) and re.search(r'prompt|skill|workflow|compil|constraint|evaluat|agent|language|provenance',x,re.I)]
  print('\n'.join(lines[:5])[:1750])
 else:
  text=re.sub(r'\n{2,}','\n',text)
  for term in ('Abstract','algorithm','evaluation','limitation'):
   match=re.search(term,text,re.I)
   if match:print('['+term+'] '+re.sub(r'\s+',' ',text[max(0,match.start()-100):match.start()+900]))
