"""Run the observation-driven Agent scenario against disposable local MySQL.

Requires Go and an already available official mysql:8.4.11 image. No real
model, API key, email, existing database or Kafka broker is used.
"""
import argparse
import collections
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import uuid


def run(repo, evidence, outer=False):
    if not (repo / 'go.mod').read_text(encoding='utf-8').startswith('module sqlsentinel'):
        raise ValueError('SQL Sentinel source checkout required')
    evidence.mkdir(parents=True, exist_ok=True)
    subprocess.run(['docker', 'image', 'inspect', 'mysql:8.4.11'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    container = None
    password = secrets.token_hex(32)
    with tempfile.TemporaryDirectory(prefix='sentinel-agent-acceptance-') as temporary:
        private = Path(temporary).resolve()
        # Validate the sole recursive-cleanup target managed by TemporaryDirectory.
        if not private.is_relative_to(Path(tempfile.gettempdir()).resolve()) or not private.name.startswith('sentinel-agent-acceptance-'):
            raise ValueError('unexpected private test directory')
        (private / 'password').write_text(password, encoding='utf-8')
        try:
            started = subprocess.check_output([
                'docker', 'run', '--pull', 'never', '-d', '--name', 'sentinel-agent-test-' + uuid.uuid4().hex[:12],
                '--label', 'purpose=sentinel-agent-acceptance', '--memory', '1024m', '--cpus', '2',
                '--tmpfs', '/var/lib/mysql:rw,size=1024m', '--mount', f'type=bind,source={private},target=/run/test-private,readonly',
                '-e', 'MYSQL_ROOT_PASSWORD_FILE=/run/test-private/password', '-e', 'MYSQL_ROOT_HOST=%',
                '-p', '127.0.0.1::3306', 'mysql:8.4.11'], text=True).strip()
            if not re.fullmatch('[a-f0-9]{64}', started):
                raise RuntimeError('unexpected test container identity')
            container = started
            shell = ['docker', 'exec', container, 'sh', '-c']
            for _ in range(60):
                probe = subprocess.run([*shell, 'MYSQL_PWD=$(cat /run/test-private/password) mysql -uroot -N -e "SELECT 1"'], capture_output=True, timeout=5)
                if probe.returncode == 0:
                    break
                time.sleep(1)
            else:
                raise RuntimeError('disposable MySQL not ready')
            schema = 'CREATE DATABASE sentinel_improvement; CREATE DATABASE agent_baseline; CREATE DATABASE agent_candidate;'
            subprocess.run(['docker', 'exec', '-i', container, 'sh', '-c', 'MYSQL_PWD=$(cat /run/test-private/password) mysql -uroot'], input=schema.encode(), check=True, capture_output=True)
            ports = json.loads(subprocess.check_output(['docker', 'inspect', container]))[0]['NetworkSettings']['Ports']
            port = ports['3306/tcp'][0]['HostPort']
            env = dict(os.environ, GOMAXPROCS='2')
            for key in ['OPENAI_API_KEY', 'ANTHROPIC_AUTH_TOKEN', 'EINO_MODEL', 'EINO_BASE_URL', 'SQL_SENTINEL_ES_URL']:
                env.pop(key, None)
            for label, database in [('CONTROL', 'sentinel_improvement'), ('BASELINE', 'agent_baseline'), ('CANDIDATE', 'agent_candidate')]:
                file = private / (label.lower() + '-dsn')
                file.write_text(f'root:{password}@tcp(127.0.0.1:{port})/{database}?parseTime=true&loc=UTC&timeout=3s&readTimeout=35s&writeTimeout=10s', encoding='utf-8')
                env[f'SQL_SENTINEL_AGENT_{label}_DSN_FILE'] = str(file)
            packages = ['./internal/agentrun', './internal/service'] if outer else ['./internal/service']
            pattern = '^(TestAgentRunMySQLIntegration|TestWebsiteAgentRealMySQLIntegration|TestMCPRealMySQLIntegration|TestAgentRealMySQLIntegration)$' if outer else '^TestAgentRealMySQLIntegration$'
            test = subprocess.run(['go', 'test', '-p', '1', *packages, '-run', pattern, '-count=1', '-json', '-timeout=4m'], cwd=repo, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=420)
            output = test.stdout.decode('utf-8', errors='replace').replace(password, '[redacted]')
            (evidence / 'agent-mysql.jsonl').write_text(output, encoding='utf-8')
            counts = collections.Counter()
            passed = set()
            for line in output.splitlines():
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if event.get('Test') and event.get('Action') in ['pass', 'fail', 'skip']:
                    counts[event['Action']] += 1
                    if event['Action'] == 'pass':
                        passed.add(event['Test'])
            ok = test.returncode == 0 and 'TestAgentRealMySQLIntegration' in passed and counts['skip'] == 0
            if outer:
                ok = ok and {'TestAgentRunMySQLIntegration', 'TestWebsiteAgentRealMySQLIntegration', 'TestMCPRealMySQLIntegration'}.issubset(passed)
            result = dict(status='PASS' if ok else 'FAIL', test_exit_code=test.returncode, counts=dict(counts), database='disposable MySQL 8.4.11', model='local OpenAI-compatible fixture', queue='durable outbox delivered to Worker.Handle; no broker claim', paid_model_calls=0, production_or_cloud_test=False)
            (evidence / 'agent-result.json').write_text(json.dumps(result, indent=2), encoding='utf-8')
            return ok
        finally:
            if container:
                subprocess.run(['docker', 'rm', '-f', container], check=True, capture_output=True, timeout=30)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--outer', action='store_true', help='Also verify durable website runs and the outer-to-inner real MySQL path.')
    args = parser.parse_args()
    try:
        passed = run(Path(__file__).resolve().parent.parent, args.evidence_dir.resolve(), args.outer)
    except Exception:
        raise SystemExit('Agent acceptance could not complete. Check Docker, the cached official MySQL image, Go and the evidence directory. No credentials printed.')
    print('PASS isolated Agent acceptance' if passed else 'FAIL isolated Agent acceptance; inspect evidence')
    raise SystemExit(0 if passed else 1)
