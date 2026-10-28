<#
.SYNOPSIS
    業務チャットと同じ形式 (OpenAI 互換 "file" パート + base64) で PDF を mini-aigw に送信し、エラーを再現する。
.EXAMPLE
    $env:AIGW_API_KEY = "sk-xxx"
    .\scripts\repro\send_pdf.ps1 -Pdf .\error_sample\sample.pdf
    .\scripts\repro\send_pdf.ps1 -Pdf .\sample.pdf -Model jp.anthropic.claude-haiku-5-5 -Stream
#>
[CmdletBinding()]
param (
    [Parameter(Mandatory = $true)][string]$Pdf,
    [string]$BaseUrl = $(if ($env:AIGW_URL) { $env:AIGW_URL } else { "http://localhost:8000/v1" }),
    [string]$ApiKey = $env:AIGW_API_KEY,
    [string]$Model = "",
    [string]$Prompt = "この資料を要約してください",
    [switch]$Stream
)

if (-not $ApiKey) { throw "AIGW_API_KEY (or -ApiKey) is required" }

$bytes = [System.IO.File]::ReadAllBytes((Resolve-Path $Pdf))
$b64 = [System.Convert]::ToBase64String($bytes)

$payload = [ordered]@{
    stream   = [bool]$Stream
    messages = @(@{
        role    = "user"
        content = @(
            @{ type = "text"; text = $Prompt },
            @{ type = "file"; file = @{ filename = [System.IO.Path]::GetFileName($Pdf); file_data = "data:application/pdf;base64,$b64" } }
        )
    })
}
if ($Model) { $payload["model"] = $Model }

$body = [System.Text.Encoding]::UTF8.GetBytes(($payload | ConvertTo-Json -Depth 10 -Compress))
Write-Host "file      : $Pdf ($($bytes.Length) bytes)"
Write-Host "body size : $($body.Length) bytes"
Write-Host "endpoint  : $BaseUrl/chat/completions (stream=$([bool]$Stream))"
Write-Host "----------------------------------------"

$sw = [System.Diagnostics.Stopwatch]::StartNew()
try {
    $resp = Invoke-WebRequest -Uri "$BaseUrl/chat/completions" -Method Post `
        -Headers @{ Authorization = "Bearer $ApiKey" } -ContentType "application/json; charset=utf-8" `
        -Body $body -UseBasicParsing -TimeoutSec 600
    Write-Host "http_status=$($resp.StatusCode) X-Request-Id=$($resp.Headers['X-Request-Id'])"
    Write-Output $resp.Content
} catch {
    $r = $_.Exception.Response
    if ($r) {
        Write-Host "http_status=$([int]$r.StatusCode)"
        $reader = New-Object System.IO.StreamReader($r.GetResponseStream())
        Write-Output $reader.ReadToEnd()
    } else {
        Write-Host "request failed: $($_.Exception.Message)"
    }
}
Write-Host "time_total=$($sw.Elapsed.TotalSeconds)s"
Write-Host "※ エラー時は data\logs\error.log (ERROR_LOG_FILE) を確認してください"
