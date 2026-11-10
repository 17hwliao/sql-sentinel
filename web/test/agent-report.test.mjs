import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {fileURLToPath,pathToFileURL} from 'node:url';
import path from 'node:path';
import {renderAgentRecord,reportCandidateReady} from '../src/agentreport.js';

const dir=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../src');
const source=(await readFile(path.join(dir,'product.js'),'utf8')).replace(/from '\.\/([\w]+)\.js'/g,(_,name)=>'from '+JSON.stringify(pathToFileURL(path.join(dir,name+'.js')).href));
const product=await import('data:text/javascript;base64,'+Buffer.from(source+'\nexport {renderReport};').toString('base64'));
const spec={table:'orders',index_name:'idx_cand_server',columns:[{name:'user_id',direction:'ASC'}]};
const report=outcome=>({mode:'agent',version:'agent.v1',outcome,stop_reason:'test_stop',candidate_spec:outcome==='recommended'?spec:null,agent:{mode:'agent',version:'agent.v1',status:outcome,stop_reason:'test_stop',model_calls:2,evaluations:1,selected_candidate_id:outcome==='recommended'?'e_verified_test_id_1':'',observations:[{id:'e_verified_test_id_1',action:'evaluate_index',status:'ok',verified:true,candidate:spec,candidate_sha256:'c'.repeat(64),facts:{selected_index:true,measured:false}}]},agent_trace:[{sequence:1,action:'evaluate_index',status:'ok',observation_id:'e_verified_test_id_1'}],agent_usage:{model_calls_reserved:2,evaluations_reserved:1}});
const tick=()=>new Promise(setImmediate);

test('old reports remain readable without invented Agent records',()=>{
 const old={candidate_spec:spec,artifacts:{}};
 assert.equal(renderAgentRecord(old),'');assert.equal(reportCandidateReady({status:'COMPLETED'},old),true);
 const html=product.renderReport({panel:(t,b)=>`<section>${t}${b}</section>`},{delivery_id:'old',status:'COMPLETED'},old,'a'.repeat(64));
 assert.match(html,/受限候选方案/);assert.doesNotMatch(html,/Agent 执行记录/);
});

test('Agent report explains verified candidates, reservations and L1 boundaries',()=>{
 const r=report('recommended'),html=renderAgentRecord(r);
 assert.match(html,/<details id="agent-execution-record">/);assert.match(html,/已选定经过验证的候选/);assert.match(html,/e_verified_test_id_1/);assert.match(html,/idx_cand_server/);assert.match(html,/全任务模型调用预留/);assert.match(html,/包含请求结果未知/);assert.match(html,/未提供 token 和费用/);assert.doesNotMatch(html,/¥0|0 token|费用[^<]*0 元/);
 assert.equal(reportCandidateReady({status:'COMPLETED'},r),true);
});

test('no candidate and partial stopped outcomes cannot enable approval or benchmark',()=>{
 for(const outcome of ['no_change','budget_exhausted','timeout','cancelled','protocol_failed','model_failed','infrastructure_failed','authorization_failed','lease_lost']){
  const r=report(outcome),html=renderAgentRecord(r);assert.equal(reportCandidateReady({status:'COMPLETED'},r),false);
  assert.match(html,/test_stop/);assert.match(html,outcome==='no_change'?/没有候选是合法结果/:/部分证据/);
 }
 assert.equal(reportCandidateReady({status:'CANCELLED'},report('recommended')),false);
 const malformed=report('budget_exhausted');malformed.candidate_spec=spec;assert.equal(reportCandidateReady({status:'COMPLETED'},malformed),false);
});

test('untrusted Agent fields and structured facts are escaped and bounded',()=>{
 const r=report('recommended');r.agent.summary='<script>secret()</script>';r.stop_reason='<img src=x onerror=secret()>';r.agent.observations[0].facts={text:'<img onerror=secret()>'+'x'.repeat(15000)};
 const html=renderAgentRecord(r);assert.doesNotMatch(html,/<script>|<img/);assert.match(html,/&lt;script&gt;/);assert.match(html,/显示已截断/);
 const missing=report('no_change');delete missing.agent_usage;delete missing.agent.model_calls;const absent=renderAgentRecord(missing);assert.match(absent,/全任务模型调用预留<\/span><span>未提供/);
});

