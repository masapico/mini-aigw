<#
.SYNOPSIS
    mini-aigw と同じ引数で xberg を実行し、終了コード・所要時間・出力文字数を表示する (サーバーホストでの切り分け用)。
.EXAMPLE
    .\scripts\repro\xberg_check.ps1 -Pdf .\error_sample\sample.pdf
    .\scripts\repro\xberg_check.ps1 -Pdf .\sample.pdf -Xberg C:\xberg-cli\xberg.exe -Ocr true
#>
[CmdletBinding()]
param (
    [Parameter(Mandatory = $true)][string]$Pdf,
    [string]$Xberg = $(if ($env:XBERG_PATH) { $env:XBERG_PATH } else { ".\xberg-cli\xberg.exe" }),
    [ValidateSet("true", "false")][string]$Ocr = $(if ($env:XBERG_OCR) { $env:XBERG_OCR } else { "false" })
)

if (Test-Path $Xberg -PathType Container) { $Xberg = Join-Path $Xberg "xberg.exe" }
$out = New-TemporaryFile
$err = New-TemporaryFile
try {
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $p = Start-Process -FilePath $Xberg -NoNewWindow -Wait -PassThru `
        -RedirectStandardOutput $out.FullName -RedirectStandardError $err.FullName `
        -ArgumentList @("extract", "`"$((Resolve-Path $Pdf).Path)`"", "--content-format", "markdown",
            "--extract-images", "false", "--pdf-extract-images", "false", "--ocr", $Ocr,
            "--no-config-discovery", "--log-level", "error")
    $sw.Stop()
    $text = [System.IO.File]::ReadAllText($out.FullName, [System.Text.Encoding]::UTF8)

    Write-Host "file        : $Pdf ($((Get-Item $Pdf).Length) bytes)"
    Write-Host "xberg       : $Xberg (ocr=$Ocr)"
    Write-Host "exit code   : $($p.ExitCode)"
    Write-Host "duration    : $($sw.Elapsed.TotalSeconds)s"
    Write-Host "output chars: $($text.Length)  (MAX_TOTAL_ATTACHMENT_CHARS 既定 50000 と比較)"
    Write-Host "--- stderr (tail) ---"
    Get-Content $err.FullName -Tail 20
} finally {
    Remove-Item $out, $err -ErrorAction SilentlyContinue
}
