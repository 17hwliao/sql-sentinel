import {readFile} from 'node:fs/promises';
import {randomBytes,createHash,createCipheriv,createDecipheriv,timingSafeEqual} from 'node:crypto';
import {createClient} from 'redis';
import * as oidc from 'openid-client';

export class Failure extends Error {constructor(status,code){super(code);this.status=status;}}
export const fail=(status,code)=>{throw new Failure(status,code);};
export const opaque=()=>randomBytes(32).toString('base64url');
const digest=value=>createHash('sha256').update(value).digest('hex');
const validOpaque=value=>typeof value==='string'&&/^[A-Za-z0-9_-]{43}$/.test(value);
const equal=(a,b)=>validOpaque(a)&&validOpaque(b)&&timingSafeEqual(Buffer.from(a),Buffer.from(b));
export async function secret(file){if(!file)fail(503,'credential_not_configured');const value=(await readFile(file,'utf8')).trim();if(!value||/[\r\n]/.test(value))fail(503,'credential_invalid');return value;}
export function identityConfigured(c){return !!(c.oidc_issuer&&c.oidc_client_id&&c.oidc_client_secret_file&&c.session_key_file&&c.session_redis_url&&c.session_redis_password_file&&c.members_file&&c.backend_url);}
export function validateIdentity(c){
 if(Object.keys(c).some(k=>k.startsWith('clerk_')))throw Error('legacy_clerk_configuration_replace_explicitly');
 if(c.oidc_issuer){const u=new URL(c.oidc_issuer);if(u.pathname!=='/'||u.search||u.hash||u.username||u.password||(u.protocol!=='https:'&&!(c.production!==true&&u.protocol==='http:'&&['127.0.0.1','localhost','[::1]'].includes(u.hostname))))throw Error('invalid_oidc_issuer');c.oidc_issuer=u.origin;}
 if(c.oidc_client_id&&!/^[A-Za-z0-9_-]{1,128}$/.test(c.oidc_client_id))throw Error('invalid_oidc_client');
 if(c.session_redis_url){const u=new URL(c.session_redis_url);if(!['redis:','rediss:'].includes(u.protocol)||u.username||u.password||u.search||u.hash||!/^\/[0-9]{0,2}$/.test(u.pathname))throw Error('invalid_session_redis_url');}
 c.email_delivery=c.email_delivery||'unconfigured';if(!['unconfigured','smtp','test_sink'].includes(c.email_delivery))throw Error('invalid_email_delivery');
 if(c.production===true&&(!identityConfigured(c)||!c.oidc_issuer.startsWith('https:')||c.email_delivery==='test_sink'))throw Error('production_identity_incomplete');
 c.session_idle_seconds=c.session_idle_seconds??1800;c.session_absolute_seconds=c.session_absolute_seconds??28800;
 if(!Number.isInteger(c.session_idle_seconds)||c.session_idle_seconds<60||c.session_idle_seconds>3600||!Number.isInteger(c.session_absolute_seconds)||c.session_absolute_seconds<c.session_idle_seconds||c.session_absolute_seconds>28800)throw Error('invalid_session_lifetime');
 return c;
}

