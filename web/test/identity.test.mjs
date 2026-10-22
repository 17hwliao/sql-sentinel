import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';
import * as core from '../src/core.js';

async function shell(){
 const nodes=new Map(),handlers={};
 class Node{
  constructor(id){this.id=id;this.value='';this.classList={toggle:()=>false,remove(){}};this.parentElement={append:n=>nodes.set(n.textContent,n)};}
  set innerHTML(value){this.html=value;if(this.id==='app')for(const k of [...nodes.keys()])if(k!=='app')nodes.delete(k);for(const m of value.matchAll(/id="([^"]+)"/g))nodes.set(m[1],new Node(m[1]));}
  get innerHTML(){return this.html||'';}setAttribute(){}addEventListener(){}
 }
 const root=new Node('app');nodes.set('app',root);
 const document={querySelector:s=>nodes.get(s.slice(1))||null,querySelectorAll:()=>[],addEventListener(){},createElement:()=>new Node('created')};
 const context=vm.createContext({console,URL,URLSearchParams,AbortController,AbortSignal,Blob,Headers,Response,DOMException,setTimeout,clearTimeout,document,location:{origin:'https://mesh.example.com',hash:'',search:''},sessionStorage:{getItem:()=>null,setItem(){}},window:{addEventListener:(event,fn)=>handlers[event]=fn},fetch:()=>{throw Error('unexpected fetch');}});
 const source=(await readFile(new URL('../src/main.js',import.meta.url),'utf8')).replace(/start\(\);\s*$/,'globalThis.exposed={app,draw,refreshSession,resetIdentity};');
 const mod=new vm.SourceTextModule(source,{context}),product={id:'test',title:'Test',nav:[{id:'home',label:'Home'}]};
 const stub=exports=>new vm.SyntheticModule(Object.keys(exports),function(){for(const [key,value]of Object.entries(exports))this.setExport(key,value);},{context});
 await mod.link(spec=>spec==='./style.css'?stub({}):spec==='./core.js'?stub(core):stub({product,renderPage:()=>'<p>Home</p>',mountPage(){},previewRequest(){throw Error('unexpected preview');}}));await mod.evaluate();
 const {app,draw,refreshSession,resetIdentity}=context.exposed;app.config={auth_configured:true};let mounts=0;
 app.clerk={session:null,user:null,mountSignIn:node=>{mounts++;node.innerHTML='verification form';},unmountSignIn(){},signOut:async()=>{app.clerk.session=null;}};
 return {app,draw,refreshSession,resetIdentity,nodes,handlers,context,mounts:()=>mounts};
}
test('Clerk hash navigation preserves mounted verification form',async()=>{const f=await shell();f.draw();f.nodes.get('login').onclick();f.nodes.get('login').onclick();const container=f.nodes.get('clerk-signin');f.context.location.hash='#/verify';f.handlers.hashchange();assert.equal(f.nodes.get('clerk-signin'),container);assert.equal(container.innerHTML,'verification form');assert.equal(f.mounts(),1);});
test('authenticated identity without product grant can retry and sign out',async()=>{const f=await shell();f.app.clerk.session={id:'sid_a'};f.draw();assert.equal(f.nodes.get('login').textContent,'重新检查产品权限');const button=f.nodes.get('退出并切换账号');assert.ok(button);await button.onclick();assert.equal(f.app.clerk.session,null);assert.equal(f.app.session,null);});
test('out-of-order session results cannot replace the active identity',async()=>{const f=await shell(),pending=[];f.context.fetch=async()=>new Promise(r=>pending.push(r));f.app.clerk.session={id:'a',getToken:async()=>'a'};const a=f.refreshSession();await new Promise(setImmediate);f.resetIdentity();f.app.clerk.session={id:'b',getToken:async()=>'b'};const b=f.refreshSession();await new Promise(setImmediate);pending[1](Response.json({subject:'user_b'}));await b;pending[0](Response.json({subject:'user_a'}));await a;assert.equal(f.app.session.subject,'user_b');});
test('late 401 for old identity cannot clear the new session',async()=>{const f=await shell(),pending=[];f.context.fetch=async()=>new Promise(r=>pending.push(r));f.app.clerk.session={id:'a',getToken:async()=>'a'};const a=f.refreshSession();await new Promise(setImmediate);f.resetIdentity();f.app.clerk.session={id:'b',getToken:async()=>'b'};const b=f.refreshSession();await new Promise(setImmediate);pending[1](Response.json({subject:'user_b'}));await b;pending[0](Response.json({code:'session_invalid'},{status:401}));await a;assert.equal(f.app.session.subject,'user_b');});
test('identity change during getToken prevents sending a stale request',async()=>{const f=await shell();let resolve,calls=0;f.app.clerk.session={getToken:()=>new Promise(r=>resolve=r)};f.context.fetch=()=>{calls++;throw Error('must not send');};const request=f.app.request('/api/session');f.resetIdentity();f.app.clerk.session={id:'b'};resolve('old-token');await assert.rejects(request,{name:'AbortError'});assert.equal(calls,0);});

test('identity is checked again after a delayed error body is decoded',async()=>{const f=await shell();let resolve;f.app.clerk.session={id:'a',getToken:async()=>'a'};f.context.fetch=async()=>({ok:false,status:401,json:()=>new Promise(r=>resolve=r)});const a=f.refreshSession();await new Promise(setImmediate);f.resetIdentity();f.app.clerk.session={id:'b',getToken:async()=>'b'};f.context.fetch=async()=>Response.json({subject:'user_b'});await f.refreshSession();resolve({code:'session_invalid'});await a;assert.equal(f.app.session.subject,'user_b');});

test('revoked product grants clear business state while keeping an identity exit path',async()=>{const f=await shell();f.app.clerk.session={id:'a',getToken:async()=>'a'};f.app.session={subject:'user_a'};f.app.data={private_report:'previous data'};f.context.fetch=async()=>Response.json({code:'product_access_required'},{status:403});await assert.rejects(f.app.request('/api/backend/jobs'));assert.equal(f.app.session,null);assert.equal(f.app.data.private_report,undefined);assert.ok(f.nodes.get('退出并切换账号'));});
