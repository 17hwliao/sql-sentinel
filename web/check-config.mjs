import {readFile,access,constants} from 'node:fs/promises';
import {validateConfig} from './server.mjs';
import product from './product.json' with {type:'json'};

export async function checkConfiguration(file,{probe=false}={}){
 const c=validateConfig(JSON.parse(await readFile(file,'utf8')));
 if(!c.clerk_publishable_key||!c.clerk_issuer||!c.backend_url)throw Error('configuration_incomplete');
 for(const name of ['clerk_secret_file','members_file'])if(!c[name])throw Error('private_file_missing');
 const members=JSON.parse(await readFile(c.members_file,'utf8'));if(!Object.keys(members).length)throw Error('no_product_members');
 const paths=new Set([c.clerk_secret_file,c.members_file]);let enabled=0;
 for(const [subject,m]of Object.entries(members)){
  if(!/^[A-Za-z0-9_-]{1,64}$/.test(subject)||typeof m.enabled!=='boolean')throw Error('membership_invalid');
  if(!m.enabled)continue;enabled++;
  if(!/^[A-Za-z0-9_-]{1,64}$/.test(m.tenant_id))throw Error('membership_invalid');
  if(product.id==='agentmesh'){if(!m.gateway_key_file)throw Error('gateway_credential_missing');paths.add(m.gateway_key_file);if(m.can_manage_keys||m.platform_admin)paths.add(c.backend_admin_file);}
  else{paths.add(c.backend_admin_file);if(!/^[A-Za-z0-9_-]{1,64}$/.test(m.project_id)||!['reader','writer','reviewer','admin'].includes(m.role)||!Array.isArray(m.tables)||!m.tables.length||m.tables.length>16||m.tables.some(t=>!/^[A-Za-z0-9_-]{1,64}$/.test(t)))throw Error('membership_invalid');}
 }
 if(!enabled)throw Error('no_enabled_product_members');
 for(const file of paths){if(!file)throw Error('private_file_missing');await access(file,constants.R_OK);const raw=(await readFile(file,'utf8')).trim();if(!raw)throw Error('private_file_empty');}
 if(probe){const r=await fetch(c.backend_url+'/readyz',{signal:AbortSignal.timeout(8000),redirect:'error'});if(!r.ok||(await r.json()).status!=='ready')throw Error('backend_not_ready');}
 return {status:'configuration_valid',enabled_members:enabled,backend_ready:probe?'verified':'not_probed',real_clerk_login:'not_verified'};
}
if(process.argv[1]?.endsWith('check-config.mjs')){
 try{if(!process.env.CONSOLE_CONFIG_FILE)throw Error('config_file_required');console.log(JSON.stringify(await checkConfiguration(process.env.CONSOLE_CONFIG_FILE,{probe:process.argv.includes('--probe')})));}
 catch{console.error('Console preflight failed. Check configuration, grants, private file access and backend readiness. No secrets printed.');process.exitCode=1;}
}
