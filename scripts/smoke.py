"""Critical stdio contract smoke checks; optional read-only live discovery."""
import argparse,json,subprocess,threading,queue
p=argparse.ArgumentParser();p.add_argument('--connection');p.add_argument('--server',default='./amp-mcp.exe');p.add_argument('--expected-version');a=p.parse_args()
proc=subprocess.Popen([a.server,'serve'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,encoding='utf-8')
q=queue.Queue()
def reader():
 for line in proc.stdout:q.put(line)
threading.Thread(target=reader,daemon=True).start()
def request(n,method,params):
 proc.stdin.write(json.dumps({'jsonrpc':'2.0','id':n,'method':method,'params':params})+'\n');proc.stdin.flush()
 while True:
  r=json.loads(q.get(timeout=45))
  if r.get('id')==n:return r
try:
 r=request(1,'initialize',{'protocolVersion':'2025-11-25','capabilities':{},'clientInfo':{'name':'smoke','version':'1'}});assert 'result' in r,r
 if a.expected_version:assert r['result']['serverInfo']['version']==a.expected_version,r
 proc.stdin.write(json.dumps({'jsonrpc':'2.0','method':'notifications/initialized'})+'\n');proc.stdin.flush()
 r=request(2,'tools/list',{});tools=r['result']['tools'];assert len(tools)>20
 assert all('outputSchema' not in t for t in tools)
 r=request(3,'tools/call',{'name':'amp_instance','arguments':{'operation':'start'}});assert r.get('error') or r.get('result',{}).get('isError'),r
 r=request(4,'tools/call',{'name':'amp_list_connections','arguments':{}});assert 'structuredContent' not in r.get('result',{})
 print('PASS: handshake, tools/list, validation, content-only results; tools='+str(len(tools)))
 if a.connection:
  for i,(name,op) in enumerate([('amp_test_connection',None),('amp_instance_read','list'),('amp_application_read','tasks'),('amp_setting_read','list'),('amp_job_read','list'),('amp_access_read','sessions')],5):
   args={'connection':a.connection}
   if op:args['operation']=op
   if op=='list' and name=='amp_instance_read':args['include_self']=True
   r=request(i,'tools/call',{'name':name,'arguments':args});assert 'result' in r and not r['result'].get('isError'),r
   val=json.loads(r['result']['content'][0]['text'])
   if name=='amp_instance_read':
    assert isinstance(val,dict) and isinstance(val['items'],list)
    items=val['items']
    print('PASS: live discovery; instances='+str(len(items)))
    if items:print('Instance field names: '+','.join(items[0].keys()))
    ads=next((v for v in items if str(v.get('InstanceName','')).startswith('ADS') and v.get('Running')),None)
   elif name=='amp_test_connection':print('PASS: credential decryption, HTTPS trust and AMP authentication')
   else:print('PASS: read-only '+name+' '+op)
  if ads:
   r=request(20,'tools/call',{'name':'amp_application_read','arguments':{'operation':'info','connection':a.connection,'instance_id':ads['InstanceID']}})
   assert 'result' in r and not r['result'].get('isError'),r
   print('PASS: ADS proxy authentication and scoped module read')
   r=request(21,'tools/call',{'name':'amp_module_action_read','arguments':{'operation':'list','connection':a.connection,'instance_id':ads['InstanceID']}})
   assert 'result' in r and not r['result'].get('isError'),r
   print('PASS: authenticated module-action discovery (no actions executed)')
finally:
 proc.stdin.close()
 try:proc.wait(timeout=15)
 except subprocess.TimeoutExpired:proc.kill()
