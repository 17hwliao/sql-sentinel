import test from 'node:test';
import assert from 'node:assert/strict';
import {agentScopeKey,validRun,validAgentConfig,renderAgentRun,renderRunRecord,mountAgentRun,previewAgentRun,isSettled} from '../src/agentrun.js';

const config={enabled:true,version:'outer.test.v1',model_aliases:['allowed-model','other-model'],default_model:'allowed-model',tools_enabled:true,max_model_calls:4,max_jobs:1,run_seconds:180,inner_model_calls:5,inner_evaluations:2,tenant_id:'tenant',project_id:'project',tables:['orders'],model_available:true,task_service_available:true};
const record=(id='run_a',extra={})=>({id,request_id:'req_'+id,sql:'SELECT id FROM orders',model_alias:'allowed-model',tools_enabled:true,config_version:'outer.test.v1',status:'completed',answer:'仅 L1 计划证据，未确认实测收益。',model_summary:false,events:[],model_calls_reserved:2,cost_cny:null,usage_known:false,...extra});
const response=data=>({data,headers:new Headers()});
const tick=()=>new Promise(setImmediate);
function storage(initial={}){const values=new Map(Object.entries(initial));return {values,getItem:key=>values.get(key)||null,setItem:(key,value)=>values.set(key,value)};}
function documentFixture(){
 const nodes=new Map();
 class Node{
  constructor(id){this.id=id;this.value='';this.checked=false;this.disabled=false;this.textContent='';this.html='';this.children=[];this.dataset={};}
  set innerHTML(value){for(const id of this.children)nodes.delete(id);this.children=[];this.html=value;for(const match of value.matchAll(/<(?:[a-z]+)\b([^>]*\bid="([^"]+)"[^>]*)>/g)){const child=new Node(match[2]);child.disabled=/\bdisabled\b/.test(match[1]);nodes.set(child.id,child);this.children.push(child.id);}const buttons=[];for(const match of value.matchAll(/data-agent-run="([^"]+)"/g)){const button=new Node('button_'+match[1]);button.dataset.agentRun=match[1];buttons.push(button);}this.buttons=buttons;}
  get innerHTML(){return this.html;}
 }
 const prior=globalThis.document,root=new Node('root');globalThis.document={querySelector:selector=>nodes.get(selector.slice(1))||null,querySelectorAll:selector=>selector==='[data-agent-run]'?nodes.get('agent-runs-list')?.buttons||[]:[]};
 return {root,nodes,get:id=>nodes.get(id),restore(){if(prior===undefined)delete globalThis.document;else globalThis.document=prior;}};
}
function app(backend){return {preview:false,session:{subject:'alice',tenant_id:'tenant',project_id:'project',role:'writer'},identityEpoch:1,data:{},isCurrent:()=>true,panel:(title,body)=>`<section>${title}${body}</section>`,head:(_,title,description)=>`<h1>${title}</h1><p>${description}</p>`,rememberJob(){},backend};}
const waiting=(_ms,signal)=>new Promise((resolve,reject)=>{const abort=()=>reject(new DOMException('aborted','AbortError'));if(signal.aborted)abort();else signal.addEventListener('abort',abort,{once:true});});
async function setup(backend,options={}){const d=documentFixture(),a=app(backend),view=new AbortController();d.root.innerHTML=renderAgentRun(a);try{await mountAgentRun(a,view,{storage:options.storage||storage(),wait:options.wait||waiting,uuid:()=> '11111111-2222-3333-4444-555555555555'});return {d,a,view,close(){view.abort();d.restore();}};}catch(e){view.abort();d.restore();throw e;}}

test('run response and config require actual IDs and known states',()=>{
 assert.throws(()=>validRun({status:'completed'}));assert.throws(()=>validRun(record('a',{status:'unexpected'})));assert.throws(()=>validRun(record('a',{id:undefined})));assert.equal(validRun(record()).id,'run_a');
 assert.throws(()=>validAgentConfig({enabled:true}));assert.throws(()=>validAgentConfig({...config,model_aliases:['x'.repeat(129)]}));assert.equal(validAgentConfig(config).max_model_calls,4);
});

test('run recovery key binds subject tenant and project without SQL or credentials',()=>{
 const user={subject:'alice',tenant_id:'t',project_id:'p'};assert.notEqual(agentScopeKey(user),agentScopeKey({...user,subject:'bob'}));assert.notEqual(agentScopeKey(user),agentScopeKey({...user,tenant_id:'other'}));assert.notEqual(agentScopeKey(user),agentScopeKey({...user,project_id:'other'}));assert.equal(agentScopeKey({subject:'alice'}),null);
});

test('evidence rendering separates no recommendation, partial, unknown cost and model reservations',()=>{
 const html=renderRunRecord(record('run_a',{status:'failed',stop_reason:'model_failed',job:{delivery_id:'job_001',evidence:{level:'L1',outcome:'no_change',summary:'没有已验证候选',recommended:false,facts:['候选索引已清理']}}}));
 assert.match(html,/已停止/);assert.match(html,/没有候选是合法结果/);assert.match(html,/不代表实测收益/);assert.match(html,/模型派发预留/);assert.match(html,/未知；未取得完整用量/);assert.match(html,/原诊断报告/);assert.doesNotMatch(html,/¥0|0 元|自动审批/);
 const unknown=renderRunRecord(record('unknown',{status:'outcome_unknown'}));assert.match(unknown,/后台可能已执行/);assert.doesNotMatch(unknown,/id="agent-run-cancel"/);
 const noTools=renderRunRecord(record('no_tools',{tools_enabled:false}));assert.match(noTools,/未启用诊断工具/);assert.match(noTools,/不会执行影子验证/);
});

test('untrusted run text is escaped and event and fact content bounded',()=>{
 const bad='<img src=x onerror=secret()>';const html=renderRunRecord(record('safe',{answer:bad+'x'.repeat(20000),events:Array.from({length:100},()=>({kind:bad,message:bad+'y'.repeat(10000)})),job:{delivery_id:bad,evidence:{level:bad,summary:bad,recommended:false,facts:Array(100).fill(bad)}}}));
 assert.doesNotMatch(html,/<img|onerror=secret\(\)>/);assert.match(html,/&lt;img/);assert.match(html,/动作事实记录（24）/);assert.doesNotMatch(html,/id="agent-run-report"/);assert.ok(html.length<30000);
});

test('no SQL is rejected before any POST and deployment defaults reset only choices',async()=>{
 const calls=[];const s=await setup(async(p,o)=>{calls.push([p,o]);return response(p==='/agent/config'?config:{runs:[]});});
 try{s.d.get('agent-run-message').value='帮我查营业额';s.d.get('agent-run-model').value='other-model';s.d.get('agent-run-tools').checked=false;s.d.get('agent-run-defaults').onclick();assert.equal(s.d.get('agent-run-model').value,'allowed-model');assert.equal(s.d.get('agent-run-tools').checked,true);await s.d.get('agent-run-form').onsubmit({preventDefault(){}});assert.equal(calls.some(([,o])=>o?.method==='POST'),false);assert.match(s.d.get('agent-run-input-error').innerHTML,/请提供需要诊断/);assert.equal(s.d.get('agent-run-message').value,'帮我查营业额');}finally{s.close();}
});

test('unknown submit preserves stable request metadata and recovers original run without repeat create',async()=>{
 const cache=storage(),calls=[];let created=false;
 const s=await setup(async(p,o)=>{calls.push([p,o]);if(p==='/agent/config')return response(config);if(p==='/agent/runs'&&o?.method==='POST'){created=true;throw Object.assign(Error('PRIVATE_PROVIDER_KEY'),{status:503});}if(p==='/agent/runs')return response({runs:created?[record('recovered',{request_id:'web_11111111222233334444555555555555'})]:[]});return response(record('recovered',{request_id:'web_11111111222233334444555555555555'}));},{storage:cache});
 try{s.d.get('agent-run-sql').value='SELECT id FROM orders /* PRIVATE_SQL */';s.d.get('agent-run-message').value='PRIVATE_MESSAGE';await s.d.get('agent-run-form').onsubmit({preventDefault(){}});assert.match(s.d.get('agent-run-pending').innerHTML,/尚未确认/);assert.equal(s.d.get('agent-run-submit').disabled,true);assert.doesNotMatch(s.d.get('agent-run-input-error').innerHTML,/PRIVATE_PROVIDER_KEY/);const saved=cache.getItem(agentScopeKey(s.a.session));assert.match(saved,/web_111111/);assert.doesNotMatch(saved,/PRIVATE_SQL|PRIVATE_MESSAGE|SELECT/);await s.d.get('agent-pending-check').onclick();assert.match(s.d.get('agent-run-result').innerHTML,/recovered/);assert.equal(s.d.get('agent-run-submit').disabled,false);assert.equal(calls.filter(([,o])=>o?.method==='POST').length,1);}finally{s.close();}
});

test('double submit produces one stable create while input remains readable',async()=>{
 let settle,posts=0;const s=await setup(async(p,o)=>{if(p==='/agent/config')return response(config);if(p==='/agent/runs'&&o?.method==='POST'){posts++;return new Promise(resolve=>{settle=resolve;});}if(p==='/agent/runs')return response({runs:[]});return response(record('saved'));});
 try{s.d.get('agent-run-sql').value='SELECT id FROM orders';const first=s.d.get('agent-run-form').onsubmit({preventDefault(){}});await s.d.get('agent-run-form').onsubmit({preventDefault(){}});assert.equal(posts,1);assert.equal(s.d.get('agent-run-sql').value,'SELECT id FROM orders');settle(response(record('saved',{request_id:'web_11111111222233334444555555555555'})));await first;assert.match(s.d.get('agent-run-result').innerHTML,/saved/);}finally{s.close();}
});

test('refresh restores selected run only from current scope and a different identity gets none',async()=>{
 const cache=storage({[agentScopeKey({subject:'alice',tenant_id:'tenant',project_id:'project'})]:JSON.stringify({id:'restored',request_id:'req_restored',pending:false}),[agentScopeKey({subject:'bob',tenant_id:'tenant',project_id:'project'})]:JSON.stringify({id:'bob_private',request_id:'req_bob',pending:false})}),calls=[];
 const s=await setup(async p=>{calls.push(p);if(p==='/agent/config')return response(config);if(p==='/agent/runs')return response({runs:[]});return response(record('restored'));},{storage:cache});
 try{assert.ok(calls.includes('/agent/runs/restored'));assert.ok(!calls.some(p=>p.includes('bob_private')));assert.match(s.d.get('agent-run-result').innerHTML,/restored/);}finally{s.close();}
});

test('identity change suppresses late submit result and leaves new view untouched',async()=>{
 let settle;const s=await setup(async(p,o)=>{if(p==='/agent/config')return response(config);if(o?.method==='POST')return new Promise(resolve=>{settle=resolve;});return response({runs:[]});});
 try{s.d.get('agent-run-sql').value='SELECT id FROM orders';const pending=s.d.get('agent-run-form').onsubmit({preventDefault(){}});s.a.session={...s.a.session,subject:'bob'};s.a.identityEpoch++;s.d.get('agent-run-result').innerHTML='NEW_IDENTITY';settle(response(record('old_private')));await pending;assert.equal(s.d.get('agent-run-result').innerHTML,'NEW_IDENTITY');}finally{s.close();}
});

test('out of order run selection never shows old response after newer task',async()=>{
 let oldResolve;const s=await setup(async p=>{if(p==='/agent/config')return response(config);if(p==='/agent/runs')return response({runs:[record('old'),record('new')]});if(p.endsWith('/old'))return new Promise(resolve=>{oldResolve=resolve;});return response(record('new',{answer:'NEW_RESULT'}));});
 try{const buttons=s.d.get('agent-runs-list').buttons;const old=buttons[0].onclick();await buttons[1].onclick();oldResolve(response(record('old',{answer:'OLD_PRIVATE'})));await old;assert.match(s.d.get('agent-run-result').innerHTML,/NEW_RESULT/);assert.doesNotMatch(s.d.get('agent-run-result').innerHTML,/OLD_PRIVATE/);}finally{s.close();}
});

test('leaving only aborts waiting; explicit cancel is one durable write and report link uses owned job',async()=>{
 const calls=[],cache=storage({[agentScopeKey({subject:'alice',tenant_id:'tenant',project_id:'project'})]:JSON.stringify({id:'running',request_id:'req_running'})});
 const s=await setup(async(p,o)=>{calls.push([p,o]);if(p==='/agent/config')return response(config);if(p==='/agent/runs')return response({runs:[]});return response(record('running',{status:'waiting_tool'}));},{storage:cache});
 s.view.abort();await tick();assert.equal(calls.some(([,o])=>o?.method==='POST'),false);s.close();
 let cancelled=false;const writes=[];const second=await setup(async(p,o)=>{if(p==='/agent/config')return response(config);if(p==='/agent/runs')return response({runs:[]});if(p.endsWith('/cancel')){writes.push([p,o]);cancelled=true;}return response(record('running',{status:cancelled?'cancelled':'waiting_tool',job:{delivery_id:'owned_job'}}));},{storage:cache});
 try{second.d.get('agent-run-report').onclick();assert.equal(second.a.data.currentJob,'owned_job');await second.d.get('agent-run-cancel').onclick();assert.equal(writes.length,1);assert.equal(writes[0][1].method,'POST');assert.match(second.d.get('agent-run-result').innerHTML,/已取消/);}finally{second.close();}
});

test('preview is explicitly labelled and never pretends a live task was executed',()=>{
 const a={data:{}};const result=previewAgentRun('/api/backend/agent/runs/example',{},a).data;assert.equal(result.status,'completed');assert.match(result.answer,/界面示例/);assert.match(result.answer,/没有执行真实模型/);assert.equal(result.cost_cny,null);
});

test('a pending request survives reload and selecting an older run cannot clear its uncertainty',async()=>{
 const cache=storage({[agentScopeKey({subject:'alice',tenant_id:'tenant',project_id:'project'})]:JSON.stringify({request_id:'original_request',pending:true})});
 const s=await setup(async p=>p==='/agent/config'?response(config):p==='/agent/runs'?response({runs:[record('old')]}):response(record('old')),{storage:cache});
 try{await s.d.get('agent-runs-list').buttons[0].onclick();assert.equal(s.d.get('agent-run-submit').disabled,true);assert.match(s.d.get('agent-run-pending').innerHTML,/original_request/);assert.equal(JSON.parse(cache.getItem(agentScopeKey(s.a.session))).pending,true);}finally{s.close();}
});

test('failed configuration and a different project scope cannot enable submit',async()=>{
 for(const setting of [{...config,project_id:'another_project'},null]){
  const s=await setup(async p=>{if(p==='/agent/config'){if(setting)return response(setting);throw Error('PRIVATE_DSN');}return response({runs:[]});});
  try{assert.equal(s.d.get('agent-run-submit').disabled,true);assert.match(s.d.get('agent-run-config').innerHTML,/尚未完成/);assert.doesNotMatch(s.d.get('agent-run-config').innerHTML,/PRIVATE_DSN|another_project/);}finally{s.close();}
 }
});

test('list refresh errors retain visible records and do not claim there are no runs',async()=>{
 let fail=false;const s=await setup(async p=>{if(p==='/agent/config')return response(config);if(fail)throw Error('PRIVATE_INTERNAL_ERROR');return response({runs:[record('known')]});});
 try{fail=true;await s.d.get('agent-runs-refresh').onclick();assert.match(s.d.get('agent-runs-list').innerHTML,/req_known/);assert.match(s.d.get('agent-runs-error').innerHTML,/读取失败/);assert.doesNotMatch(s.d.get('agent-runs-error').innerHTML,/PRIVATE_INTERNAL_ERROR/);}finally{s.close();}
});

test('a completed poll updates the visible recent run and an in-flight list cannot revert it',async()=>{
 const cache=storage({[agentScopeKey({subject:'alice',tenant_id:'tenant',project_id:'project'})]:JSON.stringify({id:'tracked',request_id:'req_tracked'})});
 const queued=record('tracked',{status:'queued'});let poll,staleList,reads=0,lists=0;
 const s=await setup(async p=>{
  if(p==='/agent/config')return response(config);
  if(p==='/agent/runs')return ++lists===1?response({runs:[queued]}):new Promise(resolve=>{staleList=resolve;});
  return response(++reads===1?queued:record('tracked'));
 },{storage:cache,wait:()=>new Promise(resolve=>{poll=resolve;})});
 try{
  assert.match(s.d.get('agent-runs-list').innerHTML,/等待执行/);
  const refresh=s.d.get('agent-runs-refresh').onclick();poll();await tick();
  assert.match(s.d.get('agent-run-result').innerHTML,/已完成/);
  assert.equal(s.d.get('agent-list-state-tracked').textContent,'已完成');
  staleList(response({runs:[queued]}));await refresh;
  assert.match(s.d.get('agent-runs-list').innerHTML,/已完成/);
 }finally{s.close();}
});

test('a creation response bound to another request stays unknown instead of accepting a wrong run',async()=>{
 const s=await setup(async(p,o)=>p==='/agent/config'?response(config):o?.method==='POST'?response(record('wrong')):response({runs:[]}));
 try{s.d.get('agent-run-sql').value='SELECT id FROM orders';await s.d.get('agent-run-form').onsubmit({preventDefault(){}});assert.equal(s.d.get('agent-run-submit').disabled,true);assert.match(s.d.get('agent-run-pending').innerHTML,/尚未确认/);assert.doesNotMatch(s.d.get('agent-run-result').innerHTML,/req_wrong/);}finally{s.close();}
});

test('exact request recovery finds an older run outside recent thirty without resubmitting',async()=>{
 const cache=storage({[agentScopeKey({subject:'alice',tenant_id:'tenant',project_id:'project'})]:JSON.stringify({request_id:'old_stable_request',pending:true})}),calls=[];
 const recovered=record('old_persisted',{request_id:'old_stable_request'});
 const s=await setup(async(p,o)=>{calls.push([p,o]);if(p==='/agent/config')return response(config);if(p==='/agent/runs')return response({runs:Array.from({length:30},(_,i)=>record('recent_'+i))});return response(recovered);},{storage:cache});
 try{assert.ok(calls.some(([p])=>p==='/agent/runs/by-request/old_stable_request'));assert.equal(calls.some(([,o])=>o?.method==='POST'),false);assert.match(s.d.get('agent-run-result').innerHTML,/old_persisted/);assert.equal(JSON.parse(cache.getItem(agentScopeKey(s.a.session))).pending,false);assert.equal(s.d.get('agent-run-submit').disabled,false);}finally{s.close();}
});

test('exact recovery 404 preserves pending request and never retries creation',async()=>{
 const cache=storage({[agentScopeKey({subject:'alice',tenant_id:'tenant',project_id:'project'})]:JSON.stringify({request_id:'maybe_inflight',pending:true})}),calls=[];
 const s=await setup(async(p,o)=>{calls.push([p,o]);if(p==='/agent/config')return response(config);if(p.includes('/by-request/'))throw Object.assign(Error('not_found'),{status:404,code:'agent_run_not_found'});return response({runs:[]});},{storage:cache});
 try{assert.match(s.d.get('agent-pending-result').innerHTML,/暂未找到/);assert.equal(s.d.get('agent-run-submit').disabled,true);await s.d.get('agent-pending-check').onclick();assert.equal(calls.filter(([p])=>p.includes('/by-request/')).length,2);assert.equal(calls.some(([,o])=>o?.method==='POST'),false);assert.equal(JSON.parse(cache.getItem(agentScopeKey(s.a.session))).pending,true);}finally{s.close();}
});

test('late exact recovery cannot replace a newly selected run',async()=>{
 const cache=storage(),calls=[];let resolveRecovery;
 const s=await setup(async(p,o)=>{calls.push([p,o]);if(p==='/agent/config')return response(config);if(p==='/agent/runs'&&o?.method==='POST')throw Object.assign(Error('lost_response'),{status:503});if(p==='/agent/runs')return response({runs:[record('selected')]});if(p.includes('/by-request/'))return new Promise(resolve=>{resolveRecovery=resolve;});return response(record('selected'));},{storage:cache});
 try{s.d.get('agent-run-sql').value='SELECT id FROM orders';await s.d.get('agent-run-form').onsubmit({preventDefault(){}});const pending=s.d.get('agent-pending-check').onclick();await s.d.get('agent-runs-list').buttons[0].onclick();resolveRecovery(response(record('old',{request_id:'web_11111111222233334444555555555555'})));await pending;assert.match(s.d.get('agent-run-result').innerHTML,/req_selected/);assert.doesNotMatch(s.d.get('agent-run-result').innerHTML,/req_old/);assert.equal(s.d.get('agent-run-submit').disabled,true);}finally{s.close();}
});

test('pending cancellation remains observable while settled and unknown outcomes have no cancel action',()=>{
 const pending=record('cancel_pending',{status:'cancelled',stop_reason:'cancel_requested'});
 assert.equal(isSettled(pending),false);const html=renderRunRecord(pending);assert.match(html,/取消已请求，等待任务确认/);assert.doesNotMatch(html,/id="agent-run-cancel"/);
 for(const status of ['cancelled','completed','failed','outcome_unknown']){const value=record('settled',{status,stop_reason:'confirmed'});assert.equal(isSettled(value),true);assert.doesNotMatch(renderRunRecord(value),/id="agent-run-cancel"/);}
});

test('pending child cancellation keeps polling until acknowledgement then stops',async()=>{
 const cache=storage({[agentScopeKey({subject:'alice',tenant_id:'tenant',project_id:'project'})]:JSON.stringify({id:'cancelling',request_id:'req_cancelling'})});
 let reads=0,advance,waits=0;
 const s=await setup(async p=>{if(p==='/agent/config')return response(config);if(p==='/agent/runs')return response({runs:[]});reads++;return response(record('cancelling',{status:'cancelled',stop_reason:reads<3?'cancel_requested':'cancelled'}));},{storage:cache,wait:async()=>{waits++;await new Promise(resolve=>{advance=resolve;});}});
 try{assert.equal(reads,1);assert.equal(waits,1);assert.match(s.d.get('agent-run-result').innerHTML,/等待任务确认/);assert.equal(s.d.get('agent-run-cancel'),undefined);advance();await tick();assert.equal(reads,2);assert.equal(waits,2);assert.match(s.d.get('agent-run-result').innerHTML,/等待任务确认/);advance();await tick();assert.equal(reads,3);assert.equal(waits,2);assert.doesNotMatch(s.d.get('agent-run-result').innerHTML,/取消已请求，等待任务确认/);assert.match(s.d.get('agent-run-result').innerHTML,/已取消/);}finally{s.close();}
});
