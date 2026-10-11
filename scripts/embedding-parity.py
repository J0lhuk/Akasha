"""Independent native Go/Python ONNX parity check; no networking or downloads."""
import argparse
import json
import subprocess
from pathlib import Path
import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

p=argparse.ArgumentParser()
p.add_argument('--data',default='.local/vector-data')
p.add_argument('--exe',default='.local/akasha-next.exe')
p.add_argument('--out',default='reports/embedding-parity.json')
a=p.parse_args()
folder=next((Path(a.data)/'models').iterdir())
tokenizer=Tokenizer.from_file(str(folder/'tokenizer.json'))
tokenizer.enable_truncation(256)
options=ort.SessionOptions();options.intra_op_num_threads=2;options.inter_op_num_threads=1
session=ort.InferenceSession(str(folder/'model.onnx'),sess_options=options,providers=['CPUExecutionProvider'])
results=[]
for text in ['Повторное списание денег за один заказ','Защита учётных данных от перебора с дорогими вычислениями','Reject edits made from an outdated document revision']:
    enc=tokenizer.encode('query: '+text)
    mapping={'input_ids':np.array([enc.ids],dtype=np.int64),'attention_mask':np.array([enc.attention_mask],dtype=np.int64),'token_type_ids':np.array([enc.type_ids],dtype=np.int64)}
    hidden=session.run(None,{i.name:mapping[i.name] for i in session.get_inputs()})[0]
    mask=mapping['attention_mask'][:,:,None]
    vector=(hidden*mask).sum(axis=1)/mask.sum(axis=1)
    vector=vector[0]/np.linalg.norm(vector[0])
    go=json.loads(subprocess.check_output([a.exe,'embed','--data',a.data,'--text',text],encoding='utf-8'))
    difference=float(np.max(np.abs(vector-np.array(go['vector']))))
    results.append({'text':text,'tokens':len(enc.ids),'active_tokens':sum(enc.attention_mask),'max_abs_error':difference})
report={'python_ort':ort.__version__,'checks':results,'passed':all(r['max_abs_error']<0.0001 for r in results)}
Path(a.out).write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
assert report['passed']
