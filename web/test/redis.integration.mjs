// Explicit integration entry: requires an isolated, password-protected Redis.
// Not part of the default dependency-free unit suite.
import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {mkdtemp,writeFile,rm,readFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {createClient} from 'redis';
import path from 'node:path';
import os from 'node:os';
import {createApp} from '../server.mjs';
import {redisStore,opaque} from '../auth.mjs';
import {provider,bind,cookies} from './fixtures/identity.mjs';

test('real Redis preserves encrypted sessions across replicas, isolates products and consumes transactions atomically',async t=>{
 assert.ok(process.env.CONSOLE_TEST_REDIS_URL,'Set CONSOLE_TEST_REDIS_URL for an isolated Redis');assert.ok(process.env.CONSOLE_TEST_REDIS_PASSWORD_FILE,'Set a private test password file');
 const idp=await provider(t),dir=await mkdtemp(path.join(os.tmpdir(),'console-redis-')),file=n=>path.join(dir,n);
 await writeFile(file('client'),'fixture-client-secret');await writeFile(file('key'),'3'.repeat(64));await writeFile(file('members'),'{}');
 const c={origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181',backend_url:'http://127.0.0.1:1',oidc_issuer:idp.issuer,oidc_client_id:'fixture-client',oidc_client_secret_file:file('client'),session_key_file:file('key'),session_redis_url:process.env.CONSOLE_TEST_REDIS_URL,session_redis_password_file:process.env.CONSOLE_TEST_REDIS_PASSWORD_FILE,members_file:file('members'),email_delivery:'test_sink'};
 const apps=[createApp(c),createApp(c),createApp({...c,origin:c.peer_url,peer_url:c.origin})],urls=await Promise.all(apps.map(bind)),store=redisStore(c);
 t.after(async()=>{for(const app of apps)app.closeAllConnections();await Promise.all(apps.map(app=>new Promise(r=>app.close(r))));await store.close();await rm(dir,{recursive:true,force:true});});
 const login=await idp.login(urls[0]);assert.equal(login.result.status,303);const headers={Cookie:login.cookie,Origin:c.origin};
 const identity=await (await fetch(urls[1]+'/api/auth/session',{headers})).json();assert.equal(identity.subject,'user_a');
 const sid=login.cookie.split('=')[1],stored=await store.get('session',sid);assert.ok(stored);assert.doesNotMatch(stored,/user_a|accessToken|csrf/);
 assert.equal((await fetch(urls[2]+'/api/auth/session',{headers})).status,401);
 const tx=opaque();await store.put('transaction',tx,'controlled-test-value',60);const take=await Promise.all([store.take('transaction',tx),store.take('transaction',tx)]);assert.equal(take.filter(x=>x==='controlled-test-value').length,1);
 const revoked=opaque();await store.put('session',revoked,'controlled-test-value',60);await store.remove('session',revoked);assert.equal(await store.touch('session',revoked,60),0);assert.equal(await store.get('session',revoked),null);
 const rate=opaque();for(let i=0;i<30;i++)assert.equal(await store.rate(rate),true);assert.equal(await store.rate(rate),false);
 // A dropped Redis socket must recover on the next request without restarting
 // the console. Kill only connections in this test's random issuer namespace.
 const admin=createClient({url:c.session_redis_url,password:(await readFile(c.session_redis_password_file,'utf8')).trim()});admin.on('error',()=>{});await admin.connect();
 try{const prefix='console:'+createHash('sha256').update(JSON.stringify([c.oidc_issuer,c.oidc_client_id,c.origin])).digest('hex')+':';const clients=(await admin.sendCommand(['CLIENT','LIST'])).split('\n').map(line=>Object.fromEntries(line.split(' ').map(v=>v.split('='))));const own=clients.filter(v=>v.name?.startsWith(prefix));assert.ok(own.length>=2);for(const v of own)await admin.sendCommand(['CLIENT','KILL','ID',v.id]);await new Promise(r=>setTimeout(r,30));assert.equal(await store.ping(),'PONG');assert.equal((await fetch(urls[1]+'/api/auth/session',{headers})).status,200);}finally{await admin.close();}
 assert.equal((await fetch(urls[0]+'/api/auth/logout',{method:'POST',headers:{...headers,'X-CSRF-Token':identity.csrf_token}})).status,204);assert.equal((await fetch(urls[1]+'/api/auth/session',{headers})).status,401);
});

async function sqlLifecycleFixture(t){
 const idp=await provider(t),dir=await mkdtemp(path.join(os.tmpdir(),'console-lifecycle-')),file=name=>path.join(dir,name),state={issued:[],revoked:new Set(),calls:[]};
 const backend=http.createServer(async(req,res)=>{
  let raw='';for await(const part of req)raw+=part;state.calls.push({path:req.url,method:req.method,auth:req.headers.authorization});res.setHeader('Content-Type','application/json');
  if(req.url==='/auth/tokens'){
   const item={token:'test_jwt_'+opaque(),token_id:'test_jti_'+opaque(),expires_in:3600};state.issued.push(item);await state.beforeIssue?.(item);return res.end(JSON.stringify(item));
  }
  if(req.url==='/auth/revoke'){state.revoked.add(JSON.parse(raw).token_id);return res.end('{}');}
  if(['/agent/config','/agent/runs'].includes(req.url)){
   const item=state.issued.find(value=>'Bearer '+value.token===req.headers.authorization);await state.beforeBusiness?.(item,req);const valid=item&&!state.revoked.has(item.token_id);res.writeHead(valid?200:401);return res.end(JSON.stringify(valid?{enabled:true}:{code:'unauthorized'}));
  }
  res.writeHead(404);res.end('{}');
 });
 const backendURL=await bind(backend);await writeFile(file('client'),'fixture-client-secret');await writeFile(file('key'),'5'.repeat(64));await writeFile(file('admin'),'private-test-admin');await writeFile(file('members'),JSON.stringify({user_a:{enabled:true,issuer:idp.issuer,tenant_id:'tenant',project_id:'project',role:'writer',tables:['orders']}}));
 const config={origin:'http://127.0.0.1:4181',peer_url:'http://127.0.0.1:4180',backend_url:backendURL,backend_admin_file:file('admin'),oidc_issuer:idp.issuer,oidc_client_id:'fixture-client',oidc_client_secret_file:file('client'),session_key_file:file('key'),session_redis_url:process.env.CONSOLE_TEST_REDIS_URL,session_redis_password_file:process.env.CONSOLE_TEST_REDIS_PASSWORD_FILE,members_file:file('members'),email_delivery:'test_sink'};
 const apps=[createApp(config),createApp(config)],urls=await Promise.all(apps.map(bind)),store=redisStore(config);
 t.after(async()=>{for(const app of apps)app.closeAllConnections();backend.closeAllConnections();await Promise.all([...apps,backend].map(server=>new Promise(resolve=>server.close(resolve))));await store.close();assert.equal(path.dirname(path.resolve(dir)),path.resolve(os.tmpdir()));assert.ok(path.basename(dir).startsWith('console-lifecycle-'));await rm(dir,{recursive:true,force:true});});
 const login=await idp.login(urls[0]);assert.equal(login.result.status,303);const headers={Cookie:login.cookie,Origin:config.origin};const identity=await (await fetch(urls[0]+'/api/auth/session',{headers})).json();
 const request=(replica,p,options={})=>fetch(urls[replica]+p,{...options,headers:{...headers,'X-CSRF-Token':identity.csrf_token,...options.headers}});
 return {state,store,identity,headers,urls,request};
}

test('F1/F2 real Redis fences SQL identity publication and compares stale credential invalidation',async t=>{
 assert.ok(process.env.CONSOLE_TEST_REDIS_URL);assert.ok(process.env.CONSOLE_TEST_REDIS_PASSWORD_FILE);
 await t.test('F1 logout wins over a delayed publication on a different replica',async sub=>{
  const f=await sqlLifecycleFixture(sub);let release,seen;const issued=new Promise(resolve=>{seen=resolve;}),gate=new Promise(resolve=>{release=resolve;});f.state.beforeIssue=async()=>{seen();await gate;};
  const pending=f.request(0,'/api/backend/agent/config');await issued;assert.equal((await f.request(1,'/api/auth/logout',{method:'POST'})).status,204);release();assert.equal((await pending).status,401);
  assert.equal(f.state.issued.length,1);assert.ok(f.state.revoked.has(f.state.issued[0].token_id));assert.equal(f.state.calls.filter(call=>call.path==='/agent/config').length,0);assert.equal(await f.store.get('sqlbinding',f.identity.session_binding),null);assert.ok(await f.store.get('sqlclosed',f.identity.session_binding));
 });
 await t.test('F1 replacement login retires the old binding and revokes its original JWT',async sub=>{
  const f=await sqlLifecycleFixture(sub);assert.equal((await f.request(0,'/api/backend/agent/config')).status,200);const start=await fetch(f.urls[1]+'/auth/login?reauth=1',{redirect:'manual',headers:f.headers}),transaction=cookies(start),authorization=await fetch(start.headers.get('location'),{redirect:'manual'}),callback=new URL(authorization.headers.get('location'));
  const completed=await fetch(f.urls[1]+callback.pathname+callback.search,{redirect:'manual',headers:{Cookie:f.headers.Cookie+'; '+transaction}});assert.equal(completed.status,303);assert.ok(f.state.revoked.has(f.state.issued[0].token_id));assert.equal(await f.store.get('sqlbinding',f.identity.session_binding),null);assert.ok(await f.store.get('sqlclosed',f.identity.session_binding));assert.equal((await f.request(0,'/api/auth/session')).status,401);
 });
 await t.test('F2 a rejected write is not replayed and the next replica can issue a fresh JWT',async sub=>{
  const f=await sqlLifecycleFixture(sub);await f.request(0,'/api/backend/agent/config');f.state.revoked.add(f.state.issued[0].token_id);assert.equal((await f.request(0,'/api/backend/agent/runs',{method:'POST',body:'{}'})).status,401);assert.equal(f.state.calls.filter(call=>call.path==='/agent/runs'&&call.method==='POST').length,1);assert.equal(f.state.issued.length,1);assert.equal(await f.store.get('sqlbinding',f.identity.session_binding),null);assert.equal(await f.store.get('sqlclosed',f.identity.session_binding),null);assert.equal((await f.request(1,'/api/backend/agent/config')).status,200);assert.equal(f.state.issued.length,2);
 });
 await t.test('F2 a late old-token 401 preserves a newer encrypted Redis binding',async sub=>{
  const f=await sqlLifecycleFixture(sub);await f.request(0,'/api/backend/agent/config');const old=f.state.issued[0];let release,seen,first=true;const entered=new Promise(resolve=>{seen=resolve;}),gate=new Promise(resolve=>{release=resolve;});f.state.beforeBusiness=async item=>{if(item===old){if(first){first=false;seen();await gate;}f.state.revoked.add(item.token_id);}};
  const pending=f.request(0,'/api/backend/agent/config');await entered;assert.equal((await f.request(1,'/api/backend/agent/config')).status,401);assert.equal((await f.request(1,'/api/backend/agent/config')).status,200);const fresh=await f.store.get('sqlbinding',f.identity.session_binding);assert.ok(fresh);assert.equal(f.state.issued.length,2);release();assert.equal((await pending).status,401);assert.equal(await f.store.get('sqlbinding',f.identity.session_binding),fresh);assert.equal((await f.request(0,'/api/backend/agent/config')).status,200);assert.equal(f.state.issued.length,2);
 });
 await t.test('F1/F2 Redis atomic primitives preserve NX ownership and reject stale deletes',async sub=>{
  const f=await sqlLifecycleFixture(sub),sid=opaque(),binding=createHash('sha256').update(sid).digest('hex');
  assert.equal(await f.store.publishScoped(binding,'opaque_test_a',60),-1,'an expired or missing console session cannot publish');await f.store.put('session',sid,'test-session-existence-only',60);
  assert.equal(await f.store.publishScoped(binding,'opaque_test_a',60),1);assert.equal(await f.store.publishScoped(binding,'opaque_test_b',60),0);assert.equal(await f.store.get('sqlbinding',binding),'opaque_test_a');
  assert.equal(await f.store.removeIfValue('sqlbinding',binding,'opaque_test_a'),1);assert.equal(await f.store.publishScoped(binding,'opaque_test_b',60),1);assert.equal(await f.store.removeIfValue('sqlbinding',binding,'opaque_test_a'),0);assert.equal(await f.store.get('sqlbinding',binding),'opaque_test_b');
  await f.store.put('sqlclosed',binding,'1',60);assert.equal(await f.store.removeIfValue('sqlbinding',binding,'opaque_test_b'),1);assert.equal(await f.store.publishScoped(binding,'opaque_test_c',60),-1);assert.equal(await f.store.get('sqlbinding',binding),null);await f.store.remove('session',sid);
 });
});

test('real Redis shares encrypted SQL identity across consoles and logout revokes it without live IdP access',async t=>{
 assert.ok(process.env.CONSOLE_TEST_REDIS_URL,'Set CONSOLE_TEST_REDIS_URL for an isolated Redis');assert.ok(process.env.CONSOLE_TEST_REDIS_PASSWORD_FILE,'Set a private test password file');
 for(const mode of ['unavailable','revoked'])await t.test(mode,async sub=>{
  const idp=await provider(sub),dir=await mkdtemp(path.join(os.tmpdir(),'console-sqlbinding-')),file=name=>path.join(dir,name),calls=[],revoked=new Set();
  const token='private-sql-jwt-fixture-'+opaque(),tokenID='sql_session_'+opaque(),adminToken='private-admin-fixture-'+opaque();let unavailable=false;
  const backend=http.createServer(async(req,res)=>{
   let raw='';for await(const chunk of req)raw+=chunk;const value=raw?JSON.parse(raw):null;
   calls.push({path:req.url,method:req.method,auth:req.headers.authorization,body:value});res.setHeader('Content-Type','application/json');
   if(req.url==='/auth/tokens'){
    assert.equal(req.headers.authorization,'Bearer '+adminToken);assert.deepEqual(value,{subject:'user_a',tenant_id:'tenant_redis',project_id:'project_redis',role:'writer',tables:['orders']});
    return res.end(JSON.stringify({token,token_id:tokenID,expires_in:3600}));
   }
   if(req.url==='/auth/revoke'){
    assert.equal(req.headers.authorization,'Bearer '+adminToken);assert.deepEqual(value,{token_id:tokenID});revoked.add(value.token_id);return res.end(JSON.stringify({revoked:true}));
   }
   if(req.url==='/agent/config'){
    assert.equal(req.headers.authorization,'Bearer '+token);if(revoked.has(tokenID)){res.writeHead(401);return res.end(JSON.stringify({code:'unauthorized'}));}
    return res.end(JSON.stringify({enabled:true,version:'redis-test.v1',tenant_id:'tenant_redis',project_id:'project_redis'}));
   }
   res.writeHead(404);res.end(JSON.stringify({code:'not_found'}));
  });
  const backendURL=await bind(backend);
  await writeFile(file('client'),'fixture-client-secret');await writeFile(file('key'),'4'.repeat(64));await writeFile(file('admin'),adminToken);
  await writeFile(file('members'),JSON.stringify({user_a:{enabled:true,issuer:idp.issuer,tenant_id:'tenant_redis',project_id:'project_redis',role:'writer',tables:['orders']}}));
  const config={origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181',backend_url:backendURL,oidc_issuer:idp.issuer,oidc_client_id:'fixture-client',oidc_client_secret_file:file('client'),session_key_file:file('key'),session_redis_url:process.env.CONSOLE_TEST_REDIS_URL,session_redis_password_file:process.env.CONSOLE_TEST_REDIS_PASSWORD_FILE,members_file:file('members'),backend_admin_file:file('admin'),email_delivery:'test_sink'};
  const fetcher=(url,options)=>{if(unavailable&&new URL(url).origin===idp.issuer)throw Error('controlled_idp_outage');return fetch(url,options);};
  const apps=[createApp(config,{fetcher}),createApp(config,{fetcher})],urls=await Promise.all(apps.map(bind)),store=redisStore(config);
  sub.after(async()=>{
   for(const app of apps)app.closeAllConnections();backend.closeAllConnections();await Promise.all([...apps,backend].map(server=>new Promise(resolve=>server.close(resolve))));await store.close();
   assert.equal(path.dirname(path.resolve(dir)),path.resolve(os.tmpdir()));assert.ok(path.basename(dir).startsWith('console-sqlbinding-'));await rm(dir,{recursive:true,force:true});
  });
  const login=await idp.login(urls[0]);assert.equal(login.result.status,303);const headers={Cookie:login.cookie,Origin:config.origin};
  const identityResponse=await fetch(urls[0]+'/api/auth/session',{headers});assert.equal(identityResponse.status,200);const identity=await identityResponse.json();
  for(const url of urls){const result=await fetch(url+'/api/backend/agent/config',{headers});assert.equal(result.status,200);assert.equal((await result.json()).project_id,'project_redis');}
  assert.equal(calls.filter(call=>call.path==='/auth/tokens').length,1,'second replica must reuse the published execution identity');
  const rawBinding=await store.get('sqlbinding',identity.session_binding);assert.ok(rawBinding,'SQL binding must be persisted in real Redis');
  for(const sensitive of [token,tokenID,'user_a','tenant_redis','project_redis'])assert.equal(rawBinding.includes(sensitive),false,'raw Redis value must not disclose SQL identity');
  if(mode==='unavailable'){
   unavailable=true;assert.equal((await fetch(urls[1]+'/api/auth/session',{headers})).status,503);
  }else idp.active=false;
  const providerRequests=idp.requests.length;
  const logout=await fetch(urls[1]+'/api/auth/logout',{method:'POST',headers:{...headers,'X-CSRF-Token':identity.csrf_token}});
  assert.equal(logout.status,204);assert.ok(logout.headers.getSetCookie().some(cookie=>cookie.includes('Max-Age=0')));
  assert.equal(idp.requests.length,providerRequests,'logout must use local encrypted session rather than require a live IdP');
  assert.equal(calls.filter(call=>call.path==='/auth/revoke').length,1);assert.equal(revoked.has(tokenID),true);assert.equal(await store.get('sqlbinding',identity.session_binding),null);
  const sid=login.cookie.split('=')[1];assert.equal(await store.get('session',sid),null);
  const before=calls.length;assert.equal((await fetch(urls[0]+'/api/backend/agent/config',{headers})).status,401);assert.equal(calls.length,before,'logged-out replica must not reach SQL backend or mint another token');
 });
});
