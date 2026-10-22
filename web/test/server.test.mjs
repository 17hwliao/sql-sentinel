import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {createLocalJWKSet,generateKeyPair,exportJWK,SignJWT} from 'jose';
import {createApp,validateConfig} from '../server.mjs';
import product from '../product.json' with {type:'json'};

const mesh=product.id==='agentmesh',issuer='https://unit.clerk.accounts.dev';
const pair=await generateKeyPair('RS256'),pub={...await exportJWK(pair.publicKey),kid:'unit',alg:'RS256'};
const bind=server=>new Promise(resolve=>server.listen(0,'127.0.0.1',()=>resolve('http://127.0.0.1:'+server.address().port)));
async function fixture(t){
 const dir=await mkdtemp(path.join(os.tmpdir(),'console-test-')),calls=[];const file=n=>path.join(dir,n);
 const f={dir,calls,sessionActive:true,verified:true,banned:false,members:{user_a:{enabled:true,tenant_id:'tenant_a',project_id:'project_a',role:'writer',tables:['orders'],gateway_key_file:file('gateway'),can_manage_keys:true}}};
 await writeFile(file('clerk'),'sk_test_fixture_only');await writeFile(file('admin'),'internal-admin-fixture');await writeFile(file('gateway'),'am_fixture_tenant_a');
 const backend=http.createServer(async(req,res)=>{let raw='';for await(const x of req)raw+=x;calls.push({method:req.method,path:req.url,auth:req.headers.authorization,body:raw?JSON.parse(raw):null});res.setHeader('Content-Type','application/json');
  if(req.url==='/v1/account')return res.end(JSON.stringify({tenant_id:f.wrongTenant?'tenant_b':'tenant_a',enabled:true}));
  if(req.url==='/auth/tokens')return res.end(JSON.stringify({token:'scoped-backend-token',expires_in:3600}));
  if(req.url.startsWith('/admin/tenants/'))return res.end(JSON.stringify({data:[{key_id:'owned_key',enabled:true}],next_after:''}));
  if(req.url==='/admin/api-keys')return res.end(JSON.stringify({api_key:'am_new_fixture',key_id:'owned_new'}));
  if(req.method==='DELETE'){res.statusCode=204;return res.end();}
  if(req.url==='/v1/chat/completions'){res.setHeader('Content-Type','text/event-stream');res.setHeader('X-AgentMesh-Billing-Request-ID','req_fixture');return res.end('data: {"choices":[{"delta":{"content":"hello"}}]}\n\ndata: [DONE]\n\n');}
  if(req.url.endsWith('/report'))res.setHeader('X-Sentinel-Report-SHA256','a'.repeat(64));
  res.end(JSON.stringify({data:[],ok:true}));
 });
 const backendURL=await bind(backend);
 const config={origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181',backend_url:backendURL,clerk_issuer:issuer,clerk_publishable_key:'pk_test_'+Buffer.from('unit.clerk.accounts.dev$').toString('base64'),clerk_secret_file:file('clerk'),backend_admin_file:file('admin'),members_file:file('members')};
 const save=()=>writeFile(file('members'),JSON.stringify(f.members));await save();f.save=save;
 const fetcher=async(url,opts)=>{if(url.startsWith('https://api.clerk.com/v1/')){assert.equal(opts.headers.Authorization,'Bearer sk_test_fixture_only');const session=url.includes('/sessions/');return Response.json(session?{status:f.sessionActive?'active':'revoked',user_id:'user_a'}:{id:'user_a',first_name:'Example',banned:f.banned,primary_email_address_id:'email1',email_addresses:[{id:'email1',email_address:'unit@example.invalid',verification:{status:f.verified?'verified':'unverified'}}]});}return fetch(url,opts);};
 const app=createApp(config,{jwks:createLocalJWKSet({keys:[pub]}),fetcher});f.url=await bind(app);f.config=config;
 f.token=async(extra={})=>new SignJWT({sid:'sess_a',azp:config.origin,...extra}).setSubject('user_a').setIssuedAt().setExpirationTime(extra.exp??'2m').setIssuer(issuer).setProtectedHeader({alg:'RS256',kid:'unit'}).sign(pair.privateKey);
 f.request=async(p,opts={},extra={})=>fetch(f.url+p,{...opts,headers:{Authorization:'Bearer '+await f.token(extra),Origin:config.origin,...opts.headers}});
 t.after(async()=>{app.closeAllConnections();backend.closeAllConnections();await Promise.all([new Promise(r=>app.close(r)),new Promise(r=>backend.close(r))]);await rm(dir,{recursive:true,force:true});});return f;
}
test('unconfigured console fails closed while static config remains public',async t=>{const app=createApp({origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181'});const url=await bind(app);t.after(()=>{app.closeAllConnections();app.close();});assert.equal((await fetch(url+'/api/session')).status,503);const data=await (await fetch(url+'/api/config')).json();assert.equal(data.auth_configured,false);assert.equal('clerk_secret_file'in data,false);});
test('same issuer identity is mapped to the current product membership',async t=>{const f=await fixture(t);const r=await f.request('/api/session');assert.equal(r.status,200);const data=await r.json();assert.equal(data.subject,'user_a');assert.equal(data.tenant_id,'tenant_a');assert.equal('gateway_key_file'in data,false);assert.equal(f.calls.length,0);});
test('unsigned, wrong issuer, wrong origin, pending and expired sessions rejected',async t=>{const f=await fixture(t);for(const extra of [{azp:'https://evil.invalid'},{sts:'pending'},{iss:'https://evil.invalid'},{exp:1},{sid:undefined}]){let token;if(extra.iss){token=await new SignJWT({sid:'sess_a',azp:f.config.origin}).setSubject('user_a').setIssuer(extra.iss).setIssuedAt().setExpirationTime('2m').setProtectedHeader({alg:'RS256',kid:'unit'}).sign(pair.privateKey);}else token=await f.token(extra);assert.equal((await fetch(f.url+'/api/session',{headers:{Authorization:'Bearer '+token}})).status,401);}assert.equal((await fetch(f.url+'/api/session',{headers:{Authorization:'Bearer unsigned.invalid.token'}})).status,401);assert.equal(f.calls.length,0);});
test('unverified email and revoked Clerk sessions cannot use business APIs',async t=>{const f=await fixture(t);f.verified=false;assert.equal((await f.request('/api/session')).status,403);f.verified=true;f.sessionActive=false;assert.equal((await f.request('/api/backend/'+(mesh?'v1/models':'jobs/job1'))).status,401);assert.equal(f.calls.length,0);});
test('membership removal and disable take effect without restarting',async t=>{const f=await fixture(t);assert.equal((await f.request('/api/session')).status,200);f.members.user_a.enabled=false;await f.save();assert.equal((await f.request('/api/session')).status,403);delete f.members.user_a;await f.save();assert.equal((await f.request('/api/session')).status,403);});
test('foreign origins and arbitrary admin routes are denied',async t=>{const f=await fixture(t);assert.equal((await f.request('/api/session',{headers:{Origin:'https://evil.invalid'}})).status,403);for(const p of ['/api/backend/admin/tenants','/api/backend/auth/tokens','/api/backend/metrics','/api/backend/jobs/job1?tenant_id=other'])assert.equal((await f.request(p)).status,404);assert.equal(f.calls.length,0);});
test('upstream receives only scoped server credential, never browser token',async t=>{const f=await fixture(t);const r=await f.request('/api/backend/'+(mesh?'v1/models':'jobs/job1'));assert.equal(r.status,200);const last=f.calls.at(-1);assert.equal(last.auth,mesh?'Bearer am_fixture_tenant_a':'Bearer scoped-backend-token');if(!mesh){const issue=f.calls.find(x=>x.path==='/auth/tokens');assert.deepEqual(issue.body,{subject:'user_a',tenant_id:'tenant_a',project_id:'project_a',role:'writer',tables:['orders']});}});
test('response association headers survive console proxy',async t=>{const f=await fixture(t);const r=await f.request(mesh?'/api/backend/v1/chat/completions':'/api/backend/jobs/job1/report',mesh?{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'}:{});assert.equal(r.status,200);assert.equal(r.headers.get(mesh?'x-agentmesh-billing-request-id':'x-sentinel-report-sha256'),mesh?'req_fixture':'a'.repeat(64));if(mesh)assert.match(await r.text(),/data: \[DONE\]/);});
test(mesh?'Key creation ignores no browser-supplied tenant and rejects foreign Key revocation':'role changes invalidate cached scoped SQL token',async t=>{const f=await fixture(t);if(mesh){assert.equal((await f.request('/api/keys',{method:'POST',headers:{'Content-Type':'application/json'},body:'{"tenant_id":"tenant_b"}'})).status,400);assert.equal((await f.request('/api/keys',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status,200);assert.deepEqual(f.calls.find(x=>x.path==='/admin/api-keys').body,{tenant_id:'tenant_a'});assert.equal((await f.request('/api/keys/other_key',{method:'DELETE'})).status,404);assert.equal(f.calls.some(x=>x.path==='/admin/api-keys/other_key'),false);assert.equal((await f.request('/api/keys/owned_key',{method:'DELETE'})).status,204);}else{await f.request('/api/backend/jobs/job1');f.members.user_a.role='reader';await f.save();await f.request('/api/backend/jobs/job1');assert.equal(f.calls.filter(x=>x.path==='/auth/tokens').length,2);assert.equal(f.calls.findLast(x=>x.path==='/auth/tokens').body.role,'reader');}});
test(mesh?'misbound gateway credential cannot read another tenant':'malformed role grants no SQL access',async t=>{const f=await fixture(t);if(mesh){f.wrongTenant=true;assert.equal((await f.request('/api/backend/v1/models')).status,403);assert.equal(f.calls.some(x=>x.path==='/v1/models'),false);}else{f.members.user_a.role='superadmin';await f.save();assert.equal((await f.request('/api/session')).status,503);assert.equal(f.calls.length,0);}});
test('configuration refuses mixed Clerk instances and overlapping console origins',()=>{assert.throws(()=>validateConfig({origin:'https://a.example.com',peer_url:'https://a.example.com'}));assert.throws(()=>validateConfig({origin:'https://a.example.com',peer_url:'https://b.example.com',clerk_publishable_key:'pk_test_'+Buffer.from('other.clerk.accounts.dev$').toString('base64'),clerk_issuer:issuer}));});


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
