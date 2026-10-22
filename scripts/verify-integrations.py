"""Disposable Linux integration tests. No host ports, reused volumes or ambient credentials."""
import argparse
import collections
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid

REQUIRED = {
    "AgentMesh": {"TestPersistentMoneyStateInvariants", "TestRealMySQLMoneySettlementAndRecovery",
                  "TestPersistentProcessCrashRecovery",
                  "TestRealRedisTerminalIdempotencySurvivesVersionChanges", "TestRealRedisMissingBalanceCannotBeRecreatedByTerminal",
                  "TestRealRedisCacheIsolationTTLAndBound", "TestRealKafkaFailedFirstOffsetIsRetriedBeforeSecondAndCommitted"},
    "sql-sentinel": {"TestPersistentWorkflowStateInvariants", "TestPersistentSQLCounterexamples", "TestPersistentSkewIndexNegativeEvidence",
                     "TestRealLockExcludesConcurrentRunsAndReleasesAfterCancellation", "TestRealScopedConsoleLists", "TestConsolePerformanceFixtureMatchesGoWireContract"},
}

def command(args, **kw):
    p = subprocess.run(args, capture_output=True, text=True, encoding="utf-8", errors="replace", **kw)
    if p.returncode:
        # Docker/test commands may contain secrets in diagnostics. Never echo them.
        raise RuntimeError("command failed: " + args[0] + " (see private verification logs)")
    return p.stdout.strip()

def classify(log, required):
    counts = collections.Counter()
    passed, skipped, failed = set(), set(), set()
    for line in log.splitlines():
        try:
            row = json.loads(line)
        except ValueError:
            continue
        if not isinstance(row, dict) or not row.get("Test"):
            continue
        action = row.get("Action")
        if action in {"pass", "skip", "fail"}:
            counts[action] += 1
            {"pass": passed, "skip": skipped, "fail": failed}[action].add(row["Test"])
    missing = sorted(required - passed)
    return {"counts": dict(counts), "mandatory_missing": missing, "skipped": sorted(skipped),
            "failed": sorted(failed), "status": "FAIL" if missing or failed else "PASS"}

