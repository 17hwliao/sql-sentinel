import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {mkdtemp,writeFile,rm,readFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {provider,TestStore,bind,cookies} from './fixtures/identity.mjs';
import {createApp,validateConfig} from '../server.mjs';
import product from '../product.json' with {type:'json'};

const mesh=product.id==='agentmesh';
async function fixture(t){
 const dir=await mkdtemp(path.join(os.tmpdir(),'console-test-')),calls=[],idp=await provider(t);const file=n=>path.join(dir,n);
 const f={dir,calls,sessionActive:true,verified:true,banned:false,members:{user_a:{enabled:true,tenant_id:'tenant_a',project_id:'project_a',role:'writer',tables:['orders'],gateway_key_file:file('gateway'),can_manage_keys:true}}};
 f.identity=idp;for(const [field,target]of [['sessionActive','active'],['verified','verified'],['banned','banned']])Object.defineProperty(f,field,{get:()=>idp[target],set:value=>idp[target]=value});
 await writeFile(file('client'),'fixture-client-secret');await writeFile(file('session'),'1'.repeat(64));await writeFile(file('redis'),'redis-fixture');await writeFile(file('admin'),'internal-admin-fixture');await writeFile(file('gateway'),'am_fixture_tenant_a');
 const image=Buffer.from([137,80,78,71,13,10,26,10,0,255,128,1]);
 const backend=http.createServer(async(req,res)=>{const chunks=[];for await(const x of req)chunks.push(x);const raw=Buffer.concat(chunks);calls.push({method:req.method,path:req.url,auth:req.headers.authorization,body:raw.length&&req.headers['content-type']?.startsWith('application/json')?JSON.parse(raw.toString()):null,raw,contentType:req.headers['content-type']});res.setHeader('Content-Type','application/json');
  if(req.url==='/v1/account')return res.end(JSON.stringify({tenant_id:f.wrongTenant?'tenant_b':'tenant_a',enabled:true}));
  if(req.url==='/auth/tokens'){f.issueCount=(f.issueCount||0)+1;const sequence=f.issueCount;await f.beforeIssue?.(sequence);return res.end(JSON.stringify({token:'scoped-backend-token'+(f.uniqueTokens?'_'+sequence:''),token_id:'fixture_scoped_session'+(f.uniqueTokens?'_'+sequence:''),expires_in:3600}));}
  if(f.respond&&await f.respond(req,res))return;
  if(req.url.startsWith('/admin/tenants/'))return res.end(JSON.stringify({data:[{key_id:'owned_key',enabled:true}],next_after:''}));
  if(req.url==='/admin/api-keys')return res.end(JSON.stringify({api_key:'am_new_fixture',key_id:'owned_new'}));
  if(req.url==='/v1/images'&&req.method==='POST')return res.writeHead(201).end(JSON.stringify({id:'img_'+'a'.repeat(32),reference:'agentmesh://images/img_'+'a'.repeat(32)}));
  if(req.url==='/v1/images/img_'+'a'.repeat(32)&&req.method==='GET'){res.setHeader('Content-Type','image/png');return res.end(image);}
  if(req.method==='DELETE'){res.statusCode=204;return res.end();}
  if(req.url==='/v1/chat/completions'){res.setHeader('Content-Type','text/event-stream');res.setHeader('X-AgentMesh-Billing-Request-ID','req_fixture');return res.end('data: {"choices":[{"delta":{"content":"hello"}}]}\n\ndata: [DONE]\n\n');}
  if(req.url.endsWith('/report'))res.setHeader('X-Sentinel-Report-SHA256','a'.repeat(64));
  res.end(JSON.stringify({data:[],ok:true}));
 });
 const backendURL=await bind(backend);
 const config={origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181',backend_url:backendURL,oidc_issuer:idp.issuer,oidc_client_id:'fixture-client',oidc_client_secret_file:file('client'),session_key_file:file('session'),session_redis_url:'redis://127.0.0.1:6379/0',session_redis_password_file:file('redis'),email_delivery:'test_sink',backend_admin_file:file('admin'),members_file:file('members')};
 f.members.user_a.issuer=idp.issuer;
 const save=()=>writeFile(file('members'),JSON.stringify(f.members));await save();f.save=save;
 f.store=new TestStore();const app=createApp(config,{sessionStore:f.store});f.url=await bind(app);f.config=config;f.image=image;
 t.after(async()=>{app.closeAllConnections();backend.closeAllConnections();await Promise.all([new Promise(r=>app.close(r)),new Promise(r=>backend.close(r))]);await rm(dir,{recursive:true,force:true});});
 const logged=await idp.login(f.url);assert.equal(logged.result.status,303);f.cookie=logged.cookie;
 f.csrf=(await (await fetch(f.url+'/api/auth/session',{headers:{Cookie:f.cookie}})).json()).csrf_token;
 f.request=async(p,opts={})=>fetch(f.url+p,{...opts,headers:{Cookie:f.cookie,Origin:config.origin,'X-CSRF-Token':f.csrf,...opts.headers}});
 return f;
}

test('SQL Agent job-id reads and explicit cancellation preserve scoped credentials and CSRF',async t=>{
 if(mesh)return;
 const f=await fixture(t),id='a'.repeat(32);
 const report=await f.request('/api/backend/jobs/by-id/'+id+'/report');assert.equal(report.status,200);assert.equal(report.headers.get('x-sentinel-report-sha256'),'a'.repeat(64));
 assert.equal(f.calls.at(-1).path,'/jobs/by-id/'+id+'/report');assert.equal(f.calls.at(-1).auth,'Bearer scoped-backend-token');
 const before=f.calls.length;
 assert.equal((await fetch(f.url+'/api/backend/jobs/'+id+'/cancel',{method:'POST',headers:{Cookie:f.cookie,Origin:f.config.origin,'Content-Type':'application/json'},body:'{}'})).status,403);assert.equal(f.calls.length,before);
 assert.equal((await f.request('/api/backend/jobs/'+id+'/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status,200);assert.equal(f.calls.at(-1).path,'/jobs/'+id+'/cancel');assert.equal(f.calls.at(-1).auth,'Bearer scoped-backend-token');
 assert.equal((await f.request('/api/backend/jobs/by-id/'+id+'/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status,200);assert.equal(f.calls.at(-1).path,'/jobs/by-id/'+id+'/cancel');
});
test('unconfigured console fails closed while static config remains public',async t=>{const app=createApp({origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181'});const url=await bind(app);t.after(()=>{app.closeAllConnections();app.close();});assert.equal((await fetch(url+'/api/session')).status,503);const data=await (await fetch(url+'/api/config')).json();assert.equal(data.auth_configured,false);assert.equal('clerk_secret_file'in data,false);});
test('same issuer identity is mapped to the current product membership',async t=>{const f=await fixture(t);const r=await f.request('/api/session');assert.equal(r.status,200);const data=await r.json();assert.equal(data.subject,'user_a');assert.equal(data.tenant_id,'tenant_a');assert.equal('gateway_key_file'in data,false);assert.equal(f.calls.length,0);});
test('browser Bearer tokens and forged or duplicate cookies cannot replace a server session',async t=>{const f=await fixture(t);for(const headers of [{Authorization:'Bearer unsigned.invalid.token'},{Cookie:f.cookie.replace(/=.*/, '='+'a'.repeat(43))},{Cookie:f.cookie+'; '+f.cookie}])assert.equal((await fetch(f.url+'/api/session',{headers})).status,401);assert.equal(f.calls.length,0);});
test('unverified email and revoked identity tokens cannot use business APIs',async t=>{const f=await fixture(t);f.verified=false;assert.equal((await f.request('/api/session')).status,403);f.verified=true;f.sessionActive=false;assert.equal((await f.request('/api/backend/'+(mesh?'v1/models':'jobs/job1'))).status,401);assert.equal(f.calls.length,0);});
test('membership removal and disable take effect without restarting',async t=>{const f=await fixture(t);assert.equal((await f.request('/api/session')).status,200);f.members.user_a.enabled=false;await f.save();assert.equal((await f.request('/api/session')).status,403);delete f.members.user_a;await f.save();assert.equal((await f.request('/api/session')).status,403);});
test('foreign origins and arbitrary admin routes are denied',async t=>{const f=await fixture(t);assert.equal((await f.request('/api/session',{headers:{Origin:'https://evil.invalid'}})).status,403);for(const p of ['/api/backend/admin/tenants','/api/backend/auth/tokens','/api/backend/metrics','/api/backend/jobs/job1?tenant_id=other'])assert.equal((await f.request(p)).status,404);assert.equal(f.calls.length,0);});
test('upstream receives only scoped server credential, never browser token',async t=>{const f=await fixture(t);const r=await f.request('/api/backend/'+(mesh?'v1/models':'jobs/job1'));assert.equal(r.status,200);const last=f.calls.at(-1);assert.equal(last.auth,mesh?'Bearer am_fixture_tenant_a':'Bearer scoped-backend-token');if(!mesh){const issue=f.calls.find(x=>x.path==='/auth/tokens');assert.deepEqual(issue.body,{subject:'user_a',tenant_id:'tenant_a',project_id:'project_a',role:'writer',tables:['orders']});}});
test('response association headers survive console proxy',async t=>{const f=await fixture(t);const r=await f.request(mesh?'/api/backend/v1/chat/completions':'/api/backend/jobs/job1/report',mesh?{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'}:{});assert.equal(r.status,200);assert.equal(r.headers.get(mesh?'x-agentmesh-billing-request-id':'x-sentinel-report-sha256'),mesh?'req_fixture':'a'.repeat(64));if(mesh)assert.match(await r.text(),/data: \[DONE\]/);});
test('private image upload and download retain binary bytes through the authenticated console proxy',async t=>{
 if(!mesh)return;const f=await fixture(t),r=await f.request('/api/backend/v1/images',{method:'POST',headers:{'Content-Type':'image/png'},body:f.image});assert.equal(r.status,201);assert.equal((await r.json()).reference,'agentmesh://images/img_'+'a'.repeat(32));
 const upload=f.calls.find(x=>x.path==='/v1/images');assert.deepEqual(upload.raw,f.image);assert.equal(upload.contentType,'image/png');assert.equal(upload.auth,'Bearer am_fixture_tenant_a');
 const image=await f.request('/api/backend/v1/images/img_'+'a'.repeat(32));assert.equal(image.headers.get('content-type'),'image/png');assert.equal(image.headers.get('cache-control'),'no-store');assert.deepEqual(Buffer.from(await image.arrayBuffer()),f.image);
});
test(mesh?'Key creation ignores no browser-supplied tenant and rejects foreign Key revocation':'role changes invalidate cached scoped SQL token',async t=>{const f=await fixture(t);if(mesh){assert.equal((await f.request('/api/keys',{method:'POST',headers:{'Content-Type':'application/json'},body:'{"tenant_id":"tenant_b"}'})).status,400);assert.equal((await f.request('/api/keys',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status,200);assert.deepEqual(f.calls.find(x=>x.path==='/admin/api-keys').body,{tenant_id:'tenant_a'});assert.equal((await f.request('/api/keys/other_key',{method:'DELETE'})).status,404);assert.equal(f.calls.some(x=>x.path==='/admin/api-keys/other_key'),false);assert.equal((await f.request('/api/keys/owned_key',{method:'DELETE'})).status,204);}else{await f.request('/api/backend/jobs/job1');f.members.user_a.role='reader';await f.save();await f.request('/api/backend/jobs/job1');assert.equal(f.calls.filter(x=>x.path==='/auth/tokens').length,2);assert.equal(f.calls.findLast(x=>x.path==='/auth/tokens').body.role,'reader');}});
test(mesh?'misbound gateway credential cannot read another tenant':'malformed role grants no SQL access',async t=>{const f=await fixture(t);if(mesh){f.wrongTenant=true;assert.equal((await f.request('/api/backend/v1/models')).status,403);assert.equal(f.calls.some(x=>x.path==='/v1/models'),false);}else{f.members.user_a.role='superadmin';await f.save();assert.equal((await f.request('/api/session')).status,503);assert.equal(f.calls.length,0);}});
test('configuration refuses legacy identity settings and overlapping console origins',()=>{assert.throws(()=>validateConfig({origin:'https://a.example.com',peer_url:'https://a.example.com'}));assert.throws(()=>validateConfig({origin:'https://a.example.com',peer_url:'https://b.example.com',clerk_issuer:'https://old.invalid'}));});


test('list queries retain scope and reject caller-supplied tenant or unknown parameters',async t=>{
 const f=await fixture(t),base=mesh?'/v1/images':'/jobs';
 assert.equal((await f.request('/api/backend'+base+'?after=cursor1'+(mesh?'':'&limit=25&status=COMPLETED'))).status,200);
 assert.match(f.calls.at(-1).path,/after=cursor1/);
 const before=f.calls.length;
 for(const query of ['tenant_id=other','after=a&after=b','unknown=x'])assert.equal((await f.request('/api/backend'+base+'?'+query)).status,400);
 assert.equal(f.calls.length,before);
});
test('platform admin routes require an explicit server-side grant',async t=>{
 const f=await fixture(t);assert.equal((await f.request('/api/admin/tenants')).status,403);
 f.members.user_a.platform_admin=true;await f.save();
 const response=await f.request('/api/admin/tenants?limit=50');assert.equal(response.status,mesh?200:403);
 if(mesh){assert.equal(f.calls.at(-1).auth,'Bearer internal-admin-fixture');assert.equal((await f.request('/api/admin/billing/credits',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:'tenant_a',credit_id:'stable_credit',proof:'operator_receipt',amount_nano:1000000000})})).status,200);assert.equal(f.calls.at(-1).body.credit_id,'stable_credit');assert.equal((await f.request('/api/admin/auth/tokens')).status,404);}
});
test('scoped SQL session management is admin-only and never exposes bootstrap issuance',async t=>{
 if(mesh)return;const f=await fixture(t);assert.equal((await f.request('/api/backend/auth/sessions')).status,404);
 f.members.user_a.role='admin';await f.save();assert.equal((await f.request('/api/backend/auth/sessions?after=abc')).status,200);assert.equal(f.calls.at(-1).auth,'Bearer scoped-backend-token');assert.equal((await f.request('/api/backend/auth/tokens',{method:'POST',body:'{}'})).status,404);
});
test('production rejects development credentials and placeholder public API URLs',()=>{
 assert.throws(()=>validateConfig({origin:'https://a.example.net',peer_url:'https://b.example.net',production:true}));
 assert.throws(()=>validateConfig({origin:'https://a.example.net',peer_url:'https://b.example.net',public_api_url:'https://api.example.com/v1'}));
 assert.throws(()=>validateConfig({origin:'https://a.example.net',peer_url:'https://b.example.net',public_api_url:'https://api.example.net/v1?token=secret'}));
});

test('Key management requires literal true rather than a truthy misconfigured flag',async t=>{const f=await fixture(t);f.members.user_a.can_manage_keys='false';await f.save();const before=f.calls.filter(x=>x.path==='/admin/api-keys').length;assert.equal((await f.request('/api/keys',{method:'POST',body:'{}'})).status,403);assert.equal(f.calls.filter(x=>x.path==='/admin/api-keys').length,before);});

test('website Agent routes use scoped identity and a closed route/query allowlist',async t=>{
 if(mesh)return;const f=await fixture(t),run='a'.repeat(32);
 for(const p of ['/agent/config','/agent/runs','/agent/runs/'+run,'/agent/runs/by-request/stable1']){
  assert.equal((await f.request('/api/backend'+p)).status,200);
  assert.equal(f.calls.at(-1).auth,'Bearer scoped-backend-token');
 }
 assert.equal((await f.request('/api/backend/agent/runs',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({request_id:'stable1',sql:'SELECT id FROM orders'})})).status,200);
 assert.equal(f.calls.at(-1).body.request_id,'stable1');
 assert.equal((await f.request('/api/backend/agent/runs/'+run+'/cancel',{method:'POST',body:'{}'})).status,200);
 const before=f.calls.length;
 for(const [method,p]of [['POST','/agent/config'],['DELETE','/agent/runs/'+run],['POST','/agent/runs/'+run+'/approval'],['GET','/agent/runs/INVALID'],['GET','/agent/runs/'+run+'/report'],['GET','/agent/runs?tenant_id=other']])assert.equal((await f.request('/api/backend'+p,{method,body:method==='POST'?'{}':undefined})).status,404);
 assert.equal(f.calls.length,before);
});

test('website run writes require CSRF and logout revokes its scoped SQL session',async t=>{
 if(mesh)return;const f=await fixture(t);
 assert.equal((await f.request('/api/backend/agent/runs',{method:'POST',headers:{'X-CSRF-Token':'invalid'},body:'{}'})).status,403);
 assert.equal(f.calls.length,0);
 await f.request('/api/backend/agent/config');
 assert.equal((await f.request('/api/auth/logout',{method:'POST'})).status,204);
 const revoke=f.calls.find(x=>x.path==='/auth/revoke');assert.deepEqual(revoke.body,{token_id:'fixture_scoped_session'});assert.equal(revoke.auth,'Bearer internal-admin-fixture');
});

test('membership removal revokes the cached SQL execution identity before denying access',async t=>{
 if(mesh)return;const f=await fixture(t);await f.request('/api/backend/agent/config');
 f.members.user_a.enabled=false;await f.save();
 assert.equal((await f.request('/api/backend/agent/runs')).status,403);
 assert.equal(f.calls.at(-1).path,'/auth/revoke');
});

test('replicas share encrypted SQL credentials and logout revokes the original execution identity',async t=>{
 if(mesh)return;const f=await fixture(t);await f.request('/api/backend/agent/config');
 const replica=createApp(f.config,{sessionStore:f.store}),url=await bind(replica);
 t.after(async()=>{replica.closeAllConnections();await new Promise(r=>replica.close(r));});
 const headers={Cookie:f.cookie,Origin:f.config.origin,'X-CSRF-Token':f.csrf};
 assert.equal((await fetch(url+'/api/backend/agent/config',{headers})).status,200);
 assert.equal(f.calls.filter(x=>x.path==='/auth/tokens').length,1);
 const stored=[...f.store.rows.entries()].filter(([key])=>key.startsWith('sqlbinding:'));
 assert.equal(stored.length,1);assert.doesNotMatch(stored[0][1].value,/scoped-backend-token|fixture_scoped_session|user_a/);
 f.sessionActive=false;
 assert.equal((await fetch(url+'/api/auth/logout',{method:'POST',headers})).status,204);
 assert.equal(f.calls.filter(x=>x.path==='/auth/revoke').length,1);
 assert.equal((await f.request('/api/backend/agent/runs')).status,401);
});

test('detected identity revocation also revokes its shared SQL session',async t=>{
 if(mesh)return;const f=await fixture(t);await f.request('/api/backend/agent/config');f.sessionActive=false;
 assert.equal((await f.request('/api/auth/session')).status,401);
 assert.equal(f.calls.at(-1).path,'/auth/revoke');
});

test('F1 logout retires a binding before delayed SQL credential publication',async t=>{
 if(mesh)return;const f=await fixture(t);let release,seen;const issued=new Promise(resolve=>{seen=resolve;}),gate=new Promise(resolve=>{release=resolve;});f.beforeIssue=async()=>{seen();await gate;};
 const pending=f.request('/api/backend/agent/config');await issued;
 assert.equal((await f.request('/api/auth/logout',{method:'POST'})).status,204);release();assert.equal((await pending).status,401);
 assert.equal(f.calls.filter(call=>call.path==='/agent/config').length,0);assert.equal(f.calls.filter(call=>call.path==='/auth/revoke').length,1);assert.equal([...f.store.rows.keys()].filter(key=>key.startsWith('sqlbinding:')).length,0);
});

test('F1 reauthentication revokes and retires the replaced console SQL identity',async t=>{
 if(mesh)return;const f=await fixture(t);assert.equal((await f.request('/api/backend/agent/config')).status,200);
 const start=await fetch(f.url+'/auth/login?reauth=1',{redirect:'manual',headers:{Cookie:f.cookie}}),transaction=cookies(start);const authorization=await fetch(start.headers.get('location'),{redirect:'manual'}),callback=new URL(authorization.headers.get('location'));
 const complete=await fetch(f.url+callback.pathname+callback.search,{redirect:'manual',headers:{Cookie:f.cookie+'; '+transaction}});assert.equal(complete.status,303);
 assert.equal(f.calls.filter(call=>call.path==='/auth/revoke').length,1);assert.equal([...f.store.rows.keys()].filter(key=>key.startsWith('sqlbinding:')).length,0);assert.equal((await f.request('/api/auth/session')).status,401);
});

test('F2 a rejected SQL token is evicted across replicas without replaying the original write',async t=>{
 if(mesh)return;const f=await fixture(t);f.uniqueTokens=true;assert.equal((await f.request('/api/backend/agent/config')).status,200);
 f.respond=(req,res)=>{if(req.url==='/agent/runs'&&req.method==='POST'){res.writeHead(401);res.end(JSON.stringify({code:'unauthorized'}));return true;}return false;};
 assert.equal((await f.request('/api/backend/agent/runs',{method:'POST',body:'{}'})).status,401);assert.equal(f.calls.filter(call=>call.path==='/agent/runs'&&call.method==='POST').length,1);assert.equal(f.issueCount,1);
 const replica=createApp(f.config,{sessionStore:f.store}),url=await bind(replica);t.after(async()=>{replica.closeAllConnections();await new Promise(resolve=>replica.close(resolve));});
 assert.equal((await fetch(url+'/api/backend/agent/config',{headers:{Cookie:f.cookie,Origin:f.config.origin}})).status,200);assert.equal(f.issueCount,2);assert.equal(f.calls.at(-1).auth,'Bearer scoped-backend-token_2');
});

test('F2 a delayed old-token 401 cannot erase a newer shared credential',async t=>{
 if(mesh)return;const f=await fixture(t);f.uniqueTokens=true;await f.request('/api/backend/agent/config');let release,seen;const entered=new Promise(resolve=>{seen=resolve;}),gate=new Promise(resolve=>{release=resolve;});let rejectFirst=true;
 f.respond=async(req,res)=>{if(req.url==='/agent/config'&&req.headers.authorization==='Bearer scoped-backend-token_1'){if(rejectFirst){rejectFirst=false;seen();await gate;}res.writeHead(401);res.end(JSON.stringify({code:'unauthorized'}));return true;}return false;};
 const delayed=f.request('/api/backend/agent/config');await entered;assert.equal((await f.request('/api/backend/agent/config')).status,401);assert.equal((await f.request('/api/backend/agent/config')).status,200);assert.equal(f.issueCount,2);
 release();assert.equal((await delayed).status,401);assert.equal((await f.request('/api/backend/agent/config')).status,200);assert.equal(f.issueCount,2);assert.equal(f.calls.at(-1).auth,'Bearer scoped-backend-token_2');
});

test('F7 Sentinel example follows its own console ports and task backend',async()=>{
 if(mesh)return;const example=JSON.parse(await readFile(new URL('../console.config.example.json',import.meta.url),'utf8'));assert.equal(example.origin,'http://127.0.0.1:'+product.port);assert.equal(example.peer_url,'http://127.0.0.1:'+product.peer_port);assert.equal(example.backend_url,'http://sentinel-api:8080');assert.equal(example.backend_admin_file,'/run/console-private/admin_token');
});

test('F1 role changes revoke old SQL identity without retiring the active login',async t=>{
 if(mesh)return;const f=await fixture(t);f.uniqueTokens=true;assert.equal((await f.request('/api/backend/agent/config')).status,200);f.members.user_a.role='reader';await f.save();assert.equal((await f.request('/api/backend/agent/config')).status,200);assert.equal(f.issueCount,2);assert.equal(f.calls.filter(call=>call.path==='/auth/revoke').length,1);assert.equal([...f.store.rows.keys()].filter(key=>key.startsWith('sqlclosed:')).length,0);assert.equal((await f.request('/api/auth/session')).status,200);
});
