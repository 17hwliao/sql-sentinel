param(
    [string]$BaseUrl = 'http://127.0.0.1:18081',
    [string]$Secret = $env:SQL_SENTINEL_WEBHOOK_SECRET
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Secret)) { throw 'SQL_SENTINEL_WEBHOOK_SECRET is required' }

$deliveryId = 'verify-' + [Guid]::NewGuid().ToString('N').Substring(0, 16)
$diff = @"
diff --git a/query.sql b/query.sql
new file mode 100644
--- /dev/null
+++ b/query.sql
+SELECT id, amount_cents
+FROM orders
+WHERE status = 'paid'
+ORDER BY amount_cents DESC
+LIMIT 20;
"@
$body = [ordered]@{ delivery_id = $deliveryId; pr_number = 1; diff = $diff } | ConvertTo-Json -Compress
$bytes = [Text.Encoding]::UTF8.GetBytes($body)
$key = [Text.Encoding]::UTF8.GetBytes($Secret)
$mac = [Security.Cryptography.HMACSHA256]::new($key).ComputeHash($bytes)
$signature = 'sha256=' + (-join ($mac | ForEach-Object { $_.ToString('x2') }))
$headers = @{ 'X-SQL-Sentinel-Signature' = $signature }

$accepted = (Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/webhook/pr" -Method Post -ContentType 'application/json' -Headers $headers -Body $body -TimeoutSec 10).Content | ConvertFrom-Json
if ($accepted.verification_status -ne 'queued' -or [string]::IsNullOrWhiteSpace($accepted.job_id)) { throw 'webhook was not queued' }

$status = $null
for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Milliseconds 500
    $status = (Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/jobs/$deliveryId" -TimeoutSec 5).Content | ConvertFrom-Json
    if ($status.status -in @('COMPLETED', 'REJECTED', 'DEAD_LETTER')) { break }
}
if ($status.status -ne 'COMPLETED') { throw "workflow terminal status: $($status.status)" }

$duplicate = (Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/webhook/pr" -Method Post -ContentType 'application/json' -Headers $headers -Body $body -TimeoutSec 10).Content | ConvertFrom-Json
if (-not $duplicate.duplicate -or $duplicate.job_id -ne $accepted.job_id) { throw 'duplicate delivery did not reuse the original job' }
if ($status.attempts -ne 1) { throw "unexpected worker attempts: $($status.attempts)" }

[ordered]@{
    status = 'verified'
    delivery_id = $deliveryId
    job_id = $accepted.job_id
    worker_status = $status.status
    attempts = $status.attempts
    sql_sha256 = $status.sql_sha256
} | ConvertTo-Json -Compress
