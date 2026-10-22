import test from 'node:test';
import assert from 'node:assert/strict';
import {mountWorkspace} from '../src/workspace.js';

test('thirty remembered jobs cause one scoped page request instead of thirty parallel lookups',async()=>{
 const nodes=new Map(),get=s=>{if(!nodes.has(s))nodes.set(s,{value:'',innerHTML:'',setAttribute(){},removeAttribute(){}});return nodes.get(s);};globalThis.document={querySelector:get,querySelectorAll:()=>[]};const calls=[];
 try{await mountWorkspace({page:'tasks',preview:false,session:{role:'reader'},data:{jobs:Array.from({length:30},(_,i)=>'job_'+i)},isCurrent:()=>true,error:(_,e)=>{throw e;},backend:async p=>{calls.push(p);return {data:{data:[{delivery_id:'job_1',status:'COMPLETED',attempts:1}],next_after:''}};}},new AbortController());assert.equal(calls.length,1);assert.match(calls[0],/^\/jobs\?limit=50&after=&status=$/);assert.match(get('#resource-result').innerHTML,/job_1/);}finally{delete globalThis.document;}
});

test('knowledge list does not require task-only controls',async()=>{const ids=new Map(['#reload-resource','#next-resource','#resource-result','#resource-error'].map(id=>[id,{innerHTML:'',setAttribute(){},removeAttribute(){}}]));globalThis.document={querySelector:s=>ids.get(s)||null,querySelectorAll:()=>[]};try{await mountWorkspace({page:'documents',preview:false,session:{role:'reader'},isCurrent:()=>true,error:(_,e)=>{throw e;},backend:async p=>{assert.match(p,/^\/rag\/documents\?/);return {data:{data:[{document_id:'doc',version:'v2',sha256:'a'.repeat(64)}],next_after:''}};}},new AbortController());assert.match(ids.get('#resource-result').innerHTML,/v2/);}finally{delete globalThis.document;}});
