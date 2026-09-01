$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$required = @('AGENTMESH_REPO', 'OLLAMA_BASE_URL', 'OLLAMA_MODEL')
$missing = @($required | Where-Object { [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($_, 'Process')) })
if ($missing.Count -gt 0) {
    [ordered]@{ status = 'verification_unavailable'; code = 'bridge_environment_missing'; network_attempts = 0 } | ConvertTo-Json -Compress
    exit 1
}

$agentMeshRepo = (Resolve-Path -LiteralPath $env:AGENTMESH_REPO).Path
if (-not (Test-Path -LiteralPath (Join-Path $agentMeshRepo '.git'))) {
    [ordered]@{ status = 'verification_unavailable'; code = 'agentmesh_repository_invalid'; network_attempts = 0 } | ConvertTo-Json -Compress
    exit 1
}

$agentMeshCommit = (& git -C $agentMeshRepo rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($agentMeshCommit)) {
    [ordered]@{ status = 'verification_unavailable'; code = 'agentmesh_commit_unavailable'; network_attempts = 0 } | ConvertTo-Json -Compress
    exit 1
}
if ((@(& git -C $agentMeshRepo status --porcelain)).Count -ne 0) {
    [ordered]@{ status = 'verification_unavailable'; code = 'agentmesh_worktree_dirty'; network_attempts = 0 } | ConvertTo-Json -Compress
    exit 1
}

$port = 18185
if (@(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue).Count -ne 0) {
    [ordered]@{ status = 'verification_unavailable'; code = 'bridge_port_unavailable'; port = $port; network_attempts = 0 } | ConvertTo-Json -Compress
    exit 1
}

$variables = @(
    'AGENTMESH_BOOTSTRAP_API_KEY', 'AGENTMESH_BOOTSTRAP_TENANT_ID', 'AGENTMESH_BOOTSTRAP_MODEL_ROUTES',
    'AGENTMESH_API_KEY', 'AGENTMESH_AUTH_STORE', 'AGENTMESH_AUTH_MYSQL_DSN', 'AGENTMESH_ADMIN_TOKEN',
    'AGENTMESH_QUOTA_MODE', 'AGENTMESH_QUOTA_MYSQL_DSN', 'AGENTMESH_QUOTA_REDIS_URL',
    'AGENTMESH_BASE_URL', 'AGENTMESH_MODEL', 'AGENTMESH_COMMIT_SHA'
)
$saved = @{}
foreach ($name in $variables) {
    $entry = Get-Item "Env:$name" -ErrorAction SilentlyContinue
    $saved[$name] = @{ Exists = ($null -ne $entry); Value = if ($null -eq $entry) { '' } else { $entry.Value } }
}

$keyBytes = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($keyBytes)
$rng.Dispose()
$temporaryKey = -join ($keyBytes | ForEach-Object { $_.ToString('x2') })
$temporaryRoot = Join-Path $env:TEMP 'sql-sentinel-agentmesh-bridge'
$apiBinary = "$temporaryRoot-api.exe"
$stdoutLog = "$temporaryRoot.out"
$stderrLog = "$temporaryRoot.err"
$reportPath = Join-Path (Join-Path (Get-Location).Path '.private') ("agentmesh-bridge-{0}.json" -f (Get-Date -Format 'yyyyMMdd-HHmmss'))
$process = $null

try {
    Remove-Item -LiteralPath $apiBinary, $stdoutLog, $stderrLog -Force -ErrorAction SilentlyContinue
    $env:AGENTMESH_BOOTSTRAP_API_KEY = $temporaryKey
    $env:AGENTMESH_BOOTSTRAP_TENANT_ID = 'sql_sentinel_bridge'
    $env:AGENTMESH_BOOTSTRAP_MODEL_ROUTES = '{"bridge-connectivity":["ollama"]}'
    $env:AGENTMESH_API_KEY = $temporaryKey
    $env:AGENTMESH_BASE_URL = "http://127.0.0.1:$port"
    $env:AGENTMESH_MODEL = 'bridge-connectivity'
    $env:AGENTMESH_COMMIT_SHA = $agentMeshCommit
    Remove-Item Env:AGENTMESH_AUTH_STORE, Env:AGENTMESH_AUTH_MYSQL_DSN, Env:AGENTMESH_ADMIN_TOKEN -ErrorAction SilentlyContinue
    Remove-Item Env:AGENTMESH_QUOTA_MODE, Env:AGENTMESH_QUOTA_MYSQL_DSN, Env:AGENTMESH_QUOTA_REDIS_URL -ErrorAction SilentlyContinue

    Push-Location $agentMeshRepo
    try {
        & go build -o $apiBinary ./cmd/api
        if ($LASTEXITCODE -ne 0) { throw 'agentmesh_bridge_gateway_build_failed' }
    } finally {
        Pop-Location
    }
    $process = Start-Process -FilePath $apiBinary -ArgumentList @('--addr', "127.0.0.1:$port", '--providers', 'ollama') -WorkingDirectory $agentMeshRepo -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
    $ready = $false
    for ($index = 0; $index -lt 80; $index++) {
        Start-Sleep -Milliseconds 250
        try {
            if ((Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/healthz" -TimeoutSec 1).StatusCode -eq 200) {
                $ready = $true
                break
            }
        } catch {}
    }
    if (-not $ready) { throw 'agentmesh_bridge_gateway_unavailable' }

    & go run ./cmd/sentinel agentmesh-bridge-test --out $reportPath
    $bridgeExit = $LASTEXITCODE
    Get-Content -LiteralPath $reportPath -Raw
    if ($bridgeExit -ne 0) { exit $bridgeExit }
} catch {
    [ordered]@{ status = 'verification_failed'; code = 'agentmesh_bridge_script_failed'; network_attempts = $null } | ConvertTo-Json -Compress
    exit 1
} finally {
    if ($process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force
        $process.WaitForExit()
    }
    Remove-Item -LiteralPath $apiBinary, $stdoutLog, $stderrLog -Force -ErrorAction SilentlyContinue
    foreach ($name in $variables) {
        if ($saved[$name].Exists) { Set-Item "Env:$name" $saved[$name].Value } else { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
    }
}