function dom(){
 const nodes=new Map();
 class Node{
  constructor(id){this.id=id;this.value='';this.disabled=false;this.html='';this.classList={toggle(){}};this.textContent='';}
  set innerHTML(value){this.html=value;for(const match of value.matchAll(/id="([^"]+)"([^>]*)/g)){const node=new Node(match[1]);node.disabled=/\bdisabled\b/.test(match[2]);nodes.set(match[1],node);}}
  get innerHTML(){return this.html;}focus(){}
 }
 const get=id=>{id=id.replace(/^#/,'');if(!nodes.has(id))nodes.set(id,new Node(id));return nodes.get(id);};
 const original=globalThis.document;globalThis.document={querySelector:get,querySelectorAll:()=>[]};
 return {get,restore(){if(original===undefined)delete globalThis.document;else globalThis.document=original;}};
}
function app(backend,page='report'){
 const a={page,preview:false,session:{role:'admin',project_id:'project'},data:{},config:{peer_url:'http://127.0.0.1'},isCurrent:()=>true,rememberJob(){},panel:(t,b)=>`<section>${t}${b}</section>`,head:(a,b,c)=>`${a}${b}${c}`,icon:()=>'',button:()=>'',download(){},error:(node,e)=>{node.innerHTML=e.message;},backend};return a;
}
const response=data=>({data,headers:new Headers({'x-sentinel-report-sha256':'a'.repeat(64)})});

test('diagnosis explicitly opts into Agent; default and manual paths stay fixed',async()=>{
 for(const mode of ['fixed','agent.v1']){
  const d=dom();try{let sent;const a=app(async(p,o)=>{sent=o.body;return response({delivery_id:o.body.delivery_id});},'diagnose'),view=new AbortController();await product.mountPage(a,view);
   d.get('sql-editor').value='SELECT id FROM orders';d.get('candidate-json').value='';d.get('execution-mode').value=mode;await d.get('diagnose-form').onsubmit({preventDefault(){}});
   assert.equal(sent.execution_mode,mode==='agent.v1'?'agent.v1':undefined);
  }finally{d.restore();}
 }
 const d=dom();try{let calls=0;const a=app(async()=>{calls++;return response({});},'diagnose');await product.mountPage(a,new AbortController());d.get('sql-editor').value='SELECT id FROM orders';d.get('execution-mode').value='agent.v1';d.get('candidate-json').value=JSON.stringify(spec);await d.get('diagnose-form').onsubmit({preventDefault(){}});assert.equal(calls,0);assert.match(d.get('action-error').innerHTML,/不接收手动候选/);}finally{d.restore();}
});

test('no candidate report mounts without requesting or enabling approval',async()=>{
 const d=dom();try{const calls=[];const a=app(async p=>{calls.push(p);return p.endsWith('/report')?response(report('no_change')):response({delivery_id:'task_a',status:'COMPLETED'});});a.data.currentJob='task_a';await product.mountPage(a,new AbortController());assert.equal(calls.includes('/jobs/task_a/approval'),false);assert.equal(d.get('approve-candidate').disabled,true);assert.equal(d.get('start-benchmark').disabled,true);assert.match(d.get('approval-state').textContent,/没有可审批/);}finally{d.restore();}
});

test('late task result and approval do not replace the current selected task',async()=>{
 const d=dom();try{
  let oldApproval;const a=app(async p=>{if(p==='/jobs/task_a/approval')return new Promise(resolve=>{oldApproval=resolve;});if(p.endsWith('/report'))return response(p.includes('task_a')?report('recommended'):{...report('no_change'),stop_reason:'TASK_B_ONLY'});return response({delivery_id:p.split('/').at(-1),status:'COMPLETED'});});a.data.currentJob='task_a';await product.mountPage(a,new AbortController());assert.equal(typeof oldApproval,'function');d.get('report-id').value='task_b';d.get('report-lookup').onsubmit({preventDefault(){}});await tick();oldApproval(response({decision:'approved'}));await tick();assert.match(d.get('report-content').innerHTML,/TASK_B_ONLY/);assert.equal(d.get('start-benchmark').disabled,true);assert.match(d.get('approval-state').textContent,/没有可审批/);
 }finally{d.restore();}
 const d2=dom();try{
  let failOld;const a=app(async p=>{if(p==='/jobs/task_a')return new Promise((_,reject)=>{failOld=reject;});if(p.endsWith('/report'))return response({...report('no_change'),stop_reason:'NEW_TASK_ONLY'});return response({delivery_id:'task_b',status:'COMPLETED'});});a.data.currentJob='task_a';const pending=product.mountPage(a,new AbortController());d2.get('report-id').value='task_b';d2.get('report-lookup').onsubmit({preventDefault(){}});await tick();failOld(Error('OLD_PRIVATE_ERROR'));await pending;assert.match(d2.get('report-content').innerHTML,/NEW_TASK_ONLY/);assert.doesNotMatch(d2.get('action-error').innerHTML,/OLD_PRIVATE_ERROR/);
 }finally{d2.restore();}
});

test('identity switch suppresses a late report and never enables its controls',async()=>{
 const d=dom();try{let resolve,active=true;const a=app(async p=>p.endsWith('/report')?new Promise(r=>{resolve=r;}):response({delivery_id:'task_a',status:'COMPLETED'}));a.data.currentJob='task_a';a.isCurrent=()=>active;const pending=product.mountPage(a,new AbortController());await tick();active=false;d.get('report-content').innerHTML='NEW_IDENTITY_VIEW';resolve(response(report('recommended')));await pending;assert.equal(d.get('report-content').innerHTML,'NEW_IDENTITY_VIEW');}finally{d.restore();}
});

test('leaving report view only aborts polling; explicit cancel is a single authorized write',async()=>{
 const d=dom();try{const calls=[];const a=app(async(p,o)=>{calls.push([p,o]);return response({delivery_id:'task_a',status:'RUNNING'});});a.data.currentJob='task_a';const view=new AbortController(),pending=product.mountPage(a,view);await tick();view.abort();await pending;assert.equal(calls.some(([p])=>p.endsWith('/cancel')),false);}finally{d.restore();}
 const d2=dom();try{let cancelled=false;const calls=[];const a=app(async(p,o)=>{calls.push([p,o]);if(p.endsWith('/cancel')){cancelled=true;return response({status:'CANCELLED'});}if(p.endsWith('/report'))return response(report('cancelled'));return response({delivery_id:'task_a',status:cancelled?'CANCELLED':'RUNNING'});});a.data.currentJob='task_a';const view=new AbortController(),pending=product.mountPage(a,view);await tick();const cancel=d2.get('cancel-job').onclick;await cancel();await pending;const writes=calls.filter(([p])=>p.endsWith('/cancel'));assert.equal(writes.length,1);assert.equal(writes[0][1].method,'POST');assert.deepEqual(writes[0][1].body,{});assert.match(d2.get('report-content').innerHTML,/已取消/);assert.equal(d2.get('approve-candidate').disabled,true);}finally{d2.restore();}
});
