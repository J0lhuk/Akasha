"""Selected author repositories and papers for close reading; separate from page gates."""
import concurrent.futures,hashlib,json,re,time
from pathlib import Path
from datetime import datetime,timezone
import requests
from bs4 import BeautifulSoup
ROOT=Path(__file__).resolve().parent
RAW=ROOT/'raw'/'deep';RAW.mkdir(parents=True,exist_ok=True)
REPOS=['stanfordnlp/dspy','eth-sri/lmql','guidance-ai/guidance','dottxt-ai/outlines','BoundaryML/baml','meta-prompting/meta-prompting','suzgunmirac/meta-prompting','gepa-ai/gepa','zou-group/textgrad','microsoft/Trace','keirp/automatic_prompt_engineer','google-deepmind/opro','FoundationAgents/MetaGPT','langchain-ai/langgraph','microsoft/semantic-kernel','pydantic/pydantic-ai','microsoft/autogen','crewAIInc/crewAI','anthropics/skills','agentskills/agentskills','github/spec-kit','Fission-AI/OpenSpec','ShengranHu/ADAS','MineDojo/Voyager','noahshinn/reflexion','madaan/self-refine','princeton-nlp/tree-of-thought-llm','spcl/graph-of-thoughts','PrincetonUniversity/ILAng','ilang-ai/ilang-spec','ilang-ai/ilang.ai','cue-lang/cue','dhall-lang/dhall-lang','nickel-lang/nickel','kcl-lang/kcl','eclipse-langium/langium','textX/textX','modelcontextprotocol/modelcontextprotocol','a2aproject/A2A','aiplan4eu/unified-planning','temporalio/temporal','paralleldrive/sudolang','microsoft/poml','microsoft/prompty','langgptai/LangGPT','gsd-build/get-shit-done','microsoft/sammo','ianarawjo/ChainForge','microsoft/aici','obra/superpowers','refly-ai/refly','opensquilla/opensquilla','victorzhong0110/skill-evolution','patrick-yip/meta-skills','riktar/slang','fischerf/relatelang','Nosename/ACE']
PAPERS=['2311.11482','2401.12954','2211.01910','2310.03714','2406.07496','2212.06094','2408.08435','2510.04618','2305.16291','2507.19457','2607.20668','2309.03409','2303.17651','2303.11366','2305.10601','2308.09687','2210.03629','2308.00352','2605.10500','2605.23904','2608.07449','2608.17684']
def repo(name):
 for branch in ('main','master'):
  for file in ('README.md','README.rst'):
   url=f'https://raw.githubusercontent.com/{name}/{branch}/{file}'
   try:
    r=requests.get(url,timeout=(8,16),headers={'User-Agent':'AkashaIlangResearch/0.1'})
    if r.status_code!=200:continue
    text=r.text;sha=hashlib.sha256(text.encode()).hexdigest();(RAW/(sha+'.txt')).write_text(text,encoding='utf8')
    return {'kind':'repository','name':name,'url':url,'status':200,'fetched_at':datetime.now(timezone.utc).isoformat(),'sha256':sha,'characters':len(text),'headings':re.findall(r'^#{1,3} (.+)$',text,re.M)[:32]}
   except requests.RequestException:pass
 return {'kind':'repository','name':name,'status':0}
def paper(pid):
 # arXiv is serialized with a 3-second pause. HTML full text, then abstract if unavailable.
 for form in ('html','abs'):
  url=f'https://arxiv.org/{form}/{pid}'
  try:
   r=requests.get(url,timeout=(8,25),headers={'User-Agent':'AkashaIlangResearch/0.1'})
   time.sleep(3)
   if r.status_code!=200:continue
   soup=BeautifulSoup(r.content,'lxml')
   for tag in soup.select('script,style,nav,footer,header'):tag.decompose()
   text=(soup.select_one('article') or soup.select_one('main') or soup).get_text('\n',strip=True)
   sha=hashlib.sha256(text.encode()).hexdigest();(RAW/(sha+'.txt')).write_text(text,encoding='utf8')
   return {'kind':'paper','name':pid,'url':url,'readable_form':form,'status':200,'fetched_at':datetime.now(timezone.utc).isoformat(),'sha256':sha,'characters':len(text),'title':soup.title.get_text(' ',strip=True) if soup.title else pid}
  except requests.RequestException:pass
 return {'kind':'paper','name':pid,'status':0}
if __name__=='__main__':
 log=ROOT/'deep_sources.jsonl'
 existing=[json.loads(x) for x in log.read_text(encoding='utf8').split('\n') if x] if log.exists() else []
 done={r['name'] for r in existing if r['status']==200}
 with log.open('a',encoding='utf8') as f:
  with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
   for r in pool.map(repo,[x for x in REPOS if x not in done]):f.write(json.dumps(r,ensure_ascii=False)+'\n');f.flush();print(r['name'],r['status'],flush=True)
  for pid in PAPERS:
   if pid not in done:
    r=paper(pid);f.write(json.dumps(r,ensure_ascii=False)+'\n');f.flush();print(r['name'],r['status'],r.get('readable_form'),flush=True)
