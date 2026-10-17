# 閉域・オフライン環境向けパッケージ取得・インストール運用手順書 (Windows / Linux)

本ドキュメントは、インターネット接続のない**閉域環境（オフライン・エアギャップ環境）**の本番運用サーバー（Linux / Windows）に対し、`uv` を活用して `mini-aigw` の実行環境を安全かつ確実に構築・デプロイするための完全運用マニュアルです。

---

## 1. 概要とデプロイアーキテクチャ

### 1.1 閉域環境構築における課題
Python アプリケーションを閉域環境にデプロイする際、以下の要因で失敗するケースが多く見られます：
1. **PyPI へのアクセス不可**: `pip install` や `uv sync` が外部リポジトリへ通信を試みてタイムアウトまたは拒否される。
2. **C拡張（ネイティブコンパイル）の失敗**: `argon2-cffi-bindings`、`cryptography`、`uvloop`、`onnxruntime` など、C/C++ コンパイルが必要なライブラリが閉域環境のコンパイラ不在でビルド失敗する。
3. **OS・アーキテクチャの差異**: 開発環境（例: Windows x64）で取得したパッケージが本番環境（例: Linux x86_64）で動作しない。
4. **Python ランタイム自体の不在**: 閉域マシンに要件を満たす Python 3.12+ がインストールされていない。

### 1.2 持ち込み対象（配布物アーカイブ）の4要素
インターネット接続可能な作業端末（オンライン環境）で以下の4点を準備し、USB メディアやファイル転送ゲートウェイ経由で閉域環境に持ち込みます。

```
offline_deploy_bundle/
├── mini-aigw/                    # 1. アプリケーションコード (静的ファイル・alembic等含む)
│   ├── app/
│   ├── alembic/
│   ├── pyproject.toml
│   └── uv.lock
├── tools/                        # 2. スタンドアロン実行バイナリ
│   ├── uv                        #    Linux用 uv バイナリ (ELF 64-bit)
│   └── uv.exe                    #    Windows用 uv.exe バイナリ
├── wheelhouse/                   # 3. ターゲットOS向け事前ダウンロード済み Wheel群 (.whl)
│   ├── fastapi-0.143.0-py3-none-any.whl
│   ├── uvicorn-0.54.0-py3-none-any.whl
│   ├── cryptography-50.0.2-cp312-cp312-manylinux2014_x86_64.whl
│   └── ... (依存パッケージすべて)
├── requirements-prod.txt         #    本番用依存関係リスト (uv export で生成)
└── python_runtime/               # 4. Python 3.12+ インストーラ / スタンドアロン (未導入時のみ)
```

### 1.3 想定インフラ環境（AWS EC2 / 完全閉域 VPC）
本アプリケーション（`mini-aigw`）は、セキュリティ要件の厳しい企業・組織向けに、**AWS EC2 インスタンス（Amazon Linux 2023, RHEL 9, Ubuntu 22.04/24.04 等）** 上での本番稼働を主たるターゲットとして設計・検証されています。

特に、インターネットゲートウェイ (IGW) や NAT ゲートウェイを一切持たない**完全閉域（エアギャップ）プライベートサブネット内の EC2** での運用を想定しています。
パッケージのインストールおよびアップデートは、本マニュアルで解説するオフラインバンドル（Wheelhouse）持ち込み方式により、インターネット接続を一切必要とせずに完結します。

### 1.4 完全閉域環境における VPC エンドポイント設計
完全閉域 VPC では、パブリックインターネットへのアウトバウンド通信が存在しないため、`mini-aigw` が AWS サービス（Bedrock、S3 等）と通信するためには **VPC エンドポイント（AWS PrivateLink および Gateway 型エンドポイント）** の作成が必須となります。

以下のエンドポイントを対象 VPC に配置してください：

| サービス種別 | VPC エンドポイント サービス名 | タイプ | 用途 / 備考 |
|---|---|---|---|
| **Bedrock Runtime** | `com.amazonaws.<region>.bedrock-runtime` | Interface | Converse API / ConverseStream API / InvokeModel による LLM 推論 |
| **Bedrock Agent Runtime** | `com.amazonaws.<region>.bedrock-agent-runtime` | Interface | Bedrock Knowledge Base (S3 Vectors / Managed Store) の検索 (`Retrieve`) |
| **Bedrock Agent** | `com.amazonaws.<region>.bedrock-agent` | Interface | ナレッジベース同期ジョブ (`StartIngestionJob`, `ListIngestionJobs`) |
| **Amazon S3** | `com.amazonaws.<region>.s3` | Gateway (推奨) / Interface | ナレッジ原本ファイルおよびサイドカーメタデータ (`.metadata.json`) のアップロード・取得・削除。<br>※ 同一リージョン内の VPC であれば Gateway 型（無料・ルートテーブル設定）が推奨です。 |
| *(運用用)* **AWS Systems Manager (SSM)** | `com.amazonaws.<region>.ssm`<br>`com.amazonaws.<region>.ssmmessages`<br>`com.amazonaws.<region>.ec2messages` | Interface | 閉域 EC2 に対し、SSH ポートを開放せずマネジメントコンソールや CLI からセッションマネージャー経由で安全に接続・運用する場合に必要 |
| *(認証用)* **社内 OIDC IdP** | mini-id / Keycloak 等 | - | 認証に mini-id (OIDC) を使用する場合、社内閉域ネットワーク（Direct Connect / Transit Gateway / VPN）または VPC エンドポイントサービス経由でアクセス可能な経路を確保 |

> **VPC エンドポイント設定の重要ポイント**:
> 1. **プライベート DNS の有効化 (Private DNS Name)**:
>    - Interface 型エンドポイント作成時、「プライベート DNS 名を有効化 (Enable Private DNS Name)」に必ずチェックを入れてください。
>    - また、VPC 側の属性である「DNS 解決 (enableDnsHostnames)」および「DNS ホスト名 (enableDnsSupport)」が `true` に設定されている必要があります。
>    - これにより、`boto3` が内部で発行する標準のエンドポイント URL（例: `bedrock-runtime.ap-northeast-1.amazonaws.com`）が、自動的に VPC エンドポイントのプライベート ENI IP アドレスへ名前解決されます。
> 2. **セキュリティグループ設定**:
>    - EC2 インスタンスのセキュリティグループ: エンドポイント ENI 宛ての HTTPS (443/TCP) アウトバウンドを許可。
>    - Interface エンドポイント ENI のセキュリティグループ: EC2 インスタンスからの HTTPS (443/TCP) インバウンドを許可。

### 1.5 EC2 インスタンスに設定すべき IAM 権限（インスタンスプロファイル）
EC2 上で稼働させる場合、長期的な AWS アクセスキー（`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`）を `.env` や設定ファイルに記述してはなりません。
EC2 に **IAM ロール（インスタンスプロファイル）** をアタッチすることで、`boto3` がインスタンスメタデータサービス (IMDSv2) から一時クレデンシャルを自動的かつ安全に取得します。

EC2 用 IAM ロールにアタッチする最小特権ポリシーの構成例は以下の通りです：

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "BedrockModelInference",
      "Effect": "Allow",
      "Action": [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream"
      ],
      "Resource": [
        "arn:aws:bedrock:*::foundation-model/*",
        "arn:aws:bedrock:*:*:inference-profile/*"
      ]
    },
    {
      "Sid": "BedrockKnowledgeBaseRetrieval",
      "Effect": "Allow",
      "Action": [
        "bedrock:Retrieve"
      ],
      "Resource": [
        "arn:aws:bedrock:*:*:knowledge-base/*"
      ]
    },
    {
      "Sid": "BedrockKnowledgeBaseIngestionSync",
      "Effect": "Allow",
      "Action": [
        "bedrock:StartIngestionJob",
        "bedrock:GetIngestionJob",
        "bedrock:ListIngestionJobs",
        "bedrock:GetKnowledgeBase",
        "bedrock:GetDataSource"
      ],
      "Resource": [
        "arn:aws:bedrock:*:*:knowledge-base/*"
      ]
    },
    {
      "Sid": "KnowledgeS3BucketObjectAccess",
      "Effect": "Allow",
      "Action": [
        "s3:PutObject",
        "s3:GetObject",
        "s3:DeleteObject",
        "s3:AbortMultipartUpload"
      ],
      "Resource": [
        "arn:aws:s3:::<KNOWLEDGE_S3_BUCKET>/*"
      ]
    },
    {
      "Sid": "KnowledgeS3BucketListing",
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::<KNOWLEDGE_S3_BUCKET>"
      ]
    }
  ]
}
```

> **POINT**:
> - `<KNOWLEDGE_S3_BUCKET>` には `.env` の `KNOWLEDGE_S3_BUCKET` で指定する S3 バケット名を指定します。
> - 対象ナレッジベースを限定する場合は、Resource の `arn:aws:bedrock:*:*:knowledge-base/*` を特定のナレッジベース ID（`arn:aws:bedrock:<region>:<account-id>:knowledge-base/<BEDROCK_KB_ID>`）に絞り込んでください。
> - AWS Systems Manager (SSM) セッションマネージャー経由で EC2 にログインして作業・管理する場合は、AWS 管理ポリシー `AmazonSSMManagedInstanceCore` も併せてアタッチしてください。
> - **Bedrock のモデルアクセス権限**: 利用するモデル（Anthropic Claude, Amazon Nova 等）は、事前に AWS マネジメントコンソールの Amazon Bedrock 画面「Model access」にてアクセス申請・有効化（Granted）されている必要があります。

---

## 2. 【準備フェーズ】オンライン環境でのパッケージ事前取得

インターネット接続可能な作業端末で実施します。

### Step 2.1: 本番用依存関係リストの出力 (`uv export`)
開発環境で `uv export` を実行し、開発用ツール（pytest 等）を除外した本番実行用の依存関係リストを出力します。

```powershell
# PowerShell (Windows) / Bash (Linux) 共通
# リポジトリルートで実行
uv export --no-dev --no-emit-project --no-hashes -o requirements-prod.txt
```
> **POINT**:
> - `--no-dev`: テストライブラリ等の開発用依存関係を除外します。
> - `--no-emit-project`: `mini-aigw` 自身のパッケージ宣言を除外し、純粋な外部依存ライブラリ一覧とします。
> - `--no-hashes`: 異なるプラットフォーム向け Wheel を取得する際のハッシュ検証エラーを防止します。

---

### Step 2.2: ターゲットOS向け Wheel パッケージの一括ダウンロード (Wheelhouse 構築)

本番環境の OS / アーキテクチャに合わせて Wheel ファイルを一括ダウンロードします。

#### パターン A: 本番環境が Linux (x86_64 / Amazon Linux 2023, RHEL 9, Ubuntu 等) の場合

##### 推奨方式: オンライン端末上の Linux（WSL2, Docker, Linux機）で取得する場合
オンライン端末で Linux (WSL2 または Docker コンテナ) が使用可能な場合、環境の不整合が起きないため最も確実です。

```bash
# 作業ディレクトリ作成
mkdir -p wheelhouse_linux

# pip download で Linux 向け Wheel を一括取得
python3 -m pip download \
  --dest ./wheelhouse_linux \
  --only-binary=:all: \
  -r requirements-prod.txt
```

##### Windows 端末から直接 Linux 向け Wheel を取得する場合（クロスプラットフォーム）
オンライン端末が Windows のみの場合、`pip download` のプラットフォーム指定機能を使用します。

> **重要（環境マーカーの除去）**:
> `uv export` 出力に含まれる `sys_platform != 'win32'` などの環境マーカーが残っていると、Windows 上の pip は「現在の Windows 環境に合致しない」と判定して `uvloop` などの Linux 固有パッケージをスキップしてしまいます。
> そのため、ダウンロード用には環境マーカー（`;` 以降）を除去したファイルを使用します。

```powershell
# PowerShell (Windows)

# 1. 環境マーカーを除去した一時 requirements を生成
(Get-Content requirements-prod.txt) -replace ';.*$', '' | Set-Content requirements-linux-download.txt

# 2. 出力ディレクトリ作成
New-Item -ItemType Directory -Force -Path .\wheelhouse_linux

# 3. Linux x86_64 / Python 3.12 向けの Wheel を一括取得
# ※ 本番環境の Python バージョンに合わせて 3.12 や 3.13 を指定してください
python -m pip download `
  --dest .\wheelhouse_linux `
  --platform manylinux2014_x86_64 `
  --platform manylinux_2_17_x86_64 `
  --platform manylinux_2_28_x86_64 `
  --python-version 3.12 `
  --implementation cp `
  --abi cp312 `
  --only-binary=:all: `
  -r requirements-linux-download.txt

# 4. 一時ファイルの削除
Remove-Item requirements-linux-download.txt
```

---

#### パターン B: 本番環境が Windows (Windows Server 2022/2025, Windows 11 等) の場合

##### Windows 端末上でネイティブ取得する場合

```powershell
# PowerShell (Windows)
New-Item -ItemType Directory -Force -Path .\wheelhouse_windows

python -m pip download `
  --dest .\wheelhouse_windows `
  --only-binary=:all: `
  -r requirements-prod.txt
```

##### Linux 端末から Windows 向け Wheel を取得する場合（クロスプラットフォーム）

```bash
# Bash (Linux)
# 環境マーカーを除去
sed 's/;.*$//' requirements-prod.txt > requirements-win-download.txt

mkdir -p ./wheelhouse_windows

python3 -m pip download \
  --dest ./wheelhouse_windows \
  --platform win_amd64 \
  --python-version 3.12 \
  --implementation cp \
  --abi cp312 \
  --only-binary=:all: \
  -r requirements-win-download.txt

rm -f requirements-win-download.txt
```

---

### Step 2.3: `uv` スタンドアロンバイナリのダウンロード
`uv` は単一の自己完結型バイナリ（Rust 製）であり、外部依存なしで実行できます。閉域環境にも持ち込むことで、超高速な仮想環境作成とインストールが可能になります。

- **Linux (x86_64)**:
  - 公式リリースページからダウンロード:
    `https://github.com/astral-sh/uv/releases/latest/download/uv-x86_64-unknown-linux-gnu.tar.gz`
  - 展開すると `uv` および `uvx` バイナリが取り出せます。
- **Windows (x86_64)**:
  - 公式リリースページからダウンロード:
    `https://github.com/astral-sh/uv/releases/latest/download/uv-x86_64-pc-windows-msvc.zip`
  - 展開すると `uv.exe` および `uvx.exe` が取り出せます。

---

### Step 2.4: Python ランタイムの入手（閉域環境に未導入の場合）
閉域環境に Python 3.12+ が導入されていない場合は、インストーラまたはポータブル版を併せて持ち込みます。

- **Linux**: OS リポジトリ用 RPM/DEB パッケージ、または [python-build-standalone](https://github.com/astral-sh/python-build-standalone/releases) の tar.gz
- **Windows**: [Python 公式 Windows installer (64-bit)](https://www.python.org/downloads/windows/) の exe

---

### Step 2.5: 持ち出しパッケージのアーカイブ作成
取得した資産を 1 つのアーカイブにまとめます。

```powershell
# Windows (PowerShell) で ZIP 化する場合の例
Compress-Archive -Path .\mini-aigw, .\tools, .\wheelhouse_*, .\requirements-prod.txt -DestinationPath mini-aigw-offline-bundle.zip
```

```bash
# Linux (Bash) で tar.gz 化する場合の例
tar -czvf mini-aigw-offline-bundle.tar.gz mini-aigw/ tools/ wheelhouse_*/ requirements-prod.txt
```

---

## 3. 【導入フェーズ・Linux編】閉域環境での仮想環境作成とインストール

閉域環境の Linux サーバー（例: Amazon Linux 2023, RHEL, Ubuntu）にログインして作業します。

### Step 3.1: アーカイブの配置と展開
持ち込んだアーカイブをサーバーに配置して展開します。

```bash
# デプロイ先ディレクトリの作成と展開
mkdir -p /opt/mini-aigw-deploy
cd /opt/mini-aigw-deploy
tar -xzvf mini-aigw-offline-bundle.tar.gz

# アプリケーション配置ディレクトリへ移動
cd mini-aigw
```

### Step 3.2: `uv` バイナリの配置と権限設定
持ち込んだ Linux 用 `uv` バイナリを実行可能なパスに配置します。

```bash
# システム全体で利用する場合
sudo cp ../tools/uv /usr/local/bin/
sudo chmod +x /usr/local/bin/uv

# 動作確認
uv --version
```
> ※ sudo 権限がない場合は、作業ディレクトリ配下に配置し `./tools/uv` で実行しても問題ありません。

### Step 3.3: 仮想環境の作成
閉域環境の Python 3.12+ を指定し、仮想環境を作成します。
`--no-python-downloads` を指定することで、外部への Python 自動ダウンロード試行を完全に防止します。

```bash
# システムの python3.12 を使用して仮想環境 (.venv) を作成
uv venv --python /usr/bin/python3.12 --no-python-downloads .venv

# または環境変数を指定する場合:
# export UV_PYTHON_DOWNLOADS=never
# uv venv .venv
```

### Step 3.4: 完全オフラインでのパッケージインストール
持ち込んだ Wheelhouse ディレクトリ（`wheelhouse_linux`）を指定し、外部インデックスへのアクセスを遮断（`--no-index` / `--offline`）してインストールします。

```bash
# 1. 依存ライブラリの一括インストール
uv pip install \
  --offline \
  --no-index \
  --find-links /opt/mini-aigw-deploy/wheelhouse_linux \
  -r /opt/mini-aigw-deploy/requirements-prod.txt

# 2. mini-aigw 自身のインストール（editable または通常インストール）
uv pip install \
  --offline \
  --no-index \
  --find-links /opt/mini-aigw-deploy/wheelhouse_linux \
  --no-deps \
  -e .
```

> **参考（uv バイナリを使用せず標準 pip のみで行う場合）**:
> ```bash
> .venv/bin/python -m pip install \
>   --no-index \
>   --find-links /opt/mini-aigw-deploy/wheelhouse_linux \
>   -r /opt/mini-aigw-deploy/requirements-prod.txt
> 
> .venv/bin/python -m pip install --no-index --no-deps -e .
> ```

### Step 3.5: 環境設定 (`.env`) と DB マイグレーション
本番用環境変数を設定し、Alembic でデータベースを初期化します。

```bash
# 1. 環境変数の作成
cp .env.sample .env
# .env を本番環境（AWS リージョン、Bedrock KB ID、SECRET_KEY 等）に合わせて編集
# ※ EC2 インスタンスプロファイル（IAM ロール）を使用するため、AWS_ACCESS_KEY_ID や AWS_SECRET_ACCESS_KEY の設定は不要です（boto3 が IMDSv2 経由で自動取得）。
vi .env

# 2. SQLite データベース保存ディレクトリの作成
mkdir -p data

# 3. データベースマイグレーションの実行
.venv/bin/alembic upgrade head
```

### Step 3.6: サービス起動確認と systemd 登録

#### 手動起動テスト:
```bash
.venv/bin/uvicorn app.main:app --host 0.0.0.0 --port 8000
```
ブラウザまたは `curl http://localhost:8000/v1/models` で正常応答（200 OK）することを確認します。

#### systemd サービス定義例 (`/etc/systemd/system/mini-aigw.service`):
```ini
[Unit]
Description=mini-aigw (AI Gateway Service)
After=network.target

[Service]
Type=simple
User=ec2-user
Group=ec2-user
WorkingDirectory=/opt/mini-aigw-deploy/mini-aigw
Environment="PATH=/opt/mini-aigw-deploy/mini-aigw/.venv/bin:/usr/local/bin:/usr/bin"
EnvironmentFile=/opt/mini-aigw-deploy/mini-aigw/.env
ExecStart=/opt/mini-aigw-deploy/mini-aigw/.venv/bin/uvicorn app.main:app --host 0.0.0.0 --port 8000 --workers 4
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
# サービスの有効化と起動
sudo systemctl daemon-reload
sudo systemctl enable --now mini-aigw
sudo systemctl status mini-aigw
```

---

## 4. 【導入フェーズ・Windows編】閉域環境での仮想環境作成とインストール

閉域環境の Windows サーバー（Windows Server 2022 等）または Windows PC にログインして作業します。

### Step 4.1: アーカイブの配置と展開
持ち込んだ ZIP ファイルを作業ディレクトリに展開します。

```powershell
# PowerShell (管理者または作業ユーザー)
Expand-Archive -Path C:\deploy\mini-aigw-offline-bundle.zip -DestinationPath C:\opt\mini-aigw-deploy
Set-Location -Path C:\opt\mini-aigw-deploy\mini-aigw
```

### Step 4.2: `uv.exe` バイナリの配置
持ち込んだ `uv.exe` をパスの通った場所（またはプロジェクトルート）に配置します。

```powershell
# プロジェクト直下に配置する場合
Copy-Item ..\tools\uv.exe .\uv.exe

# 動作確認
.\uv.exe --version
```

### Step 4.3: 仮想環境の作成
閉域環境にインストールされている Python 3.12+ を指定して仮想環境を作成します。

```powershell
# Python 自動ダウンロードを無効化して仮想環境を作成
.\uv.exe venv --python "C:\Python\python.exe" --no-python-downloads .venv
```

### Step 4.4: 完全オフラインでのパッケージインストール
持ち込んだ Wheelhouse ディレクトリ（`wheelhouse_windows`）から完全オフラインでインストールします。

```powershell
# 1. 依存ライブラリの一括インストール
.\uv.exe pip install `
  --offline `
  --no-index `
  --find-links C:\opt\mini-aigw-deploy\wheelhouse_windows `
  -r C:\opt\mini-aigw-deploy\requirements-prod.txt

# 2. mini-aigw 自身のインストール
.\uv.exe pip install `
  --offline `
  --no-index `
  --find-links C:\opt\mini-aigw-deploy\wheelhouse_windows `
  --no-deps `
  -e .
```

### Step 4.5: 環境設定 (`.env`) と DB マイグレーション

```powershell
# 1. .env の作成と編集
Copy-Item .env.sample .env
notepad .env

# 2. SQLite データ用ディレクトリの作成
New-Item -ItemType Directory -Force -Path data

# 3. マイグレーションの実行
.\.venv\Scripts\alembic.exe upgrade head
```

### Step 4.6: サービス起動確認

```powershell
# 起動確認
.\.venv\Scripts\uvicorn.exe app.main:app --host 0.0.0.0 --port 8000
```
ブラウザで `http://localhost:8000/v1/models` や Web 画面にアクセスし、CSS/JS（Bootstrap, htmx）を含めオフラインで正常描画されることを確認します。

> **Windows サービス化**:
> 常駐運用する場合は [NSSM (Non-Sucking Service Manager)](https://nssm.cc/) などのツールを用いて `.\.venv\Scripts\uvicorn.exe` を Windows サービスとして登録すると安定稼働します。

---

## 5. 【代替手順】同一OS間での uv キャッシュ転送方式 (Native uv Cache)

オンライン作業端末と閉域サーバーが**同一の OS・アーキテクチャ・Python バージョン**である場合（例: オンライン Linux x86_64 → 閉域 Linux x86_64、または オンライン Win x64 → 閉域 Win x64）は、`uv` のグローバルキャッシュディレクトリをそのまま転送する方式も利用可能です。

### 手順（オンライン端末）:
```bash
# 1. 依存関係のロックとキャッシュの事前生成
uv sync --no-dev

# 2. キャッシュディレクトリのパスを確認
uv cache dir
# (Linux例: ~/.cache/uv, Windows例: %LOCALAPPDATA%\uv\cache)

# 3. キャッシュの不要部分を削除してアーカイブ
uv cache prune
tar -czvf uv-cache.tar.gz -C $(uv cache dir) .
```

### 手順（閉域環境）:
```bash
# 1. キャッシュを展開
mkdir -p /opt/transferred-uv-cache
tar -xzvf uv-cache.tar.gz -C /opt/transferred-uv-cache

# 2. UV_CACHE_DIR と UV_PYTHON_DOWNLOADS を設定してオフライン同期
export UV_CACHE_DIR="/opt/transferred-uv-cache"
export UV_PYTHON_DOWNLOADS=never

uv sync --offline --frozen --no-dev
```
> **注意**: OS やアーキテクチャが異なる環境間（Windows 開発端末から Linux サーバー等）では、キャッシュ内の Wheel バイナリ互換性がないため、本方式ではなく第 2 章・第 3 章の **Wheelhouse 方式** を必ず採用してください。

---

## 6. トラブルシューティング & Tips

### Q1. `No matching distribution found` エラーが発生する
- **原因**: 持ち込んだ Wheel のプラットフォームタグ（`manylinux...` / `win_amd64`）または Python バージョンタグ（`cp312`, `cp313` 等）が、本番環境の Python と一致していません。
- **対処**: 本番環境の Python バージョン（`python3 --version`）を正確に確認し、オンライン側で `--python-version` と `--abi` を合致させて再取得してください。

### Q2. `Failed to build wheel` / C コンパイラ関連のエラーが出る
- **原因**: Wheel (.whl) ではなくソース配布物 (.tar.gz や .zip の sdist) が混入し、閉域環境でビルドを試みた可能性があります。
- **対処**: `pip download` 実行時に必ず `--only-binary=:all:` を付与して、ビルド済み Wheel のみをダウンロードしてください。

### Q3. `uv` が外部に接続しにいってタイムアウトする
- **原因**: `--offline` または `--no-index` が指定されていない、あるいは Python 自動ダウンロードが動作しています。
- **対処**: コマンドライン引数に `--offline --no-index --no-python-downloads` を付与するか、以下の環境変数を設定してください：
  ```bash
  export UV_OFFLINE=1
  export UV_PYTHON_DOWNLOADS=never
  ```

### Q4. Web 画面のレイアウトが崩れる・外部 CDN エラーが出るか？
- **確認**: `mini-aigw` は閉域環境方針（AGENTS.md）に従い、Bootstrap 5.3 および htmx 2.x の静的ファイルを `/static/vendor/` に同梱して配信しています。外部 CDN への通信は一切発生しません。ブラウザの開発者ツール（F12）の「ネットワーク」タブで外部ドメインへのリクエストが発生していないことを確認してください。

### Q5. Bedrock や S3 呼び出し時に接続タイムアウト (`ConnectTimeoutError`) が発生する
- **原因**: 完全閉域 VPC 内において VPC エンドポイント（Interface / Gateway）が存在しない、プライベート DNS が無効化されている、またはセキュリティグループによって通信が遮断されている可能性があります。
- **対処**:
  1. 第 1.4 節に記載された各 VPC エンドポイント（`bedrock-runtime`, `bedrock-agent-runtime`, `bedrock-agent`, `s3`）が VPC 内に作成されているか確認してください。
  2. Interface エンドポイントの「プライベート DNS 名を有効化」および VPC の「DNS 解決」「DNS ホスト名」が有効になっていることを確認してください（無効の場合、AWS パブリック IP を参照しようとしてタイムアウトします）。
  3. EC2 のアウトバウンドセキュリティグループおよびエンドポイント ENI のインバウンドセキュリティグループで、443/TCP (HTTPS) 通信が許可されているか確認してください。

### Q6. Bedrock 呼び出し時に `AccessDeniedException` や `ValidationException` が発生する
- **原因**: EC2 インスタンスに IAM ロールが適切にアタッチされていない、ポリシーの Action / Resource が不足している、または Bedrock 側で対象モデルの利用申請が行われていません。
- **対処**:
  1. 第 1.5 節の IAM ポリシーが EC2 インスタンスのインスタンスプロファイルにアタッチされているか確認してください。
  2. AWS マネジメントコンソールの「Amazon Bedrock」>「Model access」画面を開き、利用したいモデル（Claude 3.5 Sonnet, Nova Lite 等）のステータスが **Access granted** になっているか確認してください。


