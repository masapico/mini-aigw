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
- **S3 Vectors プリフィルタリング**:
  - `retrievalFilter` により、所属グループ（`sales`, `dev` 等）および `public` のドキュメントのみを厳格に分離して検索。
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

- **Python**: 3.12+ (Python 3.14 対応確認済み)
- **パッケージマネージャ**: `uv` または `mise`
- **AWS 資格情報**: EC2 インスタンスプロファイル、または `~/.aws/credentials` / 環境変数（Bedrock Runtime, Bedrock Agent Runtime, S3 操作権限）

---

## 3. セットアップ手順

### 3.1 仮想環境の作成と依存関係のインストール

```bash
# リポジトリ直下に移動
cd /path/to/mini-aigw

# 仮想環境作成 (.venv がない場合)
uv venv --python 3.14 .venv
# または mise 経由:
# mise exec python@3.14 -- python -m venv .venv

# 依存関係のインストール
.venv/bin/python -m pip install -e ".[dev]"
# または uv 経由:
# uv pip install -e ".[dev]" --python .venv/bin/python
```

### 3.2 環境変数の設定 (`.env`)

必要に応じてリポジトリルートに `.env` ファイルを作成して設定します。

```env
# アプリケーション設定
APP_NAME=mini-aigw
DEBUG=false
HOST=0.0.0.0
PORT=8000
SECRET_KEY=your-secret-key-change-in-production

# SQLite データベース
DATABASE_URL=sqlite:///data/gateway.db

# AWS 設定
AWS_REGION=us-east-1
# AWS_ENDPOINT_URL=http://localhost:4566  # LocalStack または VPC エンドポイント使用時
BEDROCK_KB_ID=your-bedrock-knowledge-base-id
BEDROCK_DATA_SOURCE_ID=your-bedrock-data-source-id
KNOWLEDGE_S3_BUCKET=your-s3-knowledge-bucket
DEFAULT_MODEL_ID=anthropic.claude-3-5-sonnet-20241022-v2:0

# mini-id (OIDC IdP) 設定（SSO 連携時）
# OIDC_ISSUER=https://mini-id.internal
# OIDC_CLIENT_ID=your-client-id
# OIDC_CLIENT_SECRET=your-client-secret
# OIDC_REDIRECT_URI=http://localhost:8000/auth/oidc/callback
# OIDC_SCOPES="openid profile email groups"
# OIDC_END_SESSION_ENDPOINT=https://mini-id.internal/protocol/openid-connect/logout
```

---

## 4. 起動コマンド

### 4.1 開発サーバーの起動

```bash
.venv/bin/uvicorn app.main:app --host 0.0.0.0 --port 8000 --reload
```

起動後、コンソールに以下の初期化ログが表示されます:
- データベーステーブルの自動作成 (`init_db`)
- 初期管理者アカウントの自動登録（初回起動時のみ）

### 4.2 本番用起動 (マルチワーカー / Gunicorn または Uvicorn)

```bash
.venv/bin/uvicorn app.main:app --host 0.0.0.0 --port 8000 --workers 4
```

---

## 5. Web 管理画面の利用方法

ブラウザで `http://localhost:8000/login` にアクセスします。

### 初期ログインアカウント
- **ユーザー名**: `admin`
- **パスワード**: `admin123`

### 主要画面
1. **ダッシュボード (`/`)**:
   - ナレッジ登録件数、有効 API キー数、Bedrock リージョン、所属グループを表示。
   - OpenAI SDK 接続サンプルコードのクイックコピー。
2. **ナレッジ管理 (`/knowledge`)**:
   - 所属グループ（`public`, `sales`, `dev` 等）を指定してドキュメントをドラッグ＆ドロップまたはファイル選択でアップロード。
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
    model="anthropic.claude-3-5-sonnet-20241022-v2:0",
    messages=[
        {"role": "system", "content": "あなたは優秀な社内アシスタントです。"},
        {"role": "user", "content": "こんにちは！"},
    ],
    temperature=0.7,
)
print(response.choices[0].message.content)

# 2. ストリーミング応答 (SSE)
stream = client.chat.completions.create(
    model="anthropic.claude-3-5-sonnet-20241022-v2:0",
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
    "model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
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
    model="anthropic.claude-3-5-sonnet-20241022-v2:0",
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

## 10. ライセンス

社内利用限定 (Internal Use Only)