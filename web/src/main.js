import './style.css';
import {esc,apiError,delay} from './core.js';
import {product,renderPage,mountPage,previewRequest} from './product.js';

const icons={grid:'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z',chat:'M21 11a8 8 0 0 1-8 8H5l-3 3V11a9 9 0 0 1 19 0Z',key:'M14 10a5 5 0 1 0-4 4L3 21H0v-3l7-7 M17 7h.01',chart:'M4 20V10 M10 20V4 M16 20v-8 M22 20H2',code:'m8 5-7 7 7 7 m8-14 7 7-7 7 m-3-17-2 20',book:'M3 4h6a4 4 0 0 1 3 2 4 4 0 0 1 3-2h6v16h-6a4 4 0 0 0-3 2 4 4 0 0 0-3-2H3Z M12 6v16',shield:'M12 2 3 6v6c0 5 9 10 9 10s9-5 9-10V6Z m-5 10 3 3 6-6',arrow:'M5 12h14 m-6-6 6 6-6 6',plus:'M12 4v16 M4 12h16',refresh:'M20 7a8 8 0 1 0 0 10 M20 2v6h-6',menu:'M3 6h18 M3 12h18 M3 18h18',link:'M10 13a5 5 0 0 0 7 0l4-4a5 5 0 0 0-7-7l-3 3 M14 11a5 5 0 0 0-7 0l-4 4a5 5 0 0 0 7 7l3-3',file:'M14 2H4v20h16V8Z M14 2v6h6 M8 13h8 M8 17h6',logout:'M9 3H3v18h6 M10 12h12 m-5-5 5 5-5 5',image:'M3 3h18v18H3z m0 14 5-6 5 5 3-3 5 5 M16 7h.01'};
export const icon=name=>`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${icons[name]||icons.grid}"/></svg>`;
const app={product,preview:new URLSearchParams(location.search).get('preview')==='1',config:null,session:null,clerk:null,page:'',view:null,data:{},icon,esc,delay,identityEpoch:0,sessionRevision:0,identityAbort:new AbortController(),authMounted:false};
function resetIdentity(){app.identityEpoch++;app.identityAbort.abort();app.identityAbort=new AbortController();app.view?.abort();app.session=null;app.data={};app.authError='';}
function identityCurrent(epoch,session){return epoch===app.identityEpoch&&session===app.clerk?.session;}
app.isCurrent=view=>view===app.view&&!view.signal.aborted;
app.error=(target,error)=>{if(target)target.innerHTML=`<div class="error" role="alert">${esc(error.name==='TypeError'?'页面数据暂无法处理，请刷新后重试。':error.name==='SyntaxError'?'JSON 格式不正确，请检查输入或重新读取数据。':error.message||'操作未完成')}</div>`;};
app.request=async(p,options={})=>{
 if(app.preview){await delay(180,options.signal);return previewRequest(p,options,app);}
 if(!app.clerk?.session)throw apiError({code:'login_required'},401);
 const identity=app.clerk.session,epoch=app.identityEpoch;
 const token=await identity.getToken();if(!identityCurrent(epoch,identity))throw new DOMException('身份已变化','AbortError');if(!token)throw apiError({code:'session_invalid'},401);
 const {raw,...rest}=options,headers={...rest.headers,Authorization:'Bearer '+token};
 if(rest.body&&typeof rest.body!=='string'&&!(rest.body instanceof Blob)){rest.body=JSON.stringify(rest.body);headers['Content-Type']='application/json';}
 const signal=AbortSignal.any([app.identityAbort.signal,...(rest.signal?[rest.signal]:[]),AbortSignal.timeout(raw?130000:30000)]);
 const response=await fetch(p,{...rest,headers,signal,credentials:'same-origin'});
 if(!identityCurrent(epoch,identity))throw new DOMException('身份已变化','AbortError');
 if(!response.ok){let error={};try{error=await response.json();}catch{}if(!identityCurrent(epoch,identity))throw new DOMException('身份已变化','AbortError');const failure=apiError(error,response.status);if(response.status===401||(response.status===403&&['product_access_required','email_not_verified'].includes(failure.code))){resetIdentity();app.authError=failure.message;draw();}throw failure;}
 if(raw)return response;if(response.status===204)return {data:null,headers:response.headers};
 const data=await response.json();if(!identityCurrent(epoch,identity))throw new DOMException('身份已变化','AbortError');return {data,headers:response.headers};
};
app.backend=(p,options={})=>app.request('/api/backend'+p,options);
app.head=(overline,title,description,actions='')=>`<div class="pagehead"><div><div class="eyebrow">${esc(overline)}</div><h1>${esc(title)}</h1><p>${esc(description)}</p></div>${actions?`<div class="actions">${actions}</div>`:''}</div>`;
app.button=(label,id,primary=false,ic='')=>`<button type="button" class="btn ${primary?'primary':''}" id="${id}">${ic?icon(ic):''}${esc(label)}</button>`;
app.panel=(title,body,extra='')=>`<section class="panel"><div class="panel-head"><h2>${esc(title)}</h2>${extra}</div><div class="panel-body">${body}</div></section>`;
app.download=(data,name)=>{const url=URL.createObjectURL(new Blob([JSON.stringify(data,null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);};
app.rememberJob=id=>{if(!app.data.jobs)app.data.jobs=[];if(!app.data.jobs.includes(id))app.data.jobs.unshift(id);app.data.jobs=app.data.jobs.slice(0,30);if(!app.preview)try{sessionStorage.setItem(product.id+':jobs:'+app.session.subject,JSON.stringify(app.data.jobs));}catch{}};
function currentPage(){const page=location.hash.slice(1).split('?')[0];return product.nav.some(n=>n.id===page)?page:product.nav[0].id;}
function shell(){
 const current=product.nav.find(n=>n.id===app.page),session=app.session;
 const peer=app.config?.peer_url?app.config.peer_url+(app.preview?'/?preview=1':'/'):'#';
 return `<div class="layout"><aside class="sidebar" id="sidebar" aria-label="主导航"><a class="brand" href="#${product.nav[0].id}"><span class="mark">${icon(product.icon)}</span><span>${esc(product.title)}<small>${esc(product.subtitle)}</small></span></a><div class="workspace"><div class="eyebrow">${app.preview?'PREVIEW WORKSPACE':'WORKSPACE'}</div><strong>${esc(app.preview?'示例工作空间':session?.tenant_id||'尚未连接')}</strong><div class="subtle">${esc(app.preview?'公开示例 · 非实时数据':session?.project_id||'模型接入与用量管理')}</div></div><nav class="nav-group"><span class="eyebrow">${esc(product.group)}</span>${product.nav.filter(n=>app.preview||(!n.platform_admin||session?.platform_admin)&&(!n.roles||n.roles.includes(session?.role))).map(n=>`<a class="nav ${n.id===app.page?'active':''}" ${n.id===app.page?'aria-current="page"':''} href="#${n.id}">${icon(n.icon)}${esc(n.label)}</a>`).join('')}</nav><div class="sidebar-bottom"><a class="peer" href="${esc(peer)}"><span><strong>${esc(product.peer)}</strong><span class="subtle">打开独立控制台</span></span>${icon('arrow')}</a><div class="user"><div class="avatar">${app.preview?'预':esc((session?.name||'访').slice(0,1))}</div><div><div class="user-name">${esc(app.preview?'界面预览':session?.name||'访客')}</div><div class="subtle">${esc(app.preview?'示例数据':session?.role||'请先登录')}</div></div>${session&&!app.preview?`<button id="logout" aria-label="退出登录">${icon('logout')}</button>`:''}</div></div></aside><main class="main"><header class="topbar"><div class="breadcrumbs"><button class="btn ghost mobile-toggle" id="menu-toggle" aria-label="展开导航" aria-expanded="false">${icon('menu')}</button><span>${esc(product.title)}</span><span>/</span><b>${esc(current.label)}</b></div><div class="top-actions"><span class="mode"><span class="dot"></span>${app.preview?'界面示例':session?'已验证身份':'等待连接'}</span><a class="btn small" href="${esc(product.repo)}" target="_blank" rel="noopener noreferrer">源码 ${icon('link')}</a></div></header><div class="content">${app.preview?'<div class="notice preview"><span><strong>界面预览</strong> · 所有数字、报告和回答均为示例；不调用模型、不保存业务数据。</span><a href="/">返回真实登录 →</a></div>':''}<div id="page-content"></div><footer class="footer"><span>${esc(product.title)} / ${esc(product.tagline)}</span><span>独立部署 · 共享身份 · 权限分别管理</span></footer></div></main></div><dialog class="modal" id="modal"></dialog>`;
}
function showLogin(message=''){
 if(app.authMounted){app.clerk?.unmountSignIn(document.querySelector('#clerk-signin'));app.authMounted=false;}
 document.querySelector('#page-content').innerHTML=app.head('ACCOUNT ACCESS',product.title+' 控制台','使用同一个账号访问两个产品，业务权限分别管理。')+`<section class="panel login-card"><div class="panel-body"><div class="login-icon">${icon('shield')}</div><h2>${app.config?.auth_configured?'登录你的工作空间':'控制台已就绪，等待身份配置'}</h2><p>${esc(product.loginDescription)}</p>${message?`<div class="error" role="alert">${esc(message)}</div>`:''}${!app.config?.auth_configured?'<div class="notice">共享 Clerk 登录尚未配置。你可以先查看界面示例；真实业务操作需要登录与产品授权。</div>':''}<div class="actions" style="margin-top:20px">${app.button('登录 / 注册','login',true,'arrow')}<a class="btn" href="/?preview=1">浏览界面示例</a></div><div id="login-error" class="inline-result"></div><div id="clerk-signin" style="margin-top:25px"></div><div class="divider"></div><div class="subtle">${icon('shield')} 邮箱验证由共享身份服务完成。模型权限、余额和项目角色由各产品管理。</div></div></section>`;
 document.querySelector('#login').disabled=!app.config?.auth_configured;
 const logged=!!app.clerk?.session;
 if(logged){document.querySelector('#login').textContent='重新检查产品权限';document.querySelector('#login').onclick=refreshSession;}
 else document.querySelector('#login').onclick=()=>{if(app.authMounted)return;if(app.clerk){try{app.authMounted=true;app.clerk.mountSignIn(document.querySelector('#clerk-signin'),{routing:'hash',fallbackRedirectUrl:location.origin});}catch(e){app.authMounted=false;app.error(document.querySelector('#login-error'),Error('登录组件暂不可用，请重试或重新加载页面。'));}}else loadIdentity();};
 if(logged){const exit=document.createElement('button');exit.className='btn';exit.textContent='退出并切换账号';exit.onclick=signOut;document.querySelector('#login').parentElement.append(exit);}
}
async function signOut(){resetIdentity();try{await app.clerk.signOut();}catch(e){app.authError='退出尚未确认，请重试。';}draw();}
function draw(){
 if(app.authMounted){app.clerk?.unmountSignIn(document.querySelector('#clerk-signin'));app.authMounted=false;}
 app.view?.abort();app.view=new AbortController();app.page=currentPage();document.title=product.title+' · '+product.nav.find(n=>n.id===app.page).label;
 document.querySelector('#app').innerHTML=shell();
 const menu=document.querySelector('#menu-toggle');menu.onclick=()=>{const on=document.querySelector('#sidebar').classList.toggle('open');menu.setAttribute('aria-expanded',String(on));};
 document.addEventListener('keydown',e=>{if(e.key==='Escape'){document.querySelector('#sidebar')?.classList.remove('open');menu.setAttribute('aria-expanded','false');}},{signal:app.view.signal});
 const logout=document.querySelector('#logout');if(logout)logout.onclick=signOut;
 if(!app.session&&!app.preview){showLogin(app.authError);return;}
 const area=document.querySelector('#page-content'),view=app.view;area.innerHTML=renderPage(app);
 Promise.resolve(mountPage(app,view)).catch(e=>{if(e.name!=='AbortError'&&app.isCurrent(view))app.error(area,e);});
}
async function refreshSession(){
 const epoch=app.identityEpoch,identity=app.clerk?.session,revision=++app.sessionRevision,current=()=>identityCurrent(epoch,identity)&&revision===app.sessionRevision;
 try{const {data}=await app.request('/api/session');if(!current())return;const old=app.session?.subject;if(old!==data.subject){app.view?.abort();app.data={};}app.session=data;app.authError='';try{app.data.jobs=JSON.parse(sessionStorage.getItem(product.id+':jobs:'+data.subject)||'[]').filter(x=>/^[A-Za-z0-9_-]{1,64}$/.test(x)).slice(0,30);}catch{app.data.jobs=[];}}
 catch(e){if(!current()||e.name==='AbortError')return;app.view?.abort();app.session=null;app.data={};app.authError=e.message;}if(current())draw();
}
async function loadIdentity(){
 try{
  const {Clerk}=await import('@clerk/clerk-js');app.clerk=new Clerk(app.config.clerk_publishable_key);
  await Promise.race([app.clerk.load(),delay(25000).then(()=>{throw Error('身份服务连接超时，请重试。');})]);
  let sessionID=app.clerk.session?.id||null;
  app.identityUnsubscribe?.();app.identityUnsubscribe=app.clerk.addListener(({session})=>{const next=session?.id||null;if(next!==sessionID){sessionID=next;resetIdentity();if(next)refreshSession();else draw();}});
  if(app.clerk.session)await refreshSession();else showLogin();
 }catch(e){showLogin(e.message);const login=document.querySelector('#login');if(login){login.disabled=false;login.textContent='重试登录服务';login.onclick=loadIdentity;}}
}
async function start(){
 try{const r=await fetch('/api/config',{signal:AbortSignal.timeout(10000)});if(!r.ok)throw Error('无法读取控制台配置');app.config=await r.json();draw();if(!app.preview&&app.config.auth_configured)await loadIdentity();}
 catch(e){document.querySelector('#app').innerHTML=`<main class="content"><section class="panel login-card"><div class="panel-body"><h1>控制台暂时无法连接</h1><p>${esc(e.message)}</p><button class="btn primary" id="retry-start">重新连接</button></div></section></main>`;document.querySelector('#retry-start').onclick=start;}
}
window.addEventListener('hashchange',()=>{if(!app.preview&&!app.session&&app.authMounted&&!product.nav.some(n=>location.hash==='#'+n.id))return;draw();});start();
