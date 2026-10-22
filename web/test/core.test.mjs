import test from 'node:test';
import assert from 'node:assert/strict';
import {esc,money,readSSE} from '../src/core.js';
test('untrusted model/SQL HTML is escaped',()=>{assert.equal(esc('<img onerror="x">'), '&lt;img onerror=&quot;x&quot;&gt;');});
test('missing or unsafe monetary values are not shown as zero',()=>{assert.equal(money(null),'—');assert.equal(money(NaN),'—');assert.equal(money(0),'¥0.00');assert.equal(money(123456),'¥0.00012346');});
function response(chunks){return new Response(new ReadableStream({start(c){for(const s of chunks)c.enqueue(new TextEncoder().encode(s));c.close();}}));}
test('SSE handles fragmented records, UTF-8 and CRLF',async()=>{let text='';await readSSE(response(['data: {"choices":[{"delta":{"content":"你', '好"}}]}\r','\n\r\ndata: [DONE]\r\n\r\n']),e=>{text+=e.choices[0].delta.content;});assert.equal(text,'你好');});
test('HTTP 200 without DONE and stream error are incomplete',async()=>{await assert.rejects(()=>readSSE(response(['data: {"choices":[]}\n\n']),()=>{}),/中断/);await assert.rejects(()=>readSSE(response(['data: {"error":{"code":"stream_interrupted"}}\n\n']),()=>{}),/stream_interrupted/);});
test('invalid and unbounded SSE frames are rejected',async()=>{await assert.rejects(()=>readSSE(response(['data: invalid\n\n']),()=>{}),/格式/);await assert.rejects(()=>readSSE(response(['x'.repeat(262145)]),()=>{}),/大小/);});
