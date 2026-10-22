import http from 'node:http';
import {readFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
import {once} from 'node:events';
import {createHash} from 'node:crypto';
import {createRemoteJWKSet, jwtVerify} from 'jose';

const root=path.dirname(fileURLToPath(import.meta.url));
const product=JSON.parse(await readFile(path.join(root,'product.json'),'utf8'));
const ID=/^[a-zA-Z0-9_-]{1,128}$/;
class Failure extends Error {constructor(status,code){super(code);this.status=status;}}
const fail=(status,code)=>{throw new Failure(status,code);};
const json=(res,status,data)=>{res.writeHead(status,{'Content-Type':'application/json; charset=utf-8','Cache-Control':'no-store'});res.end(JSON.stringify(data));};
async function secret(file){if(!file)fail(503,'credential_not_configured');const value=(await readFile(file,'utf8')).trim();if(!value||/[\r\n]/.test(value))fail(503,'credential_invalid');return value;}
function origin(value,privateOK=false){const u=new URL(value);if(u.username||u.password||u.search||u.hash||u.pathname!=='/'||(!['https:'].includes(u.protocol)&&!(u.protocol==='http:'&&(privateOK||['localhost','127.0.0.1','[::1]'].includes(u.hostname)))))throw Error('invalid_origin');return u.origin;}
export function validateConfig(input){
 const c={...input};c.origin=origin(c.origin);c.peer_url=origin(c.peer_url);if(c.origin===c.peer_url)throw Error('consoles_require_distinct_origins');
 if(c.backend_url)c.backend_url=origin(c.backend_url,true);
 if(c.public_api_url){const u=new URL(c.public_api_url);origin(u.origin);if(!/^\/v1\/?$/.test(u.pathname)||u.search||u.hash||u.username||u.password||/example\.(com|invalid)$/.test(u.hostname))throw Error('invalid_public_api_url');c.public_api_url=u.origin+'/v1';}
 if(c.production===true&&(!c.origin.startsWith('https:')||!c.peer_url.startsWith('https:')||!c.clerk_publishable_key?.startsWith('pk_live_')||!c.backend_url||!c.members_file||!c.clerk_secret_file))throw Error('production_configuration_incomplete');
 if(c.production===true){if(new URL(c.origin).hostname.split('.').slice(1).join('.')!==new URL(c.peer_url).hostname.split('.').slice(1).join('.'))throw Error('production_shared_parent_domain_required');if(c.public_api_url&&!c.public_api_url.startsWith('https:'))throw Error('production_public_api_requires_https');}
 if(c.clerk_issuer)c.clerk_issuer=origin(c.clerk_issuer);
 c.authorized_parties=c.authorized_parties||[c.origin,c.peer_url];c.authorized_parties=c.authorized_parties.map(x=>origin(x));
 if(c.clerk_publishable_key){const host=Buffer.from(c.clerk_publishable_key.replace(/^pk_(test|live)_/,''),'base64').toString().replace(/\$$/,'');if(!/^pk_(test|live)_/.test(c.clerk_publishable_key)||!c.clerk_issuer||host!==new URL(c.clerk_issuer).hostname)throw Error('clerk_instance_mismatch');}
 return c;
}
async function body(req,limit=2*1024*1024){const parts=[];let size=0;for await(const p of req){size+=p.length;if(size>limit)fail(413,'body_too_large');parts.push(p);}return Buffer.concat(parts);}
async function smallJSON(req){try{return JSON.parse((await body(req,16384)).toString());}catch(e){if(e instanceof Failure)throw e;fail(400,'invalid_json');}}
function permitted(method,p,mesh){
 if(mesh)return method==='GET'&&(/^\/v1\/(account|models|images|billing\/(balance|prices|requests))$/.test(p)||/^\/v1\/(models|images|observability\/traces)\/[A-Za-z0-9_.-]+$/.test(p))||method==='POST'&&['/v1/chat/completions','/v1/images'].includes(p)||method==='DELETE'&&/^\/v1\/images\/[A-Za-z0-9_-]+$/.test(p);
 return method==='POST'&&['/jobs','/rag/documents','/rag/query','/rag/answer'].includes(p)||method==='GET'&&(/^\/jobs\/[A-Za-z0-9_-]{1,64}(\/(report|approval))?$/.test(p)||['/jobs','/rag/documents','/audit','/history/search'].includes(p))||method==='POST'&&/^\/jobs\/[A-Za-z0-9_-]{1,64}\/(approval|benchmark)$/.test(p)||method==='DELETE'&&/^\/rag\/documents\/[A-Za-z0-9_-]+$/.test(p);
}
function queryString(u,p){
 if(u.search&&!['/jobs','/auth/sessions','/rag/documents','/v1/images','/audit','/history/search'].includes(p))fail(404,'route_not_exposed');
 const allowed={'/jobs':['after','limit','status'],'/auth/sessions':['after'],'/rag/documents':['after','limit'],'/v1/images':['after'],'/audit':['after'],'/history/search':['table','q']};
 for(const [k,v]of u.searchParams){if(!allowed[p]?.includes(k)||u.searchParams.getAll(k).length!==1||v.length>(k==='q'?500:128))fail(400,'invalid_query');}
 return u.search;
}
export function createApp(input,dependencies={}){
 const c=validateConfig(input),mesh=product.id==='agentmesh',fetcher=dependencies.fetcher||fetch;
 const configured=!!(c.clerk_publishable_key&&c.clerk_issuer&&c.clerk_secret_file&&c.members_file&&c.backend_url);
 const jwks=dependencies.jwks||(c.clerk_issuer?createRemoteJWKSet(new URL(c.clerk_issuer+'/.well-known/jwks.json'),{timeoutDuration:5000}):null);
 const tokens=new Map(),slots=new Map();let authenticating=0,total=0;
 async function clerk(resource){const r=await fetcher('https://api.clerk.com/v1/'+resource,{headers:{Authorization:'Bearer '+await secret(c.clerk_secret_file)},signal:AbortSignal.timeout(8000),redirect:'error'});if(!r.ok)fail(r.status===404?401:503,'identity_unavailable');return r.json();}
 async function authenticate(req){
  if(!configured)fail(503,'login_not_configured');
  if(authenticating>=16)fail(429,'identity_busy');authenticating++;
  try{
   const header=req.headers.authorization||'';if(!/^Bearer [A-Za-z0-9_.-]{20,16384}$/.test(header))fail(401,'login_required');
   let payload;try{({payload}=await jwtVerify(header.slice(7),jwks,{issuer:c.clerk_issuer,algorithms:['RS256'],requiredClaims:['exp','iat','sub','sid','azp'],maxTokenAge:300,clockTolerance:5}));}catch{fail(401,'session_invalid');}
   if(!ID.test(payload.sub)||!ID.test(payload.sid)||!c.authorized_parties.includes(payload.azp)||payload.sts==='pending')fail(401,'session_invalid');
   const [session,user]=await Promise.all([clerk('sessions/'+payload.sid),clerk('users/'+payload.sub)]);
   if(session.status!=='active'||session.user_id!==payload.sub||user.id!==payload.sub||user.banned||user.locked)fail(401,'session_inactive');
   const email=user.email_addresses?.find(x=>x.id===user.primary_email_address_id);if(email?.verification?.status!=='verified')fail(403,'email_not_verified');
   const members=JSON.parse(await readFile(c.members_file,'utf8')),m=members[payload.sub];
   if(!m||m.enabled!==true)fail(403,'product_access_required');
   if(!ID.test(m.tenant_id))fail(503,'membership_invalid');
   if(!mesh&&(!ID.test(m.project_id)||!['reader','writer','reviewer','admin'].includes(m.role)||!Array.isArray(m.tables)||!m.tables.length||m.tables.length>16||m.tables.some(t=>!ID.test(t))))fail(503,'membership_invalid');
   return {sub:payload.sub,m,name:user.first_name||m.display_name||'工作空间成员',email:email.email_address};
  }finally{authenticating--;}
 }
 async function upstream(p,token,options={}){
  if(!c.backend_url)fail(503,'backend_not_configured');
  return fetcher(c.backend_url+p,{...options,redirect:'error',headers:{...options.headers,Authorization:'Bearer '+token},signal:options.signal||AbortSignal.timeout(12000)});
 }
 async function backendToken(a){
  if(mesh){const token=await secret(a.m.gateway_key_file);const r=await upstream('/v1/account',token);if(!r.ok)fail(r.status===401?403:503,'gateway_identity_unavailable');const account=await r.json();if(account.tenant_id!==a.m.tenant_id||account.enabled!==true)fail(403,'gateway_membership_mismatch');return token;}
  const key=createHash('sha256').update(JSON.stringify([a.sub,a.m])).digest('hex'),old=tokens.get(a.sub);
  if(old?.key===key&&old.until>Date.now())return old.value;
  const principal={subject:a.sub,tenant_id:a.m.tenant_id,project_id:a.m.project_id,role:a.m.role,tables:a.m.tables};
  const r=await upstream('/auth/tokens',await secret(c.backend_admin_file),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(principal)});
  if(!r.ok)fail(503,'scoped_identity_unavailable');const data=await r.json();if(!data.token)fail(503,'scoped_identity_invalid');
  if(tokens.size>=1000)tokens.delete(tokens.keys().next().value);tokens.set(a.sub,{key,value:data.token,until:Date.now()+Math.min(3300,data.expires_in||0)*1000});return data.token;
 }
 async function keys(req,res,a,u){
  if(!mesh||a.m.can_manage_keys!==true)fail(403,'key_management_forbidden');const admin=await secret(c.backend_admin_file);
  const listPath='/admin/tenants/'+encodeURIComponent(a.m.tenant_id)+'/api-keys';
  if(req.method==='GET'){
   const after=u.searchParams.get('after')||'';if(after&&!ID.test(after))fail(400,'invalid_cursor');
   const r=await upstream(listPath+'?limit=100&after='+encodeURIComponent(after),admin);return json(res,r.status,await r.json());
  }
  if(req.method==='POST'&&u.pathname==='/api/keys'){
   const value=await smallJSON(req);if(Object.keys(value).length)fail(400,'unexpected_fields');
   const r=await upstream('/admin/api-keys',admin,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:a.m.tenant_id})});return json(res,r.status,await r.json());
  }
  if(req.method==='DELETE'&&/^\/api\/keys\/[A-Za-z0-9_-]+$/.test(u.pathname)){
   const selected=u.pathname.split('/').at(-1);let after='',found=false;
   for(let n=0;n<100;n++){
    const r=await upstream(listPath+'?limit=100&after='+encodeURIComponent(after),admin);if(!r.ok)fail(503,'key_list_unavailable');const data=await r.json();
    if(data.data?.some(k=>k.key_id===selected)){found=true;break;}if(!data.next_after)break;after=data.next_after;
   }
   if(!found)fail(404,'key_not_found');const r=await upstream('/admin/api-keys/'+selected,admin,{method:'DELETE'});res.writeHead(r.status);return res.end();
  }fail(405,'method_not_allowed');
 }
 async function management(req,res,a,u){
  if(!mesh||a.m.platform_admin!==true)fail(403,'platform_admin_required');
  const p=u.pathname.slice('/api/admin'.length),allowed=(p==='/tenants'&&['GET','POST'].includes(req.method))||(/^\/tenants\/[A-Za-z0-9_-]+$/.test(p)&&['GET','PATCH'].includes(req.method))||(p==='/usage/outbox-summary'&&req.method==='GET')||(['/billing/credits','/billing/resolve','/quota/grants'].includes(p)&&req.method==='POST');
  if(!allowed)fail(404,'route_not_exposed');
  for(const [k,v]of u.searchParams)if(!['after','limit'].includes(k)||p!=='/tenants'||req.method!=='GET'||v.length>128||u.searchParams.getAll(k).length!==1)fail(400,'invalid_query');
  const value=['POST','PATCH'].includes(req.method)?await smallJSON(req):undefined;
  const r=await upstream('/admin'+p+u.search,await secret(c.backend_admin_file),{method:req.method,headers:{'Content-Type':'application/json'},body:value===undefined?undefined:JSON.stringify(value)});
  if(['POST','PATCH'].includes(req.method))console.info(JSON.stringify({event:'console_admin_operation',subject:a.sub,action:req.method+' '+p,status:r.status}));
  if(r.status===204){res.writeHead(204);return res.end();}return json(res,r.status,await r.json());
 }
 return http.createServer(async(req,res)=>{
  res.setHeader('X-Content-Type-Options','nosniff');res.setHeader('Referrer-Policy','no-referrer');res.setHeader('X-Frame-Options','DENY');res.setHeader('Cache-Control','no-store');
  res.setHeader('Permissions-Policy','camera=(), microphone=(), geolocation=()');
  let activeSubject;
  try{
   const u=new URL(req.url,c.origin);
   if(req.method==='GET'&&u.pathname==='/healthz')return json(res,200,{status:'ok',product:product.id});
   if(req.method==='GET'&&u.pathname==='/readyz'){
    if(!configured)fail(503,'login_not_configured');const [,members]=await Promise.all([secret(c.clerk_secret_file),readFile(c.members_file,'utf8')]);if(!Object.values(JSON.parse(members)).some(m=>m?.enabled===true))fail(503,'no_enabled_members');const r=await upstream('/readyz','');if(!r.ok)fail(503,'backend_not_ready');return json(res,200,{status:'ready',identity:'configured_not_live_verified'});
   }
   if(req.method==='GET'&&u.pathname==='/api/config')return json(res,200,{product:product.id,peer_url:c.peer_url,clerk_publishable_key:c.clerk_publishable_key||null,auth_configured:configured,api_base_url:c.public_api_url||null});
   if(u.pathname.startsWith('/api/')){
    if(req.headers.origin&&req.headers.origin!==c.origin)fail(403,'origin_forbidden');
    if(req.headers['sec-fetch-site']==='cross-site')fail(403,'origin_forbidden');
    const a=await authenticate(req);
    if(req.method==='GET'&&u.pathname==='/api/session')return json(res,200,{subject:a.sub,name:a.name,email:a.email,tenant_id:a.m.tenant_id,project_id:a.m.project_id||null,role:a.m.role||'member',can_manage_keys:mesh&&a.m.can_manage_keys===true,platform_admin:mesh&&a.m.platform_admin===true,tables:a.m.tables||[]});
    if(total>=64||(slots.get(a.sub)||0)>=8)fail(429,'console_busy');total++;slots.set(a.sub,(slots.get(a.sub)||0)+1);activeSubject=a.sub;
    if(u.pathname==='/api/keys'||u.pathname.startsWith('/api/keys/')){await backendToken(a);return await keys(req,res,a,u);}
    if(u.pathname.startsWith('/api/admin/'))return await management(req,res,a,u);
    if(req.method==='GET'&&u.pathname==='/api/readiness'){await backendToken(a);const r=await upstream('/readyz','');if(!r.ok)fail(503,'backend_not_ready');return json(res,200,await r.json());}
    if(req.method==='GET'&&u.pathname==='/api/operations'){
     const allowed=mesh?a.m.platform_admin===true:['admin','reviewer'].includes(a.m.role);if(!allowed)fail(403,'operations_forbidden');
     const result={};for(const p of ['/healthz','/readyz',...(mesh?['/health/providers']:[])]){try{const r=await upstream(p,'');result[p]={status:r.status,data:await r.json()};}catch{result[p]={status:503,data:{code:'backend_unavailable'}};}}return json(res,200,result);
    }
    if(!u.pathname.startsWith('/api/backend/'))fail(404,'not_found');
    const p=u.pathname.slice('/api/backend'.length),scopedAdmin=!mesh&&a.m.role==='admin'&&((p==='/auth/sessions'&&req.method==='GET')||(p==='/auth/revoke'&&req.method==='POST'));
    if(!permitted(req.method,p,mesh)&&!scopedAdmin&&!(p==='/connections'&&req.method==='GET'&&!mesh))fail(404,'route_not_exposed');const search=queryString(u,p);
    const data=['POST','DELETE'].includes(req.method)?await body(req):undefined;
    const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),130000);const close=()=>controller.abort();res.on('close',close);
    try{
     const r=await upstream(p+search,await backendToken(a),{method:req.method,body:data?.length?data:undefined,headers:{'Content-Type':req.headers['content-type']||'application/json'},signal:controller.signal});
     if(!mesh&&r.status===401)tokens.delete(a.sub);
     const headers={};for(const h of ['content-type','x-sentinel-report-sha256','x-agentmesh-billing-request-id','x-agentmesh-trace-id','retry-after'])if(r.headers.has(h))headers[h]=r.headers.get(h);
     res.writeHead(r.status,headers);let bytes=0;
     if(r.body)for await(const chunk of r.body){bytes+=chunk.length;if(bytes>16*1024*1024)throw Error('response_too_large');if(!res.write(chunk))await once(res,'drain',{signal:controller.signal});}res.end();
    }finally{clearTimeout(timer);res.off('close',close);controller.abort();}return;
   }
   if(!['GET','HEAD'].includes(req.method))fail(405,'method_not_allowed');
   const requestPath=decodeURIComponent(u.pathname);if(requestPath.includes('..')||requestPath.includes('\\'))fail(404,'not_found');
   const name=requestPath==='/'?'index.html':requestPath.slice(1),file=path.join(root,'dist',name);
   let data;try{data=await readFile(file);}catch{fail(404,'not_found');}
   const ext=path.extname(file);const mime={'.html':'text/html; charset=utf-8','.js':'text/javascript; charset=utf-8','.css':'text/css; charset=utf-8','.svg':'image/svg+xml','.woff2':'font/woff2'}[ext];if(!mime)fail(404,'not_found');
   if(/^assets\/[A-Za-z0-9_.-]+-[A-Za-z0-9_-]{6,}\.(js|css|woff2|svg)$/.test(name))res.setHeader('Cache-Control','public, max-age=31536000, immutable');
   res.writeHead(200,{'Content-Type':mime});res.end(req.method==='HEAD'?undefined:data);
  }catch(e){if(res.headersSent){res.destroy();}else json(res,e instanceof Failure?e.status:503,{code:e instanceof Failure?e.message:'service_unavailable'});}
  finally{if(activeSubject){total--;const left=(slots.get(activeSubject)||1)-1;if(left)slots.set(activeSubject,left);else slots.delete(activeSubject);}}
 });
}
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
 const config=process.env.CONSOLE_CONFIG_FILE?JSON.parse(await readFile(process.env.CONSOLE_CONFIG_FILE,'utf8')):{origin:`http://127.0.0.1:${product.port}`,peer_url:`http://127.0.0.1:${product.peer_port}`};
 const app=createApp(config);app.requestTimeout=150000;app.headersTimeout=15000;app.keepAliveTimeout=5000;
 app.listen(Number(process.env.PORT||product.port),process.env.HOST||'127.0.0.1',()=>console.log(`${product.title} console: ${config.origin}`));
}