def run(repo, image, out):
    if not (repo / "go.mod").is_file() or repo.name not in REQUIRED:
        raise ValueError("explicit AgentMesh or sql-sentinel checkout required")
    out.mkdir(parents=True, exist_ok=True)
    suffix = uuid.uuid4().hex[:12]
    network = "backend-integration-" + suffix
    containers = []
    network_created = False
    try:
        with tempfile.TemporaryDirectory(prefix="backend-integration-") as temporary:
            private = Path(temporary).resolve()
            password = secrets.token_hex(24)
            (private / "password").write_text(password, encoding="utf-8")
            (private / "init.sql").write_text("CREATE DATABASE agentmesh_improvement;\nCREATE DATABASE sentinel_improvement;\n", encoding="utf-8")
            for name, database in (("am_dsn", "agentmesh_improvement"), ("ss_dsn", "sentinel_improvement")):
                (private / name).write_text("root:" + password + "@tcp(mysql:3306)/" + database + "?parseTime=true&loc=UTC&timeout=5s&readTimeout=30s&writeTimeout=10s", encoding="utf-8")
            command(["docker", "network", "create", "--internal", "--label", "purpose=backend-integration", network])
            network_created = True
            def start(alias, image_name, extras):
                name = "backend-integration-" + alias + "-" + suffix
                command(["docker", "run", "-d", "--name", name, "--label", "purpose=backend-integration", "--network", network,
                         "--network-alias", alias, *extras, image_name])
                containers.append(name)
                return name
            mysql = start("mysql", "mysql:8.4.11", ["--memory", "1536m", "--tmpfs", "/var/lib/mysql:rw,size=1536m",
                "--mount", f"type=bind,source={private},target=/run/test-secrets,readonly",
                "--mount", f"type=bind,source={private / 'init.sql'},target=/docker-entrypoint-initdb.d/10-test.sql,readonly",
                "-e", "MYSQL_ROOT_PASSWORD_FILE=/run/test-secrets/password", "-e", "MYSQL_ROOT_HOST=%"])
            redis = start("redis", "redis:7.4-alpine", ["--memory", "128m"])
            kafka_env = {"KAFKA_NODE_ID": "1", "KAFKA_PROCESS_ROLES": "broker,controller",
                "KAFKA_LISTENERS": "INTERNAL://:9092,CONTROLLER://:9093", "KAFKA_ADVERTISED_LISTENERS": "INTERNAL://kafka:9092",
                "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "INTERNAL:PLAINTEXT,CONTROLLER:PLAINTEXT",
                "KAFKA_CONTROLLER_LISTENER_NAMES": "CONTROLLER", "KAFKA_INTER_BROKER_LISTENER_NAME": "INTERNAL",
                "KAFKA_CONTROLLER_QUORUM_VOTERS": "1@kafka:9093", "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1",
                "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1", "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR": "1",
                "KAFKA_AUTO_CREATE_TOPICS_ENABLE": "false", "KAFKA_HEAP_OPTS": "-Xms256M -Xmx512M",
                "CLUSTER_ID": "MkU3OEVBNTcwNTJENDM2Qk"}
            kafka = start("kafka", "apache/kafka:4.3.1", ["--memory", "1g", *[x for k, v in kafka_env.items() for x in ("-e", k + "=" + v)]])
            checks = [(mysql, ["sh", "-c", 'MYSQL_PWD="$(cat /run/test-secrets/password)" mysql -uroot -N -e "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name IN (\'agentmesh_improvement\',\'sentinel_improvement\')"'], "2"),
                      (redis, ["redis-cli", "ping"], "PONG"),
                      (kafka, ["/opt/kafka/bin/kafka-broker-api-versions.sh", "--bootstrap-server", "kafka:9092"], None)]
            for name, args, expected in checks:
                deadline = time.monotonic() + 150
                while time.monotonic() < deadline:
                    p = subprocess.run(["docker", "exec", name, *args], capture_output=True, text=True)
                    if p.returncode == 0 and (expected is None or p.stdout.strip() == expected):
                        break
                    time.sleep(1)
                else:
                    raise RuntimeError("disposable dependency not ready: " + name)
            script = ('export GOMAXPROCS=4; export AGENTMESH_TEST_MYSQL_DSN="$(cat /run/test-secrets/am_dsn)"; '
                      'export SQL_SENTINEL_TEST_MYSQL_DSN="$(cat /run/test-secrets/ss_dsn)"; '
                      'export SQL_SENTINEL_TEST_LOCK_DSN_FILE=/run/test-secrets/ss_dsn; '
                      'export AGENTMESH_TEST_REDIS_URL=redis://redis:6379/0; export AGENTMESH_TEST_KAFKA_BROKER=kafka:9092; '
                      'CGO_ENABLED=1 go test -race -json -count=1 -timeout=8m ./...')
            logfile = out / (repo.name + ".jsonl")
            with logfile.open("w", encoding="utf-8") as f:
                p = subprocess.run(["docker", "run", "--rm", "--network", network, "--cpus", "4", "--memory", "2g",
                    "--mount", f"type=bind,source={repo},target=/src,readonly", "--mount", f"type=bind,source={private},target=/run/test-secrets,readonly",
                    "-w", "/src", "--entrypoint", "sh", image, "-c", script], stdout=f, stderr=subprocess.STDOUT)
            text = logfile.read_text(encoding="utf-8").replace(password, "[redacted]")
            logfile.write_text(text, encoding="utf-8")
            result = classify(text, REQUIRED[repo.name])
            result.update(exit_code=p.returncode, repository=repo.name, log_sha256=hashlib.sha256(logfile.read_bytes()).hexdigest())
            result["dependency_states"] = {name.split("-")[2]: json.loads(command(["docker","inspect","--format","{{json .State}}",name])) for name in containers}
            if p.returncode:
                result["status"] = "FAIL"
            (out / (repo.name + ".result.json")).write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
            print(json.dumps(result), flush=True)
            if result["status"] != "PASS":
                raise RuntimeError("integration suite failed or mandatory tests skipped")
    finally:
        for name in reversed(containers):
            subprocess.run(["docker", "rm", "-f", name], capture_output=True)
        if network_created:
            subprocess.run(["docker", "network", "rm", network], capture_output=True)

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--runner-image", required=True)
    parser.add_argument("--out", type=Path, default=Path(".private/integration-results"))
    args = parser.parse_args()
    run(args.repo.resolve(), args.runner_image, args.out.resolve())
