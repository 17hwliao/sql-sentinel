import {esc,delay} from './core.js';

const idPattern=/^[A-Za-z0-9_-]{1,64}$/;
const validID=value=>typeof value==='string'&&idPattern.test(value);
const states={queued:'等待执行',running:'模型正在决策',waiting_tool:'诊断已提交，等待工具',completed:'已完成',failed:'已停止',cancelled:'已取消',outcome_unknown:'结果尚未确认'};
const terminal=new Set(['completed','failed','cancelled','outcome_unknown']);
export const isSettled=run=>terminal.has(run?.status)&&!(run.status==='cancelled'&&run.stop_reason==='cancel_requested');
const stateLabel=run=>run.status==='cancelled'&&run.stop_reason==='cancel_requested'?'取消已请求，等待任务确认':states[run.status];
const text=(v,n=4000)=>typeof v==='string'?v.slice(0,n):'';
const number=v=>Number.isSafeInteger(v)&&v>=0?String(v):'未取得';
const kv=(label,value)=>`<div class="keyval"><span>${esc(label)}</span><span>${esc(value)}</span></div>`;
export function agentScopeKey(session){
 const parts=[session?.subject,session?.tenant_id,session?.project_id];
 if(parts.some(v=>typeof v!=='string'||!v))return null;
 return 'sentinel:agent-run:'+JSON.stringify(parts);
}
export function validRun(value){
 if(!value||typeof value!=='object'||!validID(value.id)||!validID(value.request_id)||!Object.hasOwn(states,value.status))throw Error('运行状态格式不完整，请重新读取。');
 return value;
}
export function validAgentConfig(value){
 if(!value||typeof value.enabled!=='boolean'||!Array.isArray(value.model_aliases)||value.model_aliases.some(v=>typeof v!=='string'||!v||v.length>128)||!Array.isArray(value.tables)||value.tables.some(v=>typeof v!=='string')||typeof value.default_model!=='string'||typeof value.tools_enabled!=='boolean')throw Error('运行配置格式不完整，请稍后重新读取。');
 return value;
}
export function agentFailure(error){
 return ({sql_required:'请提供需要诊断的单条 SQL；Agent 不会凭空生成业务查询。',agent_run_input_invalid:'输入不符合运行范围，请检查 SQL、请求标识及模型选择。',agent_run_conflict:'同一请求标识已经绑定其他输入，请先核对已有运行。',agent_run_forbidden:'当前账号没有此运行或数据表的权限。',agent_run_not_found:'没有找到当前身份有权读取的运行。',agent_run_unavailable:'Agent 服务暂不可用，请稍后读取原运行状态。',agent_run_budget_exhausted:'本次运行已达到预算上限。',agent_run_outcome_unknown:'运行结果尚未确认，请查询原运行，不要重复创建。',forbidden:'当前角色只能查看，不能创建或取消运行。',table_not_allowed:'SQL 涉及未授权的数据表。',unauthorized:'当前身份已失效，请重新登录。'})[error?.code]||'操作尚未完成，请保留当前输入并重新核对状态。';
}
export function renderAgentRun(a){
 return a.head('SQL AGENT','让 Agent 跟进一条 SQL','提供 SQL 和诊断目标，Agent 使用同一影子验证服务取证。进度、报告和结论按事实展示。')+`<div class="columns"><div class="stack"><section class="panel"><div class="panel-head"><h2>新的诊断请求</h2><span class="badge">受限 SQL Agent · L1</span></div><form id="agent-run-form" class="panel-body form"><label for="agent-run-sql">需要诊断的 SQL<textarea class="sql" id="agent-run-sql" rows="8" maxlength="65536" spellcheck="false" placeholder="粘贴单条 SELECT / WITH 查询；不会执行任意生产 SQL" required></textarea></label><label for="agent-run-message">你希望检查什么（可选）<textarea id="agent-run-message" rows="3" maxlength="4096" placeholder="例如：检查筛选与排序的索引候选，并解释验证证据"></textarea></label><details open><summary>本次运行配置</summary><div class="form" style="margin-top:14px"><label for="agent-run-model">模型别名<select id="agent-run-model" disabled><option value="">正在读取授权模型…</option></select></label><label><input id="agent-run-tools" type="checkbox" disabled> 允许提交一个影子诊断任务</label><button class="btn small" type="button" id="agent-run-defaults" disabled>恢复部署默认配置</button><div id="agent-run-config"><p class="subtle">正在读取服务状态和授权范围…</p></div><button class="textlink" type="button" id="agent-config-refresh">重新读取配置</button></div></details><div class="actions"><button class="btn primary" type="submit" id="agent-run-submit" disabled>开始 SQL Agent</button></div><div id="agent-run-input-error" class="inline-result" role="alert"></div><div id="agent-run-pending" class="inline-result" aria-live="polite"></div></form></section><section class="panel"><div class="panel-head"><h2>当前运行</h2><button type="button" class="btn small" id="agent-run-refresh" disabled>刷新状态</button></div><div class="panel-body" id="agent-run-result" aria-live="polite"><p class="subtle">尚未选择运行。创建后可刷新页面恢复，离开页面只停止等待。</p></div></section></div><aside class="stack">${a.panel('最近运行',`<div class="actions"><button type="button" class="btn small" id="agent-runs-refresh">刷新运行列表</button></div><div id="agent-runs-list" style="margin-top:14px">正在读取…</div><div id="agent-runs-error" role="alert"></div>`)}${a.panel('使用边界',`<p class="insight">这里只做 SQL 诊断。不会根据自然语言查询生产数据，也不会自动审批、上线索引或启动性能测量。</p><p class="subtle">外层最多 4 次模型派发、1 个诊断任务；内层最多 5 次模型决策、2 次候选验证。调用次数不等于 token 或费用。</p><p class="subtle">模型经 AgentMesh；业务权限仍由 SQL Sentinel 独立校验。浏览器不保存服务端模型 Key 或数据库凭据。</p>`)}</aside></div>`;
}

export function renderRunRecord(value){
 const run=validRun(value),job=run.job,evidence=job?.evidence;
 const events=Array.isArray(run.events)?run.events.slice(-24):[];
 const canCancel=['queued','running','waiting_tool'].includes(run.status);
 const summary=run.answer?`<div class="divider"></div><h3 class="section-title">${run.model_summary?'模型解释':'证据结论'}</h3><p class="insight" style="white-space:pre-wrap">${esc(text(run.answer))}</p>`:'';
 const input=`<details style="margin-top:16px"><summary>本次请求与 SQL</summary>${run.message?`<p class="insight" style="white-space:pre-wrap">${esc(text(run.message))}</p>`:''}<pre class="codeblock" style="white-space:pre-wrap;overflow-wrap:anywhere">${esc(text(run.sql,8192))}</pre>${typeof run.sql==='string'&&run.sql.length>8192?'<p class="subtle">SQL 显示已截断，服务端仍绑定原始输入。</p>':''}</details>`;
 const facts=Array.isArray(evidence?.facts)?evidence.facts.slice(0,12):[];
 const evidenceBlock=evidence?`<div class="divider"></div><h3 class="section-title">诊断证据</h3>${kv('证据级别',text(evidence.level,20)||'未提供')}${kv('诊断结果',text(evidence.outcome,80)||'未提供')}<p class="insight">${esc(text(evidence.summary))}</p>${facts.length?`<ul>${facts.map(f=>`<li>${esc(text(f,600))}</li>`).join('')}</ul>`:''}<p class="subtle">本次计划证据不代表实测收益。${evidence.recommended?'候选仍需原报告中的独立审批。':'没有候选是合法结果，不能据此创建审批或测量。'}</p>`:'';
 const delivery=text(job?.delivery_id,64),link=validID(delivery)?`<a class="btn small" id="agent-run-report" href="#report">查看原诊断报告 →</a>`:'';
 return `<span class="badge ${run.status==='failed'?'amber':''}">${esc(stateLabel(run))}</span>${run.tools_enabled===false?'<div class="notice">本次未启用诊断工具，不会执行影子验证；模型文字不能作为已验证诊断结论。</div>':''}${run.status==='outcome_unknown'?'<div class="notice">请求结果未知，后台可能已执行。请刷新原运行或查看关联报告，不要新建同一诊断。</div>':''}${run.status==='waiting_tool'?'<p class="subtle">后台等待诊断状态；轮询不再次请求模型。</p>':''}${kv('运行 ID',run.id)}${kv('稳定请求 ID',run.request_id)}${kv('配置版本',text(run.config_version,128)||'未提供')}${kv('模型派发预留',number(run.model_calls_reserved))}${kv('关联诊断任务',delivery||'尚未创建')}${kv('费用',run.usage_known?'已取得用量；金额以 AgentMesh 对账为准':'未知；未取得完整用量和结算信息')}${run.stop_reason?kv('停止原因',text(run.stop_reason,160)):''}${input}${summary}${evidenceBlock}${job&&!evidence?'<p class="subtle">关联任务尚无可确认的证据报告；不展示虚构候选或性能结论。</p>':''}${events.length?`<details style="margin-top:18px"><summary>动作事实记录（${events.length}）</summary><ol>${events.map(e=>`<li><strong>${esc(text(e.kind,80))}</strong><p class="subtle">${esc(text(e.message,600))}</p></li>`).join('')}</ol></details>`:''}<div class="actions" style="margin-top:18px">${link}${canCancel?'<button class="btn" type="button" id="agent-run-cancel">取消此运行及其诊断任务</button>':''}</div><p class="subtle">关闭页面或停止等待不会取消后台任务。取消必须由服务端确认，只影响此运行创建的任务。</p><div id="agent-run-operation" role="alert"></div>`;
}

function readMemory(storage,key){try{const v=JSON.parse(storage?.getItem(key)||'null');return v&&typeof v==='object'?{...(validID(v.id)?{id:v.id}:{}),...(validID(v.request_id)?{request_id:v.request_id}:{}),pending:v.pending===true&&validID(v.request_id)}:{};}catch{return {};}}
function writeMemory(storage,key,value){try{if(storage&&key)storage.setItem(key,JSON.stringify(value));}catch{}}
function defaultStorage(){try{return globalThis.sessionStorage;}catch{return null;}}
export async function mountAgentRun(a,view,{storage=defaultStorage(),wait=delay,uuid=()=>crypto.randomUUID()}={}){
 const $=s=>document.querySelector(s),session=a.session,epoch=a.identityEpoch;
 const current=()=>!view.signal.aborted&&a.isCurrent(view)&&session===a.session&&epoch===a.identityEpoch;
 const key=agentScopeKey(a.preview?{subject:'preview',tenant_id:'preview',project_id:'preview'}:session);
 let memory=key?readMemory(storage,key):{},config=null,run=null,busy=false,cancelling=false,readRevision=0,listRevision=0,configRevision=0,recoveryRevision=0,pollAbort=null;
 const save=()=>writeMemory(storage,key,memory);
 const allowed=a.preview||['writer','admin'].includes(session?.role);
 const error=(node,message)=>{if(current()&&node)node.innerHTML=`<div class="error" role="alert">${esc(message)}</div>`;};
 const controls=()=>{if(!current())return;$('#agent-run-submit').disabled=busy||memory.pending||!allowed||!config?.enabled||!config.model_available||($('#agent-run-tools').checked&&!config.task_service_available);};
 const pendingNotice=()=>{if(!current())return;$('#agent-run-pending').innerHTML=memory.pending?`<div class="notice">提交结果尚未确认。保留原请求 ID：<span class="mono">${esc(memory.request_id)}</span>。请查询原请求找回运行；不要重复创建。<br><button class="textlink" type="button" id="agent-pending-check">查询原请求</button><div id="agent-pending-result" role="status"></div></div>`:'';if(memory.pending)$('#agent-pending-check').onclick=()=>recoverPending();controls();};
 const configure=async()=>{
  const revision=++configRevision;
  try{const {data}=await a.backend('/agent/config',{signal:view.signal});if(!current()||revision!==configRevision)return;config=validAgentConfig(data);if(!a.preview&&(config.tenant_id!==session?.tenant_id||config.project_id!==session?.project_id))throw Error('config_scope_invalid');
   $('#agent-run-model').innerHTML=config.model_aliases.map(alias=>`<option value="${esc(alias)}">${esc(alias)}</option>`).join('');$('#agent-run-model').value=config.default_model;$('#agent-run-model').disabled=!allowed||!config.enabled;
   $('#agent-run-tools').checked=config.tools_enabled;$('#agent-run-tools').disabled=!allowed||!config.enabled||!config.tools_enabled;$('#agent-run-defaults').disabled=!allowed||!config.enabled;
   $('#agent-run-config').innerHTML=kv('项目',text(config.project_id,64)||'未提供')+kv('允许的表',config.tables.map(v=>text(v,64)).join(', ')||'没有授权表')+kv('有效配置版本',text(config.version,128)||'未提供')+kv('模型服务',config.model_available?'已配置；不代表真实调用已验证':'未配置')+kv('任务服务',config.task_service_available?'已配置；健康情况由实际任务确认':'不可用')+`<p class="subtle">外层 ${number(config.max_model_calls)} 次模型派发 / ${number(config.max_jobs)} 个任务 / ${number(config.run_seconds)} 秒；内层 ${number(config.inner_model_calls)} 次决策 / ${number(config.inner_evaluations)} 次验证。</p>${!config.enabled?'<div class="notice">部署方尚未启用网站 Agent；原 SQL 诊断仍可独立使用。</div>':''}`;
   if(!allowed)error($('#agent-run-input-error'),'当前角色只能查看运行，不能提交诊断。');controls();
  }catch(e){if(current()&&revision===configRevision&&e.name!=='AbortError'){config=null;error($('#agent-run-config'),agentFailure(e));controls();}}
 };
 const reportLink=()=>{const button=$('#agent-run-report');if(button&&run?.job?.delivery_id)button.onclick=()=>{if(current()){a.data.currentJob=run.job.delivery_id;a.rememberJob?.(run.job.delivery_id);}};};
 const drawRun=()=>{
  if(!current()||!run)return;$('#agent-run-result').innerHTML=renderRunRecord(run);$('#agent-run-refresh').disabled=false;reportLink();
  const listState=$('#agent-list-state-'+run.id);if(listState)listState.textContent=stateLabel(run);
  const cancel=$('#agent-run-cancel');if(cancel){cancel.disabled=!allowed||cancelling;cancel.onclick=async()=>{
   if(!current()||!allowed||cancelling)return;cancelling=true;const id=run.id;cancel.disabled=true;
   try{const {data}=await a.backend('/agent/runs/'+id+'/cancel',{method:'POST',body:{},signal:view.signal});if(!current()||run?.id!==id)return;const next=validRun(data);if(next.id!==id)throw Error('run_binding_invalid');run=next;drawRun();await openRun(id);}
   catch(e){if(current()&&run?.id===id&&e.name!=='AbortError')error($('#agent-run-operation'),!e.status||e.status>=500?'取消结果尚未确认，请刷新原运行状态核实。':agentFailure(e));}
   finally{cancelling=false;}
  };}
 };
 const follow=async(id,revision,signal)=>{
  const started=Date.now();
  try{while(Date.now()-started<185000){await wait(1500,signal);if(!current()||readRevision!==revision||signal.aborted)return;const {data}=await a.backend('/agent/runs/'+id,{signal});if(!current()||readRevision!==revision||signal.aborted)return;const next=validRun(data);if(next.id!==id)throw Error('run_binding_invalid');run=next;drawRun();if(isSettled(run))return;}if(current()&&readRevision===revision)error($('#agent-run-operation'),'已暂停自动等待。后台运行可能仍在继续，请稍后刷新原状态。');}
  catch(e){if(current()&&readRevision===revision&&!signal.aborted&&e.name!=='AbortError')error($('#agent-run-operation'),'状态读取中断，保留最后确认的结果。请刷新原运行，不会自动新建任务。');}
 };
 const openRun=async(id,{followStatus=true}={})=>{
  if(!validID(id)||!current())return;const revision=++readRevision;pollAbort?.abort();pollAbort=new AbortController();const signal=AbortSignal.any([view.signal,pollAbort.signal]);
  if(run?.id!==id){run=null;$('#agent-run-result').innerHTML='<p class="subtle">正在读取所选运行…</p>';}
  try{const {data}=await a.backend('/agent/runs/'+id,{signal});if(!current()||readRevision!==revision||signal.aborted)return;const next=validRun(data);if(next.id!==id)throw Error('run_binding_invalid');run=next;if(!memory.pending||run.request_id===memory.request_id){memory={id:run.id,request_id:run.request_id,pending:false};save();}pendingNotice();drawRun();if(followStatus&&!isSettled(run))void follow(id,revision,signal);}
  catch(e){if(current()&&readRevision===revision&&!signal.aborted&&e.name!=='AbortError')error($('#agent-run-operation')||$('#agent-run-result'),agentFailure(e));}
 };
 const recoverPending=async()=>{
  if(!current()||!memory.pending||!validID(memory.request_id))return;
  const requestID=memory.request_id,revision=++recoveryRevision,selectedRevision=readRevision;
  const live=()=>current()&&revision===recoveryRevision&&memory.pending&&memory.request_id===requestID&&readRevision===selectedRevision;
  try{const {data}=await a.backend('/agent/runs/by-request/'+requestID,{signal:view.signal});if(!live())return;const recovered=validRun(data);if(recovered.request_id!==requestID)throw Error('request_binding_invalid');await openRun(recovered.id);}
  catch(e){if(live()&&e.name!=='AbortError')error($('#agent-pending-result'),e.status===404?'暂未找到原请求的持久运行。提交仍可能在处理，请稍后再次查询；不会自动重新提交。':'原请求状态尚未确认，请稍后再次查询；不会自动重新提交。');}
 };
 const loadList=async()=>{
  const revision=++listRevision;
  try{const {data}=await a.backend('/agent/runs',{signal:view.signal});if(!current()||revision!==listRevision)return;if(!data||!Array.isArray(data.runs))throw Error('list_invalid');const runs=data.runs.slice(0,30).map(validRun);
   $('#agent-runs-error').innerHTML='';$('#agent-runs-list').innerHTML=runs.length?runs.map(item=>{
    // The list read may have started before the selected run's final poll.
    const shown=run?.id===item.id&&isSettled(run)&&!isSettled(item)?run:item;
    return `<button class="case-option" type="button" data-agent-run="${esc(item.id)}"><span><strong id="agent-list-state-${esc(item.id)}">${esc(stateLabel(shown))}</strong><small class="mono">${esc(item.request_id)}</small></span><span class="subtle">打开 →</span></button>`;
   }).join(''):'<p class="subtle">当前身份在此项目还没有运行记录。</p>';
   document.querySelectorAll('[data-agent-run]').forEach(button=>button.onclick=()=>openRun(button.dataset.agentRun));
  }catch(e){if(current()&&revision===listRevision&&e.name!=='AbortError')error($('#agent-runs-error'),'运行列表读取失败，未将已有记录当成空列表。请稍后重试。');}
 };
 $('#agent-config-refresh').onclick=configure;$('#agent-runs-refresh').onclick=loadList;$('#agent-run-refresh').onclick=()=>run&&openRun(run.id);
 $('#agent-run-tools').onchange=controls;
 $('#agent-run-defaults').onclick=()=>{if(!current()||!config)return;$('#agent-run-model').value=config.default_model;$('#agent-run-tools').checked=config.tools_enabled;controls();};
 $('#agent-run-form').onsubmit=async event=>{
  event.preventDefault();if(!current()||busy||memory.pending||!allowed||!config?.enabled||!config.model_available)return;
  const sql=$('#agent-run-sql').value.trim(),message=$('#agent-run-message').value.trim(),model=$('#agent-run-model').value,tools=$('#agent-run-tools').checked;
  if(!sql){error($('#agent-run-input-error'),agentFailure({code:'sql_required'}));return;}
  if(new TextEncoder().encode(sql).length>65536||new TextEncoder().encode(message).length>4096||!config.model_aliases.includes(model)||tools&&!config.tools_enabled){error($('#agent-run-input-error'),'输入或模型选择超出当前部署范围，请检查后重试。');return;}
  if(tools&&!config.task_service_available){error($('#agent-run-input-error'),'诊断任务服务当前不可用，请稍后重新读取配置。');return;}
  busy=true;const requestID='web_'+uuid().replaceAll('-','');memory={request_id:requestID,pending:true};save();controls();$('#agent-run-input-error').innerHTML='';$('#agent-run-pending').innerHTML='<div class="notice">正在提交，等待服务端确认…</div>';
  try{const {data}=await a.backend('/agent/runs',{method:'POST',body:{request_id:requestID,sql,message,model_alias:model,tools_enabled:tools},signal:view.signal});if(!current())return;const next=validRun(data);if(next.request_id!==requestID)throw Error('request_binding_invalid');run=next;memory={id:run.id,request_id:run.request_id,pending:false};save();pendingNotice();drawRun();await openRun(run.id);void loadList();}
  catch(e){if(!current())return;if(e.status&&e.status<500){memory={};save();}pendingNotice();if(e.name!=='AbortError')error($('#agent-run-input-error'),agentFailure(e));}
  finally{busy=false;if(current())controls();}
 };
 view.signal.addEventListener('abort',()=>pollAbort?.abort(),{once:true});pendingNotice();await configure();if(!current())return;if(memory.pending)await recoverPending();if(!current())return;await loadList();if(!current())return;if(memory.id&&run?.id!==memory.id)await openRun(memory.id);
}

export function previewAgentRun(path,options,a){
 const wrap=data=>({data,headers:new Headers()}),url=new URL(path,'http://preview.invalid'),method=options.method||'GET';
 const config={enabled:true,version:'preview.v1',model_aliases:['示例模型'],default_model:'示例模型',tools_enabled:true,max_model_calls:4,max_jobs:1,run_seconds:180,inner_model_calls:5,inner_evaluations:2,tenant_id:'preview',project_id:'orders-demo',tables:['orders'],model_available:true,task_service_available:true};
 const demo={id:'preview_run',request_id:'preview_request',sql:'SELECT id FROM orders WHERE user_id=42',message:'示例诊断目标',model_alias:'示例模型',tools_enabled:true,config_version:'preview.v1',status:'completed',stop_reason:'preview_only',answer:'界面示例：候选已经过计划级验证；没有执行真实模型或数据库操作，不代表实际性能收益。',model_summary:false,model_calls_reserved:2,cost_cny:null,usage_known:false,job:{job_id:'preview_job',delivery_id:'demo_orders_001',status:'COMPLETED',evidence:{outcome:'recommended',level:'L1',summary:'示例计划使用复合索引；仍需独立审批与测量。',recommended:true,facts:['示例观察：候选索引已清理']}},events:[{kind:'preview',message:'示例动作事实；未保存业务数据。'}]};
 a.data.previewAgentRuns||=[];
 if(url.pathname==='/api/backend/agent/config')return wrap(config);
 if(url.pathname==='/api/backend/agent/runs'&&method==='GET')return wrap({runs:a.data.previewAgentRuns.length?a.data.previewAgentRuns:[demo]});
 if(url.pathname==='/api/backend/agent/runs'&&method==='POST'){const body=typeof options.body==='string'?JSON.parse(options.body):options.body;const run={...demo,id:'preview_'+body.request_id.slice(0,40),request_id:body.request_id,sql:body.sql,message:body.message};a.data.previewAgentRuns.unshift(run);return wrap(run);}
 if(url.pathname.startsWith('/api/backend/agent/runs/by-request/')){const requestID=url.pathname.split('/').at(-1),found=a.data.previewAgentRuns.find(item=>item.request_id===requestID)||(demo.request_id===requestID?demo:null);if(!found)throw Object.assign(Error('preview_request_missing'),{status:404,code:'agent_run_not_found'});return wrap(found);}
 const id=url.pathname.split('/')[5],run=a.data.previewAgentRuns.find(item=>item.id===id)||demo;
 if(url.pathname.endsWith('/cancel'))return wrap({...run,status:'cancelled',stop_reason:'preview_only'});
 return wrap(run);
}
