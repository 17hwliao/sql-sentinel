import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {checkConfiguration} from '../check-config.mjs';
import product from '../product.json' with {type:'json'};

test('deployment preflight checks private grants and credentials without printing their contents',async t=>{
 const dir=await mkdtemp(path.join(os.tmpdir(),'console-preflight-'));t.after(async()=>{assert.equal(path.dirname(dir),os.tmpdir());assert.ok(path.basename(dir).startsWith('console-preflight-'));await rm(dir,{recursive:true});});const file=n=>path.join(dir,n);
 const config={origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181',backend_url:'http://127.0.0.1:1',clerk_issuer:'https://unit.clerk.accounts.dev',clerk_publishable_key:'pk_test_'+Buffer.from('unit.clerk.accounts.dev$').toString('base64'),clerk_secret_file:file('clerk'),members_file:file('members'),backend_admin_file:file('admin')};
 await writeFile(file('clerk'),'sk_test_fixture_only');await writeFile(file('gateway'),'gateway_fixture_only');await writeFile(file('admin'),'admin_fixture_only');
 const member=product.id==='agentmesh'?{enabled:true,tenant_id:'tenant_a',gateway_key_file:file('gateway')}:{enabled:true,tenant_id:'tenant_a',project_id:'project_a',role:'writer',tables:['orders']};
 await writeFile(file('members'),JSON.stringify({user_a:member}));await writeFile(file('config'),JSON.stringify(config));const result=await checkConfiguration(file('config'));assert.equal(result.status,'configuration_valid');assert.equal(result.real_clerk_login,'not_verified');assert.equal(JSON.stringify(result).includes('fixture_only'),false);
 member.enabled=false;await writeFile(file('members'),JSON.stringify({user_a:member}));await assert.rejects(checkConfiguration(file('config')),/no_enabled_product_members/);
 member.enabled=true;await writeFile(file('members'),JSON.stringify({user_a:member}));await rm(file('clerk'));await assert.rejects(checkConfiguration(file('config')));
});
