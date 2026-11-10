import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {checkConfiguration} from '../check-config.mjs';
import product from '../product.json' with {type:'json'};

test('deployment preflight checks private grants and credentials without printing their contents',async t=>{
 const dir=await mkdtemp(path.join(os.tmpdir(),'console-preflight-'));t.after(async()=>{assert.equal(path.dirname(dir),os.tmpdir());assert.ok(path.basename(dir).startsWith('console-preflight-'));await rm(dir,{recursive:true});});const file=n=>path.join(dir,n);
 const config={origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181',backend_url:'http://127.0.0.1:1',oidc_issuer:'http://127.0.0.1:8000',oidc_client_id:'fixture',oidc_client_secret_file:file('client'),session_key_file:file('session'),session_redis_url:'redis://127.0.0.1:6379/0',session_redis_password_file:file('redis'),email_delivery:'test_sink',members_file:file('members'),backend_admin_file:file('admin')};
 await writeFile(file('client'),'client_fixture_only');await writeFile(file('session'),'1'.repeat(64));await writeFile(file('redis'),'redis_fixture_only');await writeFile(file('gateway'),'gateway_fixture_only');await writeFile(file('admin'),'admin_fixture_only');
 const member={issuer:config.oidc_issuer,...(product.id==='agentmesh'?{enabled:true,tenant_id:'tenant_a',gateway_key_file:file('gateway')}:{enabled:true,tenant_id:'tenant_a',project_id:'project_a',role:'writer',tables:['orders']})};
 await writeFile(file('members'),JSON.stringify({user_a:member}));await writeFile(file('config'),JSON.stringify(config));const result=await checkConfiguration(file('config'));assert.equal(result.status,'configuration_valid');assert.equal(result.real_email_login,'not_verified');assert.equal(result.runtime_mode,'local_test');assert.equal(result.deployment_ready,false);assert.equal(JSON.stringify(result).includes('fixture_only'),false);
 member.enabled=false;await writeFile(file('members'),JSON.stringify({user_a:member}));await assert.rejects(checkConfiguration(file('config')),/no_enabled_product_members/);
 member.enabled=true;await writeFile(file('members'),JSON.stringify({user_a:member}));config.email_delivery='unconfigured';await writeFile(file('config'),JSON.stringify(config));await assert.rejects(checkConfiguration(file('config')),/email_not_configured/);assert.equal((await checkConfiguration(file('config'),{allowUnconfiguredEmail:true})).deployment_ready,false);await rm(file('client'));await assert.rejects(checkConfiguration(file('config'),{allowUnconfiguredEmail:true}));
});
