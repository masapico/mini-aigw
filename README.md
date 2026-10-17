# mini-aigw (AI Gateway)

**mini-aigw** は、閉域AWS環境（EC2）上で稼働し、業務チャットツール（OpenAI互換リクエスト）と基幹LLM/ナレッジ基盤（Amazon Bedrock Converse API / Bedrock Knowledge Bases with S3 Vectors）の中間に位置する**AIミドルウェア（ゲートウェイ）**です。

---

## 1. 主な機能

- **OpenAI 互換 API (`/v1/chat/completions`, `/v1/models`)**:
  - 公式 `openai` SDK、Dify、業務ボット等からそのまま呼び出し可能。
  - テキスト対話および SSE リアルタイムストリーミング (`stream=true`) に対応。
- **添付ファイル処理 (`markitdown` 統合)**:
  - 画像データ（PNG, JPEG 等）は Bedrock `ImageBlock` へ自動マッピング。
  - ドキュメント（PDF, DOCX, XLSX, CSV, TXT 等）は `markitdown` で Markdown テキストに自動抽出・正規化してプロンプトへ注入。
- **S3 Vectors サイドカーメタデータ管理**:
  - ナレッジアップロード時に同一ディレクトリへ `.metadata.json` サイドカーを同時作成。
  - 削除時は「ステータス更新 (`DELETING`) ➔ S3実体およびメタデータ削除 ➔ Bedrock KB同期実行 ➔ DB物理削除」の整合性保証フロー。
- **Bedrock マネージド Knowledge Base 連携 (`managedSearchConfiguration`)**:
  - 本システムは Bedrock プロキシとしてシンプルな構成を維持するため、Amazon Bedrock の**マネージド Knowledge Base（Managed Vector Store）専用**として設計されています。
  - 検索クエリには `managedSearchConfiguration` を標準使用し、Pre-filtering によるグループ分離（`public` およびユーザーグループ）を実行します。
  - **短文・単語単体クエリ補完 & 自然文クエリ最適化**: Managed KB が単語単体クエリで 0 件を返す仕様に対処するため、Tool Use 時にはユーザーの自然な質問文をそのまま検索に使用し、単語単体で 0 件だった場合は自動的に自然文（`〜について`）へ補完して再検索します。
  - **`.metadata.json` タグ・ドキュメントID ハイブリッド検索**: クエリ内に登録ドキュメントのタグ（`tags`）やファイル名が含まれる場合、`.metadata.json` の `listContains(tags)` / `in(document_id)` フィルタ検索を自動統合し、本文にタグ語句が含まれない資料も確実にヒットさせます。
  - ※ Bedrock Managed KB の詳細な検索仕様・スコア減衰特性・`SMART_PARSING`（OCR）の注意点については、[Amazon Bedrock Managed Knowledge Base 検索仕様・トラブルシューティングガイド (docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md)](docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md) を参照してください。
- **S3 Vectors プリフィルタリング**:
  - `retrievalFilter` により、グループ（`sales`, `dev` 等）および `public` のドキュメントのみを厳格に分離して検索。

- **ハイブリッド認証**:
  - **mini-id OIDC**: Authorization Code Flow + PKCE (`S256`)、不変の `sub` (UUID) 主識別子、RP-Initiated Logout 対応。
  - **ローカル認証**: Argon2id によるセキュアなパスワード認証。
  - セッション固定攻撃対策（ログイン時セッション ID 再生成）、Cookie 属性 (`HttpOnly`, `SameSite=Lax`, HTTPS時 `Secure`)。
- **業務チャット向け API キー管理 & プロンプト/モデル紐付け**:
  - 権限グループを指定した `sk-live-...` トークンの発行・失効（SHA-256 で安全に保存）。
  - **固定モデル指定**: キーごとに特定の Bedrock モデル（Claude 3.5, Nova Lite 等）を固定化可能（用途制限・コスト統制）。
  - **事前定義システムプロンプト**: キーごとに前提ポリシー・ペルソナプロンプトを設定し、リクエストに基底注入・合成。
- **LLM モデル動的 DB 管理 (`/models`)**:
  - Bedrock 基底モデルの追加、有効/無効の切り替え、既定モデル設定を Web 管理画面から動的に管理。
  - `/v1/models` や APIキー発行画面のモデル候補に即時反映。
- **データベースマイグレーション (Alembic)**:
  - SQLModel に対応した Alembic によるバージョン管理されたマイグレーションファイル機構。
