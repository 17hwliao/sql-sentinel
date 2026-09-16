param(
    [string]$BaseUrl = 'http://127.0.0.1:18081',
    [string]$Secret = $env:SQL_SENTINEL_WEBHOOK_SECRET,
    [string]$ControlToken = $env:SQL_SENTINEL_CONTROL_TOKEN
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Secret)) { throw 'SQL_SENTINEL_WEBHOOK_SECRET is required' }
if ([string]::IsNullOrWhiteSpace($ControlToken)) { throw 'SQL_SENTINEL_CONTROL_TOKEN is required' }

$deliveryId = 'dlq-' + [Guid]::NewGuid().ToString('N').Substring(0, 16)
$diff = @"
diff --git a/query.sql b/query.sql
new file mode 100644
--- /dev/null
+++ b/query.sql
+SELECT id FROM missing_sentinel_table;
"@
$body = [ordered]@{ delivery_id = $deliveryId; pr_number = 2; diff = $diff } | ConvertTo-Json -Compress
$bytes = [Text.Encoding]::UTF8.GetBytes($body)
$key = [Text.Encoding]::UTF8.GetBytes($Secret)
$mac = [Security.Cryptography.HMACSHA256]::new($key).ComputeHash($bytes)
$signature = 'sha256=' + (-join ($mac | ForEach-Object { $_.ToString('x2') }))
$webhookHeaders = @{ 'X-SQL-Sentinel-Signature' = $signature }
$controlHeaders = @{ Authorization = "Bearer $ControlToken" }

$null = Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/webhook/pr" -Method Post -ContentType 'application/json' -Headers $webhookHeaders -Body $body -TimeoutSec 10
$status = $null
for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Milliseconds 500
    $status = (Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/jobs/$deliveryId" -TimeoutSec 5).Content | ConvertFrom-Json
    if ($status.status -in @('COMPLETED', 'REJECTED', 'DEAD_LETTER')) { break }
}
if ($status.status -ne 'DEAD_LETTER') { throw "expected DEAD_LETTER, got $($status.status); start kafka-serve with --max-attempts 1" }

$history = (Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/jobs/$deliveryId/history" -Headers $controlHeaders -TimeoutSec 5).Content | ConvertFrom-Json
if (-not (@($history.history.status) -contains 'DEAD_LETTER')) { throw 'dead-letter history was not retained' }
$replay = (Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/jobs/$deliveryId/replay" -Method Post -Headers $controlHeaders -TimeoutSec 5).Content | ConvertFrom-Json
if (-not $replay.replayed -or $replay.status -ne 'QUEUED') { throw 'dead-letter replay was not accepted' }

[ordered]@{
    status = 'verified'
    delivery_id = $deliveryId
    job_id = $replay.job_id
    replay_status = $replay.status
    history_entries = @($history.history).Count
} | ConvertTo-Json -Compress
