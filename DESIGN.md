# システム設計書 (DESIGN.md)

## 1. システム概要とアーキテクチャ

本システムは、閉域AWS環境（EC2）上で稼働し、業務チャットツール（OpenAI互換リクエスト）と基幹LLM/ナレッジ基盤（Amazon Bedrock Converse API / Bedrock Knowledge Bases with S3 Vectors）の中間に位置する**AIミドルウェア（ゲートウェイ）**である。

### 1.1 全体構成図

```
[ 業務チャット ] ──(OpenAI互換 API: Bearer Token)──┐
                                                    ▼
[ ユーザー (ブラウザ) ] ──(Web UI: Session Cookie)───► [ EC2: FastAPI AI Gateway App ]
  ├─ 管理画面 (ナレッジ管理 / RAGテスト)                  │  (静的配信: Bootstrap 5.3 + htmx 2.x)
  └─ mini-id SSO 連携 or ローカルID/Pass                  ├─ markitdown (ファイル自動テキスト化/正規化)
                                                    ├─ SQLite (WAL mode: ユーザー, ナレッジメタ, トークン)
                                                    ├─ Amazon S3 (ドキュメント実体 & .metadata.json)
                                                    ├─ Amazon Bedrock (Converse API / KB Retrieve)
                                                    └─ mini-id (OIDC IdP)
```

### 1.2 技術スタック選定
- **言語/ランタイム**: Python 3.12+
- **Webフレームワーク**: FastAPI (ASGI / Uvicorn)
- **DB/ORM**: SQLite + SQLModel (SQLAlchemy)
  - WALモード（Write-Ahead Logging）により高並行性を確保
- **添付ファイル処理**: `markitdown` (Microsoft)
  - PDF, DOCX, XLSX, PPTX, HTML, 画像等のドキュメントをMarkdownテキストに変換・正規化。モデルがマルチモーダル非対応であってもコンテキストへ注入可能。
- **認証**: Authlib (OIDC/PKCE) + Passlib/Argon2id (ローカル認証)
- **AWS SDK**: `boto3` / `aioboto3` (Bedrock Runtime, Bedrock Agent Runtime, S3)
- **管理画面 (閉域環境・オフライン対応)**:
  - Jinja2 テンプレート
  - **Bootstrap 5.3** (CSS/JS をリポジトリ内 `static/vendor/` に同梱・ローカル配信)
  - **htmx 2.x** (最新安定版 JS をローカル配信し、SPAライクな非同期操作を実現)
  - 外部CDN依存を完全に排除

---

## 2. 認証・認可アーキテクチャ

### 2.1 ハイブリッド認証モデル
ユーザーはログイン画面または環境設定（デフォルト認証方式）に応じて、「mini-id SSO」または「ローカル認証」を選択できる。

```
                    ┌─ [mini-id SSO] ── Authorization Code Flow + PKCE (S256) ── sub照合
[ ログイン画面 ] ──┤
                    └─ [ローカル認証] ── ID / Password (Argon2id) ── local_id照合
```

### 2.2 mini-id 規約準拠ポイント (`rp-integration-guide.md`)
1. **識別子**: mini-id 連携時は不変の `sub`（UUID）を主識別キーとしてユーザーを特定。
2. **フロー**: Authorization Code Flow + PKCE (`S256`)。`state`, `nonce`, `code_verifier` はサーバー側一時セッションで保持・検証。
3. **セッション**: 認可完了時にアプリ独自セッションCookieを発行（`HttpOnly`, `SameSite=Lax`, HTTPS時 `Secure`）。IDトークンは保持し、ログイン成功時にセッションIDを再生成。
4. **権限同期**: IDトークンの `groups` クレームからアプリ内のロール/S3 prefix権限を同期。

### 2.3 業務チャット向け APIキー認証
- チャットボット側には `/v1/*` 呼び出し用の API Key（`sk-live-...`）を発行。
- SHA-256でハッシュ化してSQLiteに保存。
- トークン単位でアクセス可能な「モデル」「グループ（ナレッジ検索権限）」を制御。

---

## 3. OpenAI互換 ⇆ Bedrock Converse API 変換仕様

### 3.1 エンドポイント構造
- `POST /v1/chat/completions`: チャット対話エンドポイント
- リクエストヘッダー: `Authorization: Bearer <API_KEY>`

### 3.2 添付ファイル（Base64）のハンドリングと markitdown 統合
チャットから送信されるBase64ファイルデータは、対象モデルの能力に関わらず安定して処理できるよう `markitdown` を用いてテキスト正規化を行う。

```
[OpenAI互換リクエスト (Base64データ包含)]
                 │
                 ▼
     [添付ファイル判定・デコード]
                 │
      ┌──────────┴──────────┐
      ▼                     ▼
[画像ファイル (PNG/JPEG等)] [ドキュメント (PDF, DOCX, XLSX, CSV, TXT)]
      │                     │
      │                     ▼
      │             [markitdown で Markdownテキストへ変換]
      │                     │
      ▼                     ▼
[モデル種別の判定 (マルチモーダル対応か否か)]
  ├─ マルチモーダル対応モデル:
  │    ├─ 画像: Bedrock Converse API ImageBlock
  │    └─ ドキュメント: Bedrock DocumentBlock または抽出Markdownテキスト
  └─ テキスト専用モデル:
       └─ すべて抽出テキストをプロンプト内（ユーザー発話コンテキスト）に付与
```

### 3.3 ストリーミングレスポンス
- `stream=true` 時は、Bedrock の `converse_stream` を受け取り、OpenAI互換の `data: {"choices": [{"delta": {"content": "..."}}]}\n\n` 形式にチャンクをリアルタイム変換して送信。

---

## 4. ナレッジベース & S3 Vectors メタデータ管理

### 4.1 S3 格納規約とサイドカーメタデータ
Amazon Bedrock Knowledge Bases (S3 Vectors バックエンド) の仕様に基づき、ファイル本体と同一プレフィックスに `.metadata.json` を配置する。

```
s3://<kb-bucket>/
  ├── public/
  │    ├── manual.pdf
  │    └── manual.pdf.metadata.json
  └── groups/
       ├── sales/
       │    ├── proposal.docx
       │    └── proposal.docx.metadata.json
       └── dev/
            ├── spec.md
            └── spec.md.metadata.json
```

**メタデータ JSON 構造例 (`proposal.docx.metadata.json`)**:
```json
{
  "metadataAttributes": {
    "group": "sales",
    "document_id": "doc_102938",
    "filename": "proposal.docx",
    "source_prefix": "groups/sales/"
  }
}
```

### 4.2 SQLite 整合性と削除フロー
ユーザーは S3 に直接アクセスせず、必ずミドルウェアを経由する。

```
[UI/API: ドキュメント削除要求]
       │
       ▼
[SQLite: 対象ステータスを 'DELETING' に更新]
       │
       ├─► [S3: ファイル本体 & .metadata.json を DeleteObject]
       │
       ├─► [Bedrock KB: StartIngestionJob (同期実行)]
       │
       ▼
[同期完了確認後: SQLite レコードを物理削除 (または 'DELETED' 確定)]
```

### 4.3 S3 Vectors / Managed Store プリフィルタリング (Pre-Filtering) とタグ活用
RAG 検索時、ユーザーのグループ・権限に基づき、Bedrock の `Retrieve` API (`managedSearchConfiguration`) に `filter` を設定。
pre-filtering 機構により、ベクトル類似度計算の前に権限外のドキュメントを厳格に除外する。

また、`.metadata.json` の `metadataAttributes`（`tags`, `document_id`, `filename`）は本文 Embedding の対象外であるため、検索クエリ内に登録済みタグやファイル名が検知された場合（または明示的にタグが指定された場合）は、`andAll` と `listContains(tags)` / `in(document_id)` を組み合わせたメタデータフィルタ検索を実行する（※ Managed Store では `stringContains` は非対応）。

```python
retrieval_filter = {
    "andAll": [
        {
            "orAll": [
                {"equals": {"key": "group", "value": "public"}},
                {"equals": {"key": "group", "value": user_primary_group}},
            ]
        },
        {"listContains": {"key": "tags", "value": "統計検定"}},
    ]
}
```

### 4.4 Bedrock Managed KB 特有の検索仕様と対策
1. **自然文クエリの優先と単語単体フォールバック**:
   - Managed KB は助詞を含まない単語単体（例: `統計検定`）で 0 件を返す特性があるため、Tool Use ではユーザーの自然な質問文をそのまま渡し、0 件時は `"{query}について"` で自動再検索する。
2. **Pre-filtering 時のスコア減衰と閾値 (`RAG_SCORE_THRESHOLD=0.30`)**:
   - `filter` 適用時は類似度スコアが `0.30〜0.42` 前後に正規化されるため、デフォルト閾値を `0.30` とする。
3. **PDF 解析設定 (`SMART_PARSING` 非推奨)**:
   - 日本語の細かい表組み PDF に `SMART_PARSING`（OCR）を適用すると文字化けが発生するため、データソースでは標準パーサーを使用する。
   - 詳細は `docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md` を参照。

---

## 5. 管理画面・閉域 UI 設計

### 5.1 フロントエンド構成
- **静的アセット**:
  - `app/static/vendor/bootstrap/` (CSS, JS)
  - `app/static/vendor/htmx/` (htmx.min.js v2.x)
- **UI 機能一覧**:
  1. **ダッシュボード / ログイン**: ローカル認証 / mini-id SSO 切り替え
  2. **ナレッジ管理**: S3ファイル一覧、ドラッグ&ドロップアップロード、登録グループ変更、削除 & KB再同期トリガー
  3. **RAGテスト画面**:
     - 検索単体テスト（Retrieve）: 取得チャンク、スコア、メタデータのリアルタイム一覧表示
     - 対話テスト（Converse + RAG）: 引用元（Citations）付きチャットシミュレータ
  4. **APIキー管理**: 業務チャット用APIキーの発行・失効・権限グループ設定

---

## 6. データベーススキーマ (SQLite)

```sql
-- ユーザー
CREATE TABLE users (
    id TEXT PRIMARY KEY,
    auth_type TEXT NOT NULL,         -- 'oidc' | 'local'
    oidc_sub TEXT UNIQUE,            -- mini-id sub (UUID)
    username TEXT NOT NULL UNIQUE,   -- ログインID または preferred_username
    password_hash TEXT,             -- ローカル認証用ハッシュ (Argon2id)
    email TEXT,
    display_name TEXT,
    roles TEXT NOT NULL,             -- JSON配列 (例: '["sales", "admin"]')
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- APIキートークン
CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT REFERENCES users(id),
    name TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,   -- SHA-256
    prefix TEXT NOT NULL,           -- 表示用 (例: 'sk-live-ab12...')
    allowed_groups TEXT NOT NULL,   -- 検索を許可するS3グループ (JSON配列)
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ナレッジ管理テーブル
CREATE TABLE knowledge_documents (
    id TEXT PRIMARY KEY,
    s3_key TEXT NOT NULL UNIQUE,
    s3_bucket TEXT NOT NULL,
    filename TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    content_type TEXT NOT NULL,
    group_name TEXT NOT NULL,       -- S3 Vectors フィルタ用キー
    uploader_id TEXT REFERENCES users(id),
    sync_status TEXT NOT NULL,      -- 'PENDING' | 'SYNCED' | 'DELETING' | 'ERROR'
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- セッション管理
CREATE TABLE sessions (
    session_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    raw_id_token TEXT,              -- OIDC RP-Initiated Logout用
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

---

## 7. EC2 デプロイメント・運用要件

1. **閉域環境ネットワーク**:
   - VPC エンドポイント（S3, Bedrock Runtime, Bedrock Agent Runtime）の設定。
   - mini-id サーバーへのDNS名前解決および通信経路（443ポート）の確保。
2. **IAM ロール**: EC2 インスタンスプロファイルで Bedrock および S3 操作権限を付与。
3. **SQLite 並行性**: `PRAGMA journal_mode=WAL;` / `PRAGMA busy_timeout=5000;` を接続時に設定。