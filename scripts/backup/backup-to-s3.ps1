<#
.SYNOPSIS
    mini-aigw SQLite データベースの整合性ホットバックアップ & S3 アップロードスクリプト
.DESCRIPTION
    本スクリプトは、稼働中の mini-aigw サーバーを停止することなく、
    SQLite の VACUUM INTO 機能を呼び出してトランザクション整合性のある
    データベーススナップショットを作成し、ZIP 圧縮して AWS S3 へ暗号化アップロードします。
.EXAMPLE
    # タスクスケジューラでの実行例:
    # powershell.exe -ExecutionPolicy Bypass -NoProfile -File "C:\aigw\scripts\backup\backup-to-s3.ps1"
#>

[CmdletBinding()]
param (
    [string]$AppDir      = "C:\aigw",
    [string]$BucketName  = "beppu-ih-backup",
    [string]$S3Prefix    = "mini-aigw",
    [string]$Region      = "ap-northeast-1",
    # 暗号化方式: Amazon S3 マネージドキー (SSE-S3) の場合は "AES256"
    [string]$SSE         = "AES256"
)

$ErrorActionPreference = "Stop"

# --- パス定義 ---
$DbPath     = Join-Path $AppDir "data\gateway.db"
$LogDir     = Join-Path $AppDir "logs"
$TempDir    = Join-Path $AppDir "temp\backup"
$BinaryPath = Join-Path $AppDir "mini-aigw.exe"

# ログディレクトリおよび一時ディレクトリの作成
if (-not (Test-Path $LogDir)) {
    New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
}
if (-not (Test-Path $TempDir)) {
    New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
}

$LogFile = Join-Path $LogDir "backup.log"

# 前回ログクリア
if (Test-Path $LogFile) {
    Clear-Content -Path $LogFile -ErrorAction SilentlyContinue
}

# --- ログ出力ヘルパー ---
function Write-Log {
    param(
        [string]$Message,
        [ValidateSet("INFO", "WARN", "ERROR")][string]$Level = "INFO"
    )
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $line = "[$timestamp] [$Level] $Message"
    
    switch ($Level) {
        "INFO"  { Write-Host $line -ForegroundColor Cyan }
        "WARN"  { Write-Host $line -ForegroundColor Yellow }
        "ERROR" { Write-Host $line -ForegroundColor Red }
    }
    
    try {
        Add-Content -Path $LogFile -Value $line -Encoding UTF8
    } catch {
        # ログ書き込み失敗時も処理は継続
    }
}

# --- メイン処理 ---
try {
    Write-Log "============================================="
    Write-Log "mini-aigw バックアップ処理を開始します"
    Write-Log "対象DB: $DbPath"
    Write-Log "転送先: s3://$BucketName/$S3Prefix/"
    Write-Log "暗号化: SSE-S3 ($SSE)"

    # 0. 前提チェック
    if (-not (Test-Path $BinaryPath)) {
        throw "mini-aigw バイナリが見つかりません: $BinaryPath"
    }
    if (-not (Test-Path $DbPath)) {
        throw "対象のデータベースファイルが見つかりません: $DbPath"
    }

    $Timestamp    = Get-Date -Format "yyyyMMdd_HHmmss"
    $BackupDbName = "gateway_$Timestamp.db"
    $BackupDbPath = Join-Path $TempDir $BackupDbName
    $ZipName      = "gateway_$Timestamp.zip"
    $ZipPath      = Join-Path $TempDir $ZipName
    $S3Dest       = "s3://$BucketName/$S3Prefix/$Timestamp/$ZipName"

    # 1. mini-aigw CLI によるホットバックアップ (VACUUM INTO)
    Write-Log "Step 1: 整合性ホットバックアップを作成中: $BackupDbPath"
    $backupOutput = & $BinaryPath backup -db $DbPath -out $BackupDbPath 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "mini-aigw backup コマンドが失敗しました (ExitCode: $LASTEXITCODE)`n出力: $backupOutput"
    }
    Write-Log "ホットバックアップ完了: $backupOutput"

    # 2. ZIP 圧縮 (ファイル容量および転送時間の削減)
    Write-Log "Step 2: バックアップファイルを ZIP 圧縮中: $ZipPath"
    Compress-Archive -Path $BackupDbPath -DestinationPath $ZipPath -Force
    # 圧縮完了後に生DBファイルを削除
    Remove-Item -Path $BackupDbPath -Force -ErrorAction SilentlyContinue

    $zipItem = Get-Item $ZipPath
    $zipSizeKB = [math]::Round($zipItem.Length / 1KB, 2)
    Write-Log "圧縮完了: $ZipName ($zipSizeKB KB)"

    # 3. AWS S3 へアップロード
    Write-Log "Step 3: S3 へ暗号化アップロード中: $S3Dest"
    $awsArgs = @(
        "s3", "cp",
        $ZipPath,
        $S3Dest,
        "--region", $Region,
        "--no-progress"
    )

    if ($SSE -eq "AES256") {
        $awsArgs += @("--sse", "AES256")
    } elseif ($SSE -eq "aws:kms") {
        $awsArgs += @("--sse", "aws:kms")
    }

    $awsOutput = & aws @awsArgs 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "AWS S3 アップロードに失敗しました (ExitCode: $LASTEXITCODE)`nエラー詳細: $awsOutput"
    }

    Write-Log "S3 アップロード成功: $S3Dest"

    # 4. 一時 ZIP ファイルの削除
    Write-Log "Step 4: ローカル一時ファイルをクリーンアップ中..."
    Remove-Item -Path $ZipPath -Force -ErrorAction SilentlyContinue

    # 5. (オプション) 7日以上前のローカル一時フォルダ残骸があれば掃除
    Get-ChildItem -Path $TempDir -Filter "gateway_*.zip" -ErrorAction SilentlyContinue |
        Where-Object { $_.LastWriteTime -lt (Get-Date).AddDays(-7) } |
        Remove-Item -Force -ErrorAction SilentlyContinue

    Write-Log "バックアップ処理が正常に完了しました。"
    Write-Log "============================================="
    exit 0
}
catch {
    Write-Log "バックアップ処理が異常終了しました: $_" "ERROR"
    Write-Log "============================================="
    exit 1
}