// The same key space is shared by replicas, but isolated by issuer, client and origin.
// Values containing tokens/PKCE verifiers are authenticated-encrypted before Redis.
export function redisStore(c){
 let client,pending;const prefix='console:'+digest(JSON.stringify([c.oidc_issuer,c.oidc_client_id,c.origin]))+':';
 async function connection(){if(client?.isReady)return client;if(!pending)pending=(async()=>{if(client?.isOpen)client.destroy();client=createClient({url:c.session_redis_url,password:await secret(c.session_redis_password_file),name:prefix+opaque(),socket:{connectTimeout:3000,reconnectStrategy:false},disableOfflineQueue:true});client.on('error',()=>{});await client.connect();return client;})().finally(()=>{pending=null;});return pending;}
 const key=(kind,id)=>prefix+kind+':'+digest(id);
 return {
  async put(kind,id,value,seconds){return (await connection()).set(key(kind,id),value,{EX:seconds,NX:true});},
  async get(kind,id){return (await connection()).get(key(kind,id));},
  async take(kind,id){return (await connection()).getDel(key(kind,id));},
  async remove(kind,id){return (await connection()).del(key(kind,id));},
  async removeIfValue(kind,id,value){return Number(await (await connection()).eval("if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end;return 0",{keys:[key(kind,id)],arguments:[value]}));},
  async publishScoped(id,value,seconds){return Number(await (await connection()).eval("if redis.call('EXISTS',KEYS[1])==1 or redis.call('EXISTS',KEYS[3])==0 then return -1 end;if redis.call('SET',KEYS[2],ARGV[1],'EX',ARGV[2],'NX') then return 1 end;return 0",{keys:[key('sqlclosed',id),key('sqlbinding',id),prefix+'session:'+id],arguments:[value,String(seconds)]}));},
  async touch(kind,id,seconds){return (await connection()).expire(key(kind,id),seconds);},
  async rate(id){return Number(await (await connection()).eval("local n=redis.call('INCR',KEYS[1]);if n==1 then redis.call('EXPIRE',KEYS[1],60) end;return n",{keys:[key('rate',id)],arguments:[]}))<=30;},
  async ping(){return (await connection()).ping();},
  async close(){if(client?.isOpen)await client.close();}
 };
}
export function createIdentity(c,{store=redisStore(c),fetcher=fetch,now=()=>Date.now(),beforeLogout=async()=>{},onInvalidSession=async()=>{}}={}){
 const configured=identityConfigured(c),secure=c.origin.startsWith('https:'),suffix=digest(c.origin).slice(0,12),cookieName=(secure?'__Host-':'')+'console_'+suffix,txName=(secure?'__Host-':'')+'oidc_'+suffix;
 let discovery,keyPromise,active=0;
 const callback=c.origin+'/auth/callback';
 function cookie(req,name){const values=(req.headers.cookie||'').split(';').map(x=>x.trim()).filter(x=>x.startsWith(name+'='));if(values.length!==1)return null;const value=values[0].slice(name.length+1);return validOpaque(value)?value:null;}
 function setCookie(res,name,value,seconds){const old=res.getHeader('Set-Cookie')||[];res.setHeader('Set-Cookie',[...(Array.isArray(old)?old:[old]),`${name}=${value}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${seconds}${secure?'; Secure':''}`]);}
 async function key(){if(!keyPromise)keyPromise=secret(c.session_key_file).then(x=>{if(!/^[a-f0-9]{64}$/.test(x))fail(503,'session_key_invalid');return Buffer.from(x,'hex');}).catch(e=>{keyPromise=null;throw e;});return keyPromise;}
 async function seal(value){const iv=randomBytes(12),cipher=createCipheriv('aes-256-gcm',await key(),iv);cipher.setAAD(Buffer.from(c.origin));return Buffer.concat([iv,cipher.update(JSON.stringify(value)),cipher.final(),cipher.getAuthTag()]).toString('base64url');}
 async function open(raw){if(!raw)return null;try{const data=Buffer.from(raw,'base64url'),cipher=createDecipheriv('aes-256-gcm',await key(),data.subarray(0,12));cipher.setAAD(Buffer.from(c.origin));cipher.setAuthTag(data.subarray(-16));return JSON.parse(Buffer.concat([cipher.update(data.subarray(12,-16)),cipher.final()]).toString());}catch{fail(503,'session_storage_invalid');}}
 async function config(){
  if(!configured)fail(503,'login_not_configured');
  if(!discovery)discovery=(async()=>{
   const fetchOIDC=async(url,options)=>{if(new URL(url).origin!==c.oidc_issuer)fail(503,'identity_endpoint_forbidden');return fetcher(url,{...options,redirect:'error',signal:AbortSignal.timeout(8000)});};
   const options={timeout:8,execute:[oidc.enableNonRepudiationChecks,...(c.production!==true&&c.oidc_issuer.startsWith('http:')?[oidc.allowInsecureRequests]:[])],[oidc.customFetch]:fetchOIDC};
   const conf=await oidc.discovery(new URL(c.oidc_issuer),c.oidc_client_id,{id_token_signed_response_alg:'RS256'},oidc.ClientSecretPost(await secret(c.oidc_client_secret_file)),options);
   const metadata=conf.serverMetadata();for(const field of ['authorization_endpoint','token_endpoint','userinfo_endpoint','jwks_uri','introspection_endpoint'])if(!metadata[field]||new URL(metadata[field]).origin!==c.oidc_issuer)fail(503,'identity_metadata_invalid');
   return conf;
  })().catch(e=>{discovery=null;throw e;});return discovery;
 }
 async function bounded(fn){if(active>=16)fail(429,'identity_busy');active++;try{return await fn();}finally{active--;}}
 async function local(req){if(!configured)fail(503,'login_not_configured');const id=cookie(req,cookieName);if(!id)fail(401,'login_required');const session=await open(await store.get('session',id));if(!session||session.issuer!==c.oidc_issuer||session.expires<=now()){const binding=digest(id);if(session||await store.get('sqlbinding',binding))await onInvalidSession({sub:session?.sub,session_binding:binding});await store.remove('session',id);fail(401,'session_invalid');}return {id,session};}
 async function live(session){
  const conf=await config();let status,user;
  try{status=await oidc.tokenIntrospection(conf,session.accessToken);if(status.active!==true)fail(401,'session_inactive');if(status.sub&&status.sub!==session.sub)fail(401,'session_invalid');if(status.client_id&&status.client_id!==c.oidc_client_id)fail(401,'session_invalid');user=await oidc.fetchUserInfo(conf,session.accessToken,session.sub);}catch(e){if(e instanceof Failure)throw e;fail(503,'identity_unavailable');}
  if(user.email_verified!==true||typeof user.email!=='string'||user.email.length>254)fail(403,'email_not_verified');
  return {sub:session.sub,name:typeof user.name==='string'?user.name:'工作空间成员',email:user.email,csrf_token:session.csrf};
 }
 async function identify(req){return bounded(async()=>{const {id,session}=await local(req);try{const identity=await live(session);const ttl=Math.max(1,Math.ceil(Math.min(c.session_idle_seconds,(session.expires-now())/1000)));
    // Do not SET the record here: a concurrent logout must never resurrect it.
    if(!await store.touch('session',id,ttl))fail(401,'session_invalid');return {...identity,session_binding:digest(id)};
   }catch(e){if(e instanceof Failure&&[401,403].includes(e.status)){await onInvalidSession({sub:session.sub,session_binding:digest(id)});await store.remove('session',id);}throw e;}});}
 function checkWrite(req,identity){if(req.headers.origin!==c.origin||req.headers['sec-fetch-site']==='cross-site')fail(403,'origin_forbidden');if(!equal(req.headers['x-csrf-token'],identity.csrf_token))fail(403,'csrf_invalid');}
 async function handle(req,res,url){
  if(req.method==='GET'&&url.pathname==='/auth/login')return bounded(async()=>{
   if(!configured)fail(503,'login_not_configured');if(c.email_delivery==='unconfigured')fail(503,'email_not_configured');if(!await store.rate(req.socket.remoteAddress||'unknown'))fail(429,'identity_busy');
   for(const [name,value]of url.searchParams)if(!['return_to','reauth'].includes(name)||url.searchParams.getAll(name).length!==1||value.length>128)fail(400,'invalid_login_request');
   const returnTo=url.searchParams.get('return_to')||'/';if(!/^\/(?:#[a-z-]{1,32})?$/.test(returnTo))fail(400,'invalid_return_path');
   const conf=await config(),id=opaque(),state=oidc.randomState(),nonce=oidc.randomNonce(),verifier=oidc.randomPKCECodeVerifier();
   const old=cookie(req,txName);if(old)await store.remove('transaction',old);
   await store.put('transaction',id,await seal({state,nonce,verifier,returnTo}),300);setCookie(res,txName,id,300);
   const target=oidc.buildAuthorizationUrl(conf,{redirect_uri:callback,scope:'openid profile email',state,nonce,code_challenge:await oidc.calculatePKCECodeChallenge(verifier),code_challenge_method:'S256',...(url.searchParams.get('reauth')==='1'?{prompt:'login'}:{})});res.writeHead(303,{Location:target.href});res.end();return true;
  });
  if(req.method==='GET'&&url.pathname==='/auth/callback')return bounded(async()=>{
   const id=cookie(req,txName);setCookie(res,txName,'',0);const transaction=id?await open(await store.take('transaction',id)):null;
   if(!transaction)fail(401,'login_transaction_invalid');
   let tokens,claims;try{tokens=await oidc.authorizationCodeGrant(await config(),url,{pkceCodeVerifier:transaction.verifier,expectedState:transaction.state,expectedNonce:transaction.nonce,idTokenExpected:true});claims=tokens.claims();}catch{fail(401,'login_callback_invalid');}
   if(!claims||!/^[-A-Za-z0-9_]{1,128}$/.test(claims.sub)||typeof tokens.access_token!=='string'||tokens.access_token.length>16384||!Number.isFinite(tokens.expires_in)||tokens.expires_in<=0)fail(401,'login_callback_invalid');
   const session={issuer:c.oidc_issuer,sub:claims.sub,accessToken:tokens.access_token,csrf:opaque(),expires:Math.min(now()+c.session_absolute_seconds*1000,now()+tokens.expires_in*1000,claims.exp*1000)};
   session.idleUntil=Math.min(now()+c.session_idle_seconds*1000,session.expires);await live(session);
   const old=cookie(req,cookieName);if(old){const previous=await open(await store.get('session',old));await beforeLogout({sub:previous?.sub,session_binding:digest(old)});await store.remove('session',old);}const sid=opaque();await store.put('session',sid,await seal(session),Math.max(1,Math.ceil((session.idleUntil-now())/1000)));setCookie(res,cookieName,sid,Math.floor((session.expires-now())/1000));res.writeHead(303,{Location:c.origin+transaction.returnTo});res.end();return true;
  });
  if(url.pathname==='/api/auth/logout'&&req.method==='POST'){const {id,session}=await local(req);res.setHeader('X-Console-Session',digest(id));checkWrite(req,{csrf_token:session.csrf});await beforeLogout({sub:session.sub,session_binding:digest(id)});await store.remove('session',id);setCookie(res,cookieName,'',0);res.writeHead(204);res.end();return true;}
  if(url.pathname==='/api/auth/session'&&req.method==='GET'){const who=await identify(req);res.writeHead(200,{'Content-Type':'application/json; charset=utf-8'});res.end(JSON.stringify({subject:who.sub,name:who.name,email:who.email,csrf_token:who.csrf_token,session_binding:who.session_binding}));return true;}
  return false;
 }
 return {configured,handle,identify,checkWrite,
  async scoped(binding){return open(await store.get('sqlbinding',binding));},
  async scopedClosed(binding){return !!await store.get('sqlclosed',binding);},
  async retireScoped(binding){return store.put('sqlclosed',binding,'1',3600);},
  async rememberScoped(binding,value){return store.publishScoped(binding,await seal(value),3600);},
  async forgetScoped(binding,expected){const raw=await store.get('sqlbinding',binding);if(!raw)return 0;const value=await open(raw);if(!expected||value?.value!==expected.value||(expected.id&&value?.id!==expected.id))return 0;return store.removeIfValue('sqlbinding',binding,raw);},
  async ready(){await Promise.all([config(),key(),store.ping()]);},close:()=>store.close?.()};
}
