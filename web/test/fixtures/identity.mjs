import http from 'node:http';
import {generateKeyPair,exportJWK,SignJWT} from 'jose';
import {createHash,randomBytes} from 'node:crypto';
import assert from 'node:assert/strict';

export class TestStore {
 constructor(now=()=>Date.now()){this.rows=new Map();this.now=now;}
 key(k,id){return k+':'+id;}
 async put(k,id,value,seconds){const key=this.key(k,id);if(await this.get(k,id))return null;this.rows.set(key,{value,until:this.now()+seconds*1000});return 'OK';}
 async get(k,id){const key=this.key(k,id),row=this.rows.get(key);if(!row)return null;if(row.until<=this.now()){this.rows.delete(key);return null;}return row.value;}
 async take(k,id){const value=await this.get(k,id);this.rows.delete(this.key(k,id));return value;}
 async remove(k,id){return this.rows.delete(this.key(k,id));}
 async removeIfValue(k,id,value){const key=this.key(k,id),row=this.rows.get(key);if(!row||row.until<=this.now()||row.value!==value)return 0;this.rows.delete(key);return 1;}
 async publishScoped(id,value,seconds){const live=kind=>{const row=this.rows.get(this.key(kind,id));return row&&row.until>this.now();};const session=[...this.rows].some(([key,row])=>key.startsWith('session:')&&row.until>this.now()&&createHash('sha256').update(key.slice('session:'.length)).digest('hex')===id);if(live('sqlclosed')||!session)return -1;if(live('sqlbinding'))return 0;this.rows.set(this.key('sqlbinding',id),{value,until:this.now()+seconds*1000});return 1;}
 async touch(k,id,seconds){if(!await this.get(k,id))return 0;this.rows.get(this.key(k,id)).until=this.now()+seconds*1000;return 1;}
 async rate(){return true;}
 async ping(){return 'PONG';}
 async close(){}
}
export const bind=server=>new Promise(resolve=>server.listen(0,'127.0.0.1',()=>resolve('http://127.0.0.1:'+server.address().port)));
export const cookies=response=>response.headers.getSetCookie().filter(x=>!x.includes('Max-Age=0')).map(x=>x.split(';')[0]).join('; ');
export async function provider(t){
 const pair=await generateKeyPair('RS256'),other=await generateKeyPair('RS256'),pub={...await exportJWK(pair.publicKey),kid:'fixture',alg:'RS256'},codes=new Map(),access=new Map();
 const p={active:true,verified:true,banned:false,subject:'user_a',wrongIssuer:false,wrongAudience:false,wrongNonce:false,badSignature:false,missingPKCE:false,requests:[],tokenExchanges:0};
 const clientAuthenticated=(req,form)=>form.get('client_id')==='fixture-client'&&form.get('client_secret')==='fixture-client-secret';
 const json=(res,data,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(data));};
 const server=http.createServer(async(req,res)=>{
  try{
   const url=new URL(req.url,p.issuer);p.requests.push(url.pathname);
   if(url.pathname==='/.well-known/openid-configuration')return json(res,{issuer:p.issuer,authorization_endpoint:p.issuer+'/authorize',token_endpoint:p.issuer+'/token',userinfo_endpoint:p.issuer+'/userinfo',jwks_uri:p.issuer+'/jwks',introspection_endpoint:p.issuer+'/introspect',response_types_supported:['code'],grant_types_supported:['authorization_code'],subject_types_supported:['public'],id_token_signing_alg_values_supported:['RS256'],token_endpoint_auth_methods_supported:['client_secret_basic'],code_challenge_methods_supported:['S256']});
   if(url.pathname==='/jwks')return json(res,{keys:[pub]});
   if(url.pathname==='/authorize'){
    const code=randomBytes(16).toString('hex');codes.set(code,Object.fromEntries(url.searchParams));const target=new URL(url.searchParams.get('redirect_uri'));target.searchParams.set('code',code);target.searchParams.set('state',url.searchParams.get('state'));res.writeHead(303,{Location:target.href});return res.end();
   }
   if(url.pathname==='/token'){
    let raw='';for await(const chunk of req)raw+=chunk;const form=new URLSearchParams(raw),code=codes.get(form.get('code'));codes.delete(form.get('code'));p.tokenExchanges++;
    if(!code||!clientAuthenticated(req,form)||form.get('redirect_uri')!==code.redirect_uri||form.get('grant_type')!=='authorization_code'||code.code_challenge!==createHash('sha256').update(form.get('code_verifier')||'').digest('base64url'))return json(res,{error:'invalid_grant'},400);
    const token=randomBytes(20).toString('hex');access.set(token,p.subject);
    const id=await new SignJWT({nonce:p.wrongNonce?'wrong-nonce':code.nonce,email:'unit@qq.com',email_verified:p.verified}).setIssuer(p.wrongIssuer?'https://wrong.invalid':p.issuer).setAudience(p.wrongAudience?'wrong-client':'fixture-client').setSubject(p.subject).setIssuedAt().setExpirationTime('8h').setProtectedHeader({alg:'RS256',kid:'fixture'}).sign(p.badSignature?other.privateKey:pair.privateKey);
    return json(res,{access_token:token,token_type:'Bearer',expires_in:28800,id_token:id});
   }
   if(url.pathname==='/introspect'){
    let raw='';for await(const chunk of req)raw+=chunk;
    if(!clientAuthenticated(req,new URLSearchParams(raw)))return json(res,{error:'invalid_client'},401);
    const sub=access.get(new URLSearchParams(raw).get('token'));return json(res,{active:!!sub&&p.active&&!p.banned,client_id:'fixture-client',sub});
   }
   if(url.pathname==='/userinfo'){const sub=access.get((req.headers.authorization||'').slice(7));return json(res,{sub:p.wrongUserinfo?'foreign_user':sub,email:'unit@qq.com',email_verified:p.verified,name:'Example'},sub?200:401);}
   return json(res,{error:'not_found'},404);
  }catch{json(res,{error:'fixture_protocol_failure'},500);}
 });
 p.issuer=await bind(server);
 p.login=async(app,{mutateCallback}={})=>{
  const start=await fetch(app+'/auth/login',{redirect:'manual'});assert.equal(start.status,303);const tx=cookies(start);
  const authorization=await fetch(start.headers.get('location'),{redirect:'manual'});assert.equal(authorization.status,303);const callback=new URL(authorization.headers.get('location'));mutateCallback?.(callback);
  const result=await fetch(app+callback.pathname+callback.search,{redirect:'manual',headers:{Cookie:tx}});
  return {start,callback,result,cookie:cookies(result),transactionCookie:tx};
 };
 t.after(async()=>{server.closeAllConnections();await new Promise(r=>server.close(r));});return p;
}