- **完全閉域・オフライン対応 Web UI**:
  - 外部 CDN への依存を完全排除（Bootstrap 5.3 + htmx 2.x をローカル同梱・配信）。
  - UX に配慮した洗練されたモダンデザイン。

---

## 2. 必要要件 (Requirements)

### Go 版（推奨：単一バイナリ・閉域Windows/Linuxデプロイ）
- **Go**: 1.22+（ビルド環境のみ。デプロイ先サーバーにはランタイムのインストールは不要）
- **AWS 資格情報**: EC2 インスタンスプロファイル、または `~/.aws/credentials` / 環境変数（Bedrock Runtime, Bedrock Agent Runtime, S3 操作権限）
- **ドキュメント抽出ツール（任意）**: [xberg CLI](https://github.com/xberg-io/xberg)（PDF/Office等の高度なMarkdown抽出を行う場合に配置。未配置時は自動的にセキュアなUTF-8平文フォールバックが稼働）

### Python 版（リファレンス実装）
- **Python**: 3.12+ (Python 3.14 対応確認済み)
- **パッケージマネージャ**: `uv` または `mise`

---

## 3. Go 単一バイナリのビルドと起動手順（推奨）

mini-aigw は CGO フリー（純Go SQLite採用）かつ HTML テンプレート・静的 UI アセット（Bootstrap 5.3 + HTMX 2.x）をバイナリ内部に完全内包（Go `embed.FS`）しているため、外部依存なしの単一バイナリとして動作します。
Windows・macOS・Linux のいずれの開発環境でも、`make` などの外部ツールを一切使わず標準の `go` コマンドだけでビルド・実行・リリースを行えます。

### 3.1 ローカル環境でのビルド & 実行

#### macOS / Linux
```bash
# ビルド (bin/mini-aigw が生成されます)
go build -o bin/mini-aigw ./cmd/server

# 実行
./bin/mini-aigw

# またはビルドを挟まず直接実行:
go run ./cmd/server
```

#### Windows (PowerShell / コマンドプロンプト)
```powershell
# ビルド (bin/mini-aigw.exe が生成されます)
go build -o bin/mini-aigw.exe ./cmd/server

# 実行
.\bin\mini-aigw.exe

# またはビルドを挟まず直接実行:
go run ./cmd/server
```

※ バージョン情報の確認:
```bash
./bin/mini-aigw -v
# または Windows: .\bin\mini-aigw.exe -v
```

### 3.2 マルチプラットフォーム向けリリースビルド (GitHub Releases 形式)

Windows / Linux / macOS を問わず、以下のコマンド一発で全プラットフォーム（Windows, Linux, macOS 各 amd64/arm64）向けの配布パッケージを一括生成できます。

```bash
go run ./script/release -version v1.0.0
```

各パッケージには実行バイナリのほか、`README.md` および `.env.sample` が自動同梱されます。そのまま GitHub Releases にアップロードして公開・配布できます。

**生成される成果物 (`dist/` 配下):**
- `mini-aigw_v1.0.0_windows_amd64.zip` (Windows 64-bit x64)
- `mini-aigw_v1.0.0_windows_arm64.zip` (Windows arm64)
- `mini-aigw_v1.0.0_darwin_arm64.tar.gz` (macOS Apple Silicon Mシリーズ)
- `mini-aigw_v1.0.0_darwin_amd64.tar.gz` (macOS Intel)
- `mini-aigw_v1.0.0_linux_amd64.tar.gz` (Linux 64-bit x86_64)
- `mini-aigw_v1.0.0_linux_arm64.tar.gz` (Linux arm64 / AWS Graviton)
- `SHA256SUMS.txt` (全アーカイブの SHA-256 チェックサム一覧)

**主要オプション:**
- `-version <str>`: リリースバージョン指定（未指定時は git タグまたは `v0.2.0-dev`）
- `-os <list>`: 対象 OS の絞り込み（例: `-os windows,linux`）
- `-arch <list>`: 対象アーキテクチャの絞り込み（例: `-arch amd64`）
- `-dist <dir>`: 出力先ディレクトリ（デフォルト: `dist`）
- `-dry-run`: 実際にはビルドせず、計画される成果物一覧をプレビュー表示

### 3.3 テスト実行

```bash
go test -v ./...
```

---

## 4. 環境変数の設定 (`.env`)

必要に応じてリポジトリルートまたはバイナリと同一ディレクトリに `.env` ファイルを作成して設定します。

```env
# アプリケーション設定
APP_NAME=mini-aigw
DEBUG=false
HOST=0.0.0.0
PORT=8000
# PUBLIC_BASE_URL=https://aigw.internal:8000  # リバースプロキシやカスタムホスト使用時の原本DLリンク基底URL (未設定時はリクエスト元のbase_urlを自動使用)
SECRET_KEY=your-secret-key-change-in-production

# SQLite データベース (自動マイグレーション & WAL モード強制適用)
DATABASE_URL=sqlite:///data/aigw.db

# ドキュメント抽出 CLI パス (任意: 未指定時は PATH 上の "xberg" を探索、見つからない場合は平文フォールバック)
# XBERG_PATH=/usr/local/bin/xberg
# (Windows の場合: XBERG_PATH=C:\tools\xberg.exe)

# AWS 設定
AWS_REGION=ap-northeast-1
# AWS_ENDPOINT_URL=http://localhost:4566  # LocalStack または VPC エンドポイント使用時
BEDROCK_KB_ID=your-bedrock-knowledge-base-id
BEDROCK_DATA_SOURCE_ID=your-bedrock-data-source-id
KNOWLEDGE_S3_BUCKET=your-s3-knowledge-bucket
DEFAULT_MODEL_ID=openai.gpt-oss-20b-1:0

# mini-id (OIDC IdP) 設定（SSO 連携時）
# OIDC_ISSUER=https://mini-id.internal
# OIDC_CLIENT_ID=your-client-id
# OIDC_CLIENT_SECRET=your-client-secret
# OIDC_REDIRECT_URI=http://localhost:8000/auth/oidc/callback
# OIDC_SCOPES="openid profile email groups"
# OIDC_END_SESSION_ENDPOINT=https://mini-id.internal/protocol/openid-connect/logout
```

---

## 5. Web 管理画面の利用方法

ブラウザで `http://localhost:8000/login` にアクセスします。

### 初期ログインアカウント（初回自動シード）
- **ユーザー名**: `admin`
- **パスワード**: `Password1234`

### 主要画面
1. **ダッシュボード (`/`)**:
   - ナレッジ登録件数、有効 API キー数、Bedrock リージョン、グループを表示。
   - OpenAI SDK 接続サンプルコードのクイックコピー。
2. **ナレッジ管理 (`/knowledge`)**:
   - グループ（`public`, `grp000` 等）を指定してドキュメントをドラッグ＆ドロップまたはファイル選択でアップロード。
   - S3 実体および `.metadata.json` が同期され、Bedrock KB Ingestion ジョブがトリガーされます。
   - 不要なドキュメントの安全な削除（S3 実体・メタデータ・DB 連動削除）。
3. **RAG テスト画面 (`/rag-test`)**:
   - **Retrieve 単体テスト**: クエリとグループフィルタを入力し、取得チャンク、スコア（%）、メタデータをリアルタイム確認。
   - **対話シミュレーション**: モデルとグループを指定して質問し、引用元（Citations）付きの回答をシミュレート。
4. **API キー管理 (`/api-keys`)**:
   - 業務チャットボット用にアクセス可能なナレッジグループ、固定LLMモデル、事前定義システムプロンプトを指定して API キー（`sk-live-...`）を発行・失効。
   - シークレットキーは発行時に一度だけ画面上に表示され、コピーできます。
5. **モデル管理 (`/models`)**:
   - Bedrock 基底モデルの新規登録、有効/無効の切り替え、既定モデルの設定、削除をブラウザ上で実行。

---

## 6. データベースマイグレーション (Alembic)

本システムでは、SQLModel に対応した Alembic により、データベーススキーマのバージョン管理と安全な変更適用を行っています。

### 6.1 マイグレーションの実行 (スキーマ適用)

最新のリビジョンまでデータベースを更新します:

```bash
.venv/bin/alembic upgrade head
```

現在のリビジョンを確認する場合:

```bash
.venv/bin/alembic current
```

### 6.2 新規マイグレーションファイルの作成

SQLModel のモデル定義（`app/models/*.py`）を変更した後、以下のコマンドで差分マイグレーションファイルを自動生成します:

```bash
.venv/bin/alembic revision --autogenerate -m "変更内容の説明 (例: add_column_foo)"
```

生成されたマイグレーションファイル（`alembic/versions/xxxx_変更内容の説明.py`）の内容を確認し、必要に応じて微調整の上、`alembic upgrade head` で適用します。

### 6.3 マイグレーション履歴の確認

```bash
.venv/bin/alembic history --verbose
```

---

## 7. OpenAI 互換 API の利用例

### 6.1 Python (`openai` 公式 SDK)

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8000/v1",
    api_key="sk-live-...",  # 管理画面で発行した API キー
)

