import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {pathToFileURL,fileURLToPath} from 'node:url';
import path from 'node:path';
const dir=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../src');
const source=(await readFile(path.join(dir,'product.js'),'utf8')).replace(/from '\.\/([\w]+)\.js'/g,(_,name)=>'from '+JSON.stringify(pathToFileURL(path.join(dir,name+'.js')).href));
const product=await import('data:text/javascript;base64,'+Buffer.from(source+'\nexport {renderReport};').toString('base64'));
const fixture=JSON.parse(await readFile(new URL('./fixtures/performance.json',import.meta.url),'utf8'));
const panel=(title,body)=>`<section>${title}${body}</section>`;
test('actual Go wire fields render eligible L2 measurements and noise',()=>{const html=product.renderReport({panel},{delivery_id:'test',status:'COMPLETED'},{performance:fixture},'a'.repeat(64));assert.match(html,/12\.5 ms/);assert.match(html,/6\.25 ms/);assert.match(html,/1\.00%/);assert.match(html,/本次测量满足相应证据门槛/);assert.doesNotMatch(html,/本次测量不足以确认性能收益/);});
test('ineligible report never becomes an eligible performance claim',()=>{const html=product.renderReport({panel},{delivery_id:'test',status:'COMPLETED'},{performance:{...fixture,eligible_for_performance_claim:false,rejection_reasons:['noise_limit']}},'a'.repeat(64));assert.match(html,/不足以确认性能收益/);assert.match(html,/noise_limit/);});
test('summary displays backend statement, facts and required verification',()=>{const html=product.renderReport({panel},{delivery_id:'test',status:'COMPLETED'},{artifacts:{'diagnostic_hypotheses.json':{hypotheses:[{code:'review',statement:'expected statement',facts:[{field:'rows',value:'12345'}],required_verification:['controlled_ab_measurement']}]}}},'a'.repeat(64));const summary=html.split('<div data-report-panel="summary">')[1].split('<div data-report-panel="plan"')[0];assert.match(summary,/expected statement/);assert.match(summary,/12345/);assert.match(summary,/controlled_ab_measurement/);});
test('RAG upload uses a valid MIME/version and retrieval renders items text',async()=>{const nodes=new Map(),get=s=>{if(!nodes.has(s))nodes.set(s,{value:'',disabled:false,innerHTML:''});return nodes.get(s);};globalThis.document={querySelector:get,querySelectorAll:()=>[]};try{
 get('#document-id').value='doc';get('#document-content').value='controlled evidence';get('#document-version').value='v2';get('#document-type').value='text/plain';get('#question').value='backup evidence';let submitted;
 const citation={chunk_id:'chunk1',document_id:'doc',version:'v2'},a={page:'knowledge',preview:false,session:{role:'writer'},isCurrent:()=>true,error:(_,e)=>{throw e;},backend:async(p,o)=>{if(p==='/rag/documents'){submitted=o.body;return {data:{}};}return {data:{items:[{citation,text:'actual retrieved evidence'}],citations:[citation],has_evidence:true}};}};
 await product.mountPage(a,new AbortController());await get('#document-form').onsubmit({preventDefault(){}});assert.equal(submitted.content_type,'text/plain');assert.equal(submitted.version,'v2');await get('#retrieve-only').onclick();assert.match(get('#knowledge-result').innerHTML,/actual retrieved evidence/);assert.match(get('#knowledge-result').innerHTML,/chunk1/);
}finally{delete globalThis.document;}});
