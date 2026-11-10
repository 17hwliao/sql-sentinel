"""No-paid-call tests for isolated gateway + Sentinel client preparation."""
import importlib.util
import json
import os
from pathlib import Path
import runpy
import sys
import tempfile
import types
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('prepare_agent_client', Path(__file__).with_name('prepare-agent-client.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PrepareTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='sentinel-client-unit-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.token = self.root / 'writer.jwt'
        self.token.write_text('fixture-secret-jwt-not-a-real-credential', encoding='utf-8')
        self.config = self.root / 'sentinel.json'
        self.config.write_text(json.dumps({'api_origin': 'https://sentinel.example.test', 'tenant_id': 'tenant', 'project_id': 'project', 'token_file': str(self.token)}), encoding='utf-8')
        self.binary = Path(sys.executable).resolve()

    def prepare(self, **kwargs):
        return module.prepare('https://mesh.example.test/v1', 'domestic-chat', self.config, self.binary, self.root / 'new-profile', **kwargs)

    def test_offline_generation_never_runs_network_or_client(self):
        with mock.patch.object(module.subprocess, 'run', side_effect=AssertionError('offline process execution')):
            receipt = self.prepare()
        self.assertEqual(receipt['gateway_preflight'], 'not_run')
        self.assertEqual(receipt['historical_cumulative_budget'], 'not_verified')
        self.assertEqual(receipt['paid_model_calls'], 0)
        out = self.root / 'new-profile'
        profile = json.loads((out / 'opencode.json').read_text(encoding='utf-8'))
        self.assertEqual(profile['provider']['agentmesh']['options']['baseURL'], 'https://mesh.example.test/v1')
        self.assertEqual(profile['provider']['agentmesh']['options']['apiKey'], '{env:AGENTMESH_API_KEY}')
        self.assertEqual(profile['permission'], {'*': 'deny', 'sql_sentinel_*': 'allow'})
        self.assertEqual(profile['mcp']['sql_sentinel']['command'], [str(self.binary)])
        for file in out.iterdir():
            self.assertNotIn('fixture-secret-jwt', file.read_text(encoding='utf-8'))
        compile((out / 'launch-client.py').read_text(encoding='utf-8'), 'launch-client.py', 'exec')

    def test_existing_output_is_never_overwritten(self):
        out = self.root / 'new-profile'
        out.mkdir()
        marker = out / 'keep.txt'
        marker.write_text('existing user profile', encoding='utf-8')
        with self.assertRaises(ValueError):
            self.prepare()
        self.assertEqual(marker.read_text(encoding='utf-8'), 'existing user profile')

    def test_unsafe_origins_and_scope_rejected_before_output(self):
        for origin in ['http://remote.example.test', 'https://user:secret@example.test', 'https://example.test/?token=secret', 'https://example.test/arbitrary/path', 'file:///etc/passwd']:
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                module.prepare(origin, 'domestic-chat', self.config, self.binary, self.root / 'new-profile')
        self.assertFalse((self.root / 'new-profile').exists())
        value = json.loads(self.config.read_text(encoding='utf-8'))
        value['project_id'] = '../other-project'
        self.config.write_text(json.dumps(value), encoding='utf-8')
        with self.assertRaises(ValueError):
            self.prepare()

    def test_preflight_reuses_existing_gateway_generator(self):
        # A test-only generator makes use of its API visible without contacting
        # a real gateway, provider, user wallet or credential.
        mesh = self.root / 'mesh'
        (mesh / 'scripts').mkdir(parents=True)
        (mesh / 'scripts/client-config.py').write_text('''import json
def generate(base,model,key_file,out):
    assert base == 'https://mesh.example.test'
    assert model == 'domestic-chat'
    assert key_file.read_text() == 'fixture-gateway-key'
    out.mkdir()
    (out/'opencode.json').write_text(json.dumps({'provider':{'agentmesh':{'options':{'apiKey':'{env:AGENTMESH_API_KEY}'}}}}))
''', encoding='utf-8')
        key = self.root / 'gateway-key.txt'
        key.write_text('fixture-gateway-key', encoding='utf-8')
        with mock.patch.object(module.subprocess, 'run', return_value=types.SimpleNamespace(returncode=0, stdout=b'{"status":"ready"}')) as run:
            receipt = self.prepare(preflight=True, key_file=key, mesh_source=mesh)
        self.assertEqual(run.call_args.args[0], [str(self.binary), '--check'])
        self.assertEqual(receipt['gateway_preflight'], 'read_only_pass')
        self.assertEqual(receipt['sentinel_preflight'], 'read_only_pass')
        self.assertEqual(receipt['historical_cumulative_budget'], 'not_verified')
        for file in (self.root / 'new-profile').iterdir():
            self.assertNotIn('fixture-gateway-key', file.read_text(encoding='utf-8'))

    def test_failed_preflight_does_not_create_usable_profile(self):
        key = self.root / 'gateway-key.txt'
        key.write_text('fixture', encoding='utf-8')
        with self.assertRaises((ValueError, OSError)):
            self.prepare(preflight=True, key_file=key, mesh_source=self.root / 'missing-mesh')
        self.assertFalse((self.root / 'new-profile').exists())

    def test_discovery_launcher_isolated_and_does_not_read_real_key(self):
        self.prepare()
        out = self.root / 'new-profile'
        previous = dict(os.environ)
        argv = [str(out / 'launch-client.py'), '--client', 'opencode', '--client-exe', str(self.binary)]
        with mock.patch.object(sys, 'argv', argv), mock.patch('subprocess.run', return_value=types.SimpleNamespace(returncode=0)) as run:
            with self.assertRaises(SystemExit) as exited:
                runpy.run_path(str(out / 'launch-client.py'), run_name='__main__')
        self.assertEqual(exited.exception.code, 0)
        self.assertEqual(run.call_args.args[0][-3:], ['mcp', 'list', '--pure'])
        env = run.call_args.kwargs['env']
        self.assertEqual(env['AGENTMESH_API_KEY'], 'discovery-no-model-key')
        self.assertEqual(env['OPENCODE_DISABLE_PROJECT_CONFIG'], 'true')
        self.assertEqual(env['OPENCODE_CONFIG'], str(out / 'opencode.json'))
        self.assertTrue(Path(env['XDG_DATA_HOME']).is_relative_to(out))
        self.assertEqual(dict(os.environ), previous)


if __name__ == '__main__':
    unittest.main()