# 1. 通常チャット補完
response = client.chat.completions.create(
    model="openai.gpt-oss-20b-1:0",
    messages=[
        {"role": "system", "content": "あなたは優秀な業務アシスタントです。"},
        {"role": "user", "content": "こんにちは！"},
    ],
    temperature=0.7,
)
print(response.choices[0].message.content)

# 2. ストリーミング応答 (SSE)
stream = client.chat.completions.create(
    model="openai.gpt-oss-20b-1:0",
    messages=[
        {"role": "user", "content": "AI Gateway のアーキテクチャについて簡潔に説明してください。"},
    ],
    stream=True,
)
for chunk in stream:
    content = chunk.choices[0].delta.content or ""
    print(content, end="", flush=True)
print()
```

### 6.2 `curl` コマンドによる呼び出し

```bash
curl -X POST http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-live-..." \
  -d '{
    "model": "openai.gpt-oss-20b-1:0",
    "messages": [
      {"role": "user", "content": "Hello Bedrock!"}
    ],
    "stream": false
  }'
```

### 6.3 添付ファイル（Base64 ドキュメント・画像）の送信

画像（PNG/JPEG等）および各種ドキュメント（PDF, DOCX, XLSX, CSV等）を Base64 文字列として含めることで、自動的にテキスト抽出（`markitdown`）または Bedrock マルチモーダル画像ブロックとして処理されます。

```python
import base64
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8000/v1", api_key="sk-live-...")

