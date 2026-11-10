import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';
import * as core from '../src/core.js';

async function shell(){
 const nodes=new Map(),handlers={};
 class Node{
  constructor(id){this.id=id;this.value='';this.classList={toggle:()=>false,remove(){}};this.parentElement={append:n=>nodes.set(n.textContent,n)};}
  set innerHTML(value){this.html=value;if(this.id==='app')for(const k of [...nodes.keys()])if(k!=='app')nodes.delete(k);for(const m of value.matchAll(/id="([^"]+)"/g))nodes.set(m[1],new Node(m[1]));for(const m of value.matchAll(/id="([^"]+)"[^>]*>([^<]*)/g))nodes.get(m[1]).textContent=m[2];}
  get innerHTML(){return this.html||'';}setAttribute(){}addEventListener(){}
 }
 const root=new Node('app');nodes.set('app',root);const navigations=[];
 const document={querySelector:s=>nodes.get(s.slice(1))||null,querySelectorAll:()=>[],addEventListener(){},createElement:()=>new Node('created')};
 const context=vm.createContext({console,URL,URLSearchParams,AbortController,AbortSignal,Blob,Headers,Response,DOMException,setTimeout,clearTimeout,document,location:{origin:'https://mesh.example.com',hash:'',search:'',assign:url=>navigations.push(url)},sessionStorage:{getItem:()=>null,setItem(){}},window:{addEventListener:(event,fn)=>handlers[event]=fn},fetch:()=>{throw Error('unexpected fetch');}});
 const source=(await readFile(new URL('../src/main.js',import.meta.url),'utf8')).replace(/start\(\);\s*$/,'globalThis.exposed={app,draw,refreshSession,resetIdentity,loadIdentity,signOut};');
 const mod=new vm.SourceTextModule(source,{context}),product={id:'test',title:'Test',nav:[{id:'home',label:'Home'}]};
 const stub=exports=>new vm.SyntheticModule(Object.keys(exports),function(){for(const [key,value]of Object.entries(exports))this.setExport(key,value);},{context});
 await mod.link(spec=>spec==='./style.css'?stub({}):spec==='./core.js'?stub(core):stub({product,renderPage:()=>'<p>Home</p>',mountPage(){},previewRequest(){throw Error('unexpected preview');}}));await mod.evaluate();
 const exposed=context.exposed;exposed.app.config={auth_configured:true,email_delivery:'test_sink'};
 return {...exposed,nodes,handlers,context,navigations};
}
const identity=subject=>({subject,csrf_token:subject.padEnd(43,'x'),session_binding:(subject==='user_b'?'b':'a').repeat(64)});
const reply=(data,options={})=>Response.json(data,{...options,headers:{'X-Console-Session':identity(data.subject||'user_a').session_binding}});
test('email login redirects to the same-origin OIDC entry without loading a browser auth SDK',async()=>{const f=await shell();f.draw();f.nodes.get('login').onclick();assert.match(f.navigations[0],/^\/auth\/login\?return_to=/);assert.equal(f.app.clerk,undefined);});
test('unconfigured email is explicit and disables the login action',async()=>{const f=await shell();f.app.config.email_delivery='unconfigured';f.draw();assert.equal(f.nodes.get('login').disabled,true);assert.match(f.nodes.get('page-content').innerHTML,/尚未配置/);});
test('identity without a product grant keeps an exit and permission-recheck path',async()=>{const f=await shell();f.app.identity=identity('user_a');f.draw();assert.match(f.nodes.get('page-content').innerHTML,/重新检查产品权限/);assert.equal(f.nodes.get('login').onclick,f.loadIdentity);assert.ok(f.nodes.get('退出当前控制台'));});
test('cookie requests send CSRF only on writes and never expose a Bearer token',async()=>{const f=await shell();f.app.identity=identity('user_a');const calls=[];f.context.fetch=async(p,o)=>{calls.push(o);return reply({ok:true});};await f.app.request('/api/session');await f.app.request('/api/backend/v1/images',{method:'POST',body:new Blob(['png'])});assert.equal(calls[0].credentials,'same-origin');assert.equal(calls[0].headers.Authorization,undefined);assert.equal(calls[0].headers['X-CSRF-Token'],undefined);assert.equal(calls[1].headers['X-CSRF-Token'],f.app.identity.csrf_token);});
test('out-of-order product session results cannot replace a different identity',async()=>{const f=await shell(),pending=[];f.context.fetch=async()=>new Promise(r=>pending.push(r));f.app.identity=identity('user_a');const a=f.refreshSession();f.resetIdentity();f.app.identity=identity('user_b');const b=f.refreshSession();pending[1](reply({subject:'user_b'}));await b;pending[0](reply({subject:'user_a'}));await a;assert.equal(f.app.session.subject,'user_b');});
test('late 401 and delayed error bodies cannot clear the new identity',async()=>{const f=await shell();let resolve;f.app.identity=identity('user_a');f.context.fetch=async()=>({ok:false,status:401,headers:new Headers(),json:()=>new Promise(r=>resolve=r)});const a=f.refreshSession();await new Promise(setImmediate);f.resetIdentity();f.app.identity=identity('user_b');f.context.fetch=async()=>reply({subject:'user_b'});await f.refreshSession();resolve({code:'session_invalid'});await a;assert.equal(f.app.session.subject,'user_b');});
test('revoked product access clears business data but preserves the identity needed to log out',async()=>{const f=await shell();f.app.identity=identity('user_a');f.app.session={subject:'user_a'};f.app.data={private_report:'old'};f.context.fetch=async()=>reply({code:'product_access_required'},{status:403});await assert.rejects(f.app.request('/api/backend/jobs'));assert.equal(f.app.session,null);assert.equal(f.app.data.private_report,undefined);assert.equal(f.app.identity.subject,'user_a');assert.ok(f.nodes.get('退出当前控制台'));});
test('confirmed logout clears identity while uncertain logout remains recoverable',async()=>{const f=await shell();f.app.identity=identity('user_a');f.app.session={subject:'user_a'};f.app.data={private_report:'old'};f.context.fetch=async()=>reply({code:'service_unavailable'},{status:503});await f.signOut();assert.equal(f.app.session,null);assert.equal(f.app.data.private_report,undefined);assert.ok(f.app.identity);assert.match(f.app.authError,/尚未确认/);f.context.fetch=async()=>new Response(null,{status:204,headers:{'X-Console-Session':'a'.repeat(64)}});await f.signOut();assert.equal(f.app.identity,null);});
test('late identity bootstrap cannot sign back in after local logout',async()=>{const f=await shell();let resolve;f.context.fetch=async()=>new Promise(r=>resolve=r);const pending=f.loadIdentity();f.resetIdentity();resolve(reply(identity('user_a')));await pending;assert.equal(f.app.identity,null);});
test('a cookie changed in another tab cannot render a different user under the old workspace',async()=>{const f=await shell();f.app.identity=identity('user_a');f.app.session={subject:'user_a'};f.app.data={private_report:'old'};f.context.fetch=async()=>reply({subject:'user_b',private_report:'foreign'});await assert.rejects(f.app.request('/api/backend/jobs'),{name:'AbortError'});assert.equal(f.app.identity,null);assert.equal(f.app.data.private_report,undefined);assert.match(f.app.authError,/其他页面变化/);});
test('successful responses without session association are refused',async()=>{const f=await shell();f.app.identity=identity('user_a');f.context.fetch=async()=>Response.json({ok:true});await assert.rejects(f.app.request('/api/backend/jobs'),{name:'AbortError'});assert.equal(f.app.identity,null);});
