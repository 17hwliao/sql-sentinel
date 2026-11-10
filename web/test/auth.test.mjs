import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {createApp,validateConfig} from '../server.mjs';
import {provider,TestStore,bind,cookies} from './fixtures/identity.mjs';

async function fixture(t,{secure=false}={}){
 const dir=await mkdtemp(path.join(os.tmpdir(),'oidc-test-')),idp=await provider(t),file=n=>path.join(dir,n);let time=Date.now();
 await Promise.all([writeFile(file('client'),'fixture-client-secret'),writeFile(file('session'),'2'.repeat(64)),writeFile(file('redis'),'test-redis'),writeFile(file('members'),'{}')]);
 const config={origin:secure?'https://mesh.unit.test':'http://127.0.0.1:4180',peer_url:secure?'https://sql.unit.test':'http://127.0.0.1:4181',backend_url:'http://127.0.0.1:1',oidc_issuer:idp.issuer,oidc_client_id:'fixture-client',oidc_client_secret_file:file('client'),session_key_file:file('session'),session_redis_url:'redis://127.0.0.1:6379/0',session_redis_password_file:file('redis'),email_delivery:'test_sink',members_file:file('members')};
 const store=new TestStore(()=>time),apps=[];
 async function replica(){const app=createApp(config,{sessionStore:store,now:()=>time});apps.push(app);return bind(app);}
 const url=await replica();
 t.after(async()=>{for(const app of apps)app.closeAllConnections();await Promise.all(apps.map(app=>new Promise(r=>app.close(r))));await rm(dir,{recursive:true,force:true});});
 const f={idp,config,store,url,replica,advance:ms=>time+=ms};
 f.login=async options=>{const login=await idp.login(url,options);f.cookie=login.cookie;return login;};
 f.request=(p,o={})=>fetch(url+p,{...o,redirect:'manual',headers:{Cookie:f.cookie||'',Origin:config.origin,...o.headers}});
 return f;
}
test('authorization code login uses PKCE, nonce, state and a host-scoped HttpOnly cookie',async t=>{
 const f=await fixture(t,{secure:true}),logged=await f.login();assert.equal(logged.result.status,303);const target=new URL(logged.start.headers.get('location'));assert.equal(target.searchParams.get('scope'),'openid profile email');assert.equal(target.searchParams.get('code_challenge_method'),'S256');assert.ok(target.searchParams.get('nonce'));assert.ok(target.searchParams.get('state'));assert.match(logged.result.headers.getSetCookie().at(-1),/^__Host-console_.*HttpOnly; SameSite=Lax;.*Secure$/);assert.doesNotMatch(logged.result.headers.getSetCookie().at(-1),/Domain=/);
 const result=await (await f.request('/api/auth/session')).json();assert.equal(result.subject,'user_a');assert.equal(result.email,'unit@qq.com');assert.ok(result.csrf_token);assert.equal('access_token'in result,false);
 for(const row of f.store.rows.values())assert.doesNotMatch(row.value,/user_a|accessToken|csrf|fixture-client-secret/);
});
test('wrong state consumes the transaction once and never exchanges the code',async t=>{
 const f=await fixture(t),logged=await f.login({mutateCallback:u=>u.searchParams.set('state','forged-state')});assert.equal(logged.result.status,401);assert.equal(f.idp.tokenExchanges,0);const replay=await fetch(f.url+logged.callback.pathname+logged.callback.search,{headers:{Cookie:logged.transactionCookie},redirect:'manual'});assert.equal(replay.status,401);assert.equal(f.idp.tokenExchanges,0);
});
test('callback without browser transaction cookie is refused',async t=>{const f=await fixture(t);assert.equal((await f.request('/auth/callback?code=forged&state=forged')).status,401);assert.equal(f.idp.tokenExchanges,0);});
for(const flag of ['wrongIssuer','wrongAudience','wrongNonce','badSignature','wrongUserinfo'])test('OIDC rejects '+flag,async t=>{const f=await fixture(t);f.idp[flag]=true;const logged=await f.login();assert.notEqual(logged.result.status,303);assert.equal(logged.cookie,'');assert.equal([...f.store.rows.keys()].filter(k=>k.startsWith('session:')).length,0);});
test('missing or unverified email cannot establish a product session',async t=>{const f=await fixture(t);f.idp.verified=false;const logged=await f.login();assert.equal(logged.result.status,403);assert.equal(logged.cookie,'');});
test('verified identity with no product membership can see its identity and log out',async t=>{
 const f=await fixture(t);await f.login();assert.equal((await f.request('/api/session')).status,403);const identity=await (await f.request('/api/auth/session')).json();assert.equal(identity.subject,'user_a');assert.equal((await f.request('/api/auth/logout',{method:'POST',headers:{'X-CSRF-Token':identity.csrf_token}})).status,204);assert.equal((await f.request('/api/auth/session')).status,401);
});
test('logout requires both the exact Origin and a session-bound CSRF token',async t=>{
 const f=await fixture(t);await f.login();const identity=await (await f.request('/api/auth/session')).json();
 for(const headers of [{},{'X-CSRF-Token':'a'.repeat(43)},{'X-CSRF-Token':identity.csrf_token,Origin:'https://foreign.invalid'},{'X-CSRF-Token':identity.csrf_token,Origin:''}])assert.equal((await f.request('/api/auth/logout',{method:'POST',headers})).status,403);
 assert.equal((await f.request('/api/auth/session')).status,200);
});
test('logout is possible during identity-provider downtime and revokes other replicas',async t=>{
 const f=await fixture(t);await f.login();const identity=await (await f.request('/api/auth/session')).json(),replica=await f.replica();
 assert.equal((await fetch(replica+'/api/auth/session',{headers:{Cookie:f.cookie}})).status,200);f.idp.active=false;
 assert.equal((await f.request('/api/auth/logout',{method:'POST',headers:{'X-CSRF-Token':identity.csrf_token}})).status,204);assert.equal((await fetch(replica+'/api/auth/session',{headers:{Cookie:f.cookie}})).status,401);
});
test('provider token revocation and banned users invalidate server sessions',async t=>{const f=await fixture(t);await f.login();f.idp.banned=true;assert.equal((await f.request('/api/auth/session')).status,401);f.idp.banned=false;assert.equal((await f.request('/api/auth/session')).status,401);});
test('activity extends idle expiry without extending the absolute session limit',async t=>{
 const f=await fixture(t);await f.login();for(let i=0;i<16;i++){f.advance(29*60000);assert.equal((await f.request('/api/auth/session')).status,200);}
 f.advance(17*60000);assert.equal((await f.request('/api/auth/session')).status,401);
});
test('idle sessions expire and transaction cookies cannot be reused as sessions',async t=>{const f=await fixture(t),logged=await f.login();assert.equal((await f.request('/api/auth/session',{headers:{Cookie:logged.transactionCookie}})).status,401);f.advance(31*60000);assert.equal((await f.request('/api/auth/session')).status,401);});
test('login refuses external return URLs and fails closed when Redis is unavailable',async t=>{const f=await fixture(t);assert.equal((await f.request('/auth/login?return_to=https://foreign.invalid')).status,400);f.store.rate=async()=>{throw Error('fixture storage offline');};assert.equal((await f.request('/auth/login')).status,503);});
test('an unconfigured mail service blocks login without contacting the identity provider',async t=>{const f=await fixture(t);f.config.email_delivery='unconfigured';const url=await f.replica();assert.equal((await fetch(url+'/auth/login')).status,503);assert.equal(f.idp.requests.length,0);});
test('production and malformed configuration fail closed',()=>{
 const base={origin:'https://mesh.unit.test',peer_url:'https://sql.unit.test'};
 for(const extra of [{oidc_issuer:'http://public.example.net'},{oidc_issuer:'https://identity.unit.test/path'},{session_redis_url:'redis://user:secret@redis:6379/0'},{session_absolute_seconds:999999},{clerk_issuer:'https://old.unit.test'}])assert.throws(()=>validateConfig({...base,...extra}));
});