with open("spec.pdf", "rb") as f:
    pdf_b64 = base64.b64encode(f.read()).decode("utf-8")

response = client.chat.completions.create(
    model="openai.gpt-oss-20b-1:0",
    messages=[
        {
            "role": "user",
            "content": [
                {"type": "text", "text": "この仕様書の要点を要約してください。"},
                {"type": "file", "name": "spec.pdf", "data": pdf_b64},
            ],
        }
    ],
)
print(response.choices[0].message.content)
```

---

## 8. テストの実行

全テストスイート（OpenAI SDK互換、markitdown、S3メタデータ整合性、グループPre-filtering、PKCE/セキュリティ、閉域アセット検証等）を実行します。

```bash
.venv/bin/pytest -v
```

---

## 9. ディレクトリ構成

```
mini-aigw/
├── alembic/                      # データベースマイグレーション
│   ├── versions/                 # 各リビジョンマイグレーションファイル
│   └── env.py                    # Alembic 実行設定 (SQLModel 連動)
├── app/
│   ├── api/
│   │   ├── v1/
│   │   │   ├── chat.py           # OpenAI互換 /v1/chat/completions
│   │   │   └── models.py         # /v1/models (DB動的取得)
│   │   └── web/
│   │       ├── api_keys.py       # APIキー発行/失効/一覧
│   │       ├── auth.py           # mini-id OIDC & ローカル認証
│   │       ├── dashboard.py      # システムダッシュボード
│   │       ├── knowledge.py      # ナレッジアップロード/削除/一覧
│   │       ├── models.py         # LLMモデル管理画面・HTMX
│   │       └── rag_test.py       # RAGテスト画面・HTMXエンドポイント
│   ├── core/
│   │   ├── config.py             # 環境変数・設定 (pydantic-settings)
│   │   ├── database.py           # SQLite (WAL設定付きエンジン)
│   │   └── security.py           # パスワードハッシュ(Argon2id), セッション, PKCE
│   ├── models/                   # SQLModel 定義 (User, Group, ApiKey, KnowledgeDocument, LlmModel, Session)
│   ├── services/
│   │   ├── bedrock_service.py    # Converse API & ConverseStream, Retrieve (S3 Vectors)
│   │   ├── knowledge_service.py  # S3 Vectors & .metadata.json サイドカー, Ingestion
│   │   ├── markitdown_service.py # markitdown によるファイルテキスト抽出・正規化
│   │   └── oidc_service.py       # mini-id OIDCクライアント (Authlib PKCE)
│   ├── static/
│   │   ├── css/app.css           # UX配慮のモダンデザイン CSS
│   │   └── vendor/               # 完全閉域ローカル静的配信 (Bootstrap 5.3, htmx 2.x)
│   ├── templates/                # Jinja2 テンプレート (完全ローカルアセット参照)
│   └── main.py                   # FastAPI アプリケーションエントリポイント
├── tests/                        # 自動テストスイート
├── alembic.ini                   # Alembic 設定
├── pyproject.toml
├── AGENTS.md                     # 開発ガイドライン・実装指示書
├── DESIGN.md                     # システム設計書
└── README.md                     # 本ドキュメント
```

---
