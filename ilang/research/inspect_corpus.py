"""Compact progress / review cards; ignore only an incomplete live JSONL tail."""
import argparse, collections, json, re
from pathlib import Path
ROOT=Path(__file__).resolve().parent
def read(name):
 p=ROOT/name
 if not p.exists():return []
 data=p.read_text(encoding='utf8')
 lines=data.split('\n')
 if data and not data.endswith('\n'):lines=lines[:-1]
 return [json.loads(x) for x in lines if x]
p=argparse.ArgumentParser();p.add_argument('--start',type=int);p.add_argument('--count',type=int,default=50);p.add_argument('--family');p.add_argument('--github',action='store_true');a=p.parse_args()
g=[r for r in read('github_pages.jsonl') if r.get('status')==200 and r.get('items') and not r.get('incomplete_results')]
w=read('web_sources.jsonl');good=[r for r in w if r.get('accepted')]
if a.github:
 for i,r in enumerate(g[a.start or 0:(a.start or 0)+a.count],1+(a.start or 0)):
  print(f"G{i:03d} {r['query']} page {r['page']} | "+'; '.join(x['name']+': '+(x.get('description') or '')[:65] for x in r['items'][:3]))
elif a.start is not None or a.family:
 rows=[r for r in good if not a.family or r['family']==a.family]
 for i,r in enumerate(rows[a.start or 0:(a.start or 0)+a.count],1+(a.start or 0)):
  evidence=next((x for x in r.get('evidence',[]) if re.search(r'[.!?:]',x)), ' '.join(r.get('headings',[])[:5]))
  print(f"W{i:03d} {r['family']} | {r['title']}\n  "+evidence[:240])
else:
 print(json.dumps({'github':len(g),'web_fetched':len(w),'screened':len(good),'families':collections.Counter(r['family'] for r in good),'exclusions':collections.Counter(r.get('exclusion') for r in w if not r.get('accepted')).most_common(8)},ensure_ascii=False,indent=2))
