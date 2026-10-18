# システム設計書 (DESIGN.md)

## 1. システム概要とアーキテクチャ

本システムは、閉域AWS環境（EC2）上で稼働し、業務チャットツール（OpenAI互換リクエスト）と基幹LLM/ナレッジ基盤（Amazon Bedrock Converse API / Bedrock Knowledge Bases with S3 Vectors）の中間に位置する**AIミドルウェア（ゲートウェイ）**である。

### 1.1 全体構成図

```
[ 業務チャット ] ──(OpenAI互換 API: Bearer Token)──┐
                                                    ▼
[ ユーザー (ブラウザ) ] ──(Web UI: Session Cookie)───► [ EC2: Go AI Gateway (mini-aigw) ]
  ├─ 管理画面 (ナレッジ管理 / RAGテスト / APIキー)        │  (完全自己完結内包: Bootstrap 5.3 + htmx 2.x)
  └─ mini-id SSO 連携 or ローカルID/Pass                  ├─ xberg CLI (ドキュメントテキスト化 / UTF-8平文フォールバック)
                                                    ├─ 純Go SQLite (WAL mode + GORM AutoMigrate)
                                                    ├─ Amazon S3 (ドキュメント実体 & .metadata.json)
                                                    ├─ Amazon Bedrock (Converse API / Managed KB Retrieve)
                                                    └─ mini-id (OIDC IdP)
```

### 1.2 技術スタック選定
- **言語/ランタイム**: Go 1.22+（CGO フリー / 単一実行バイナリ）
- **Webフレームワーク/ルーター**: `go-chi/chi/v5`
- **DB/ORM**: Pure-Go SQLite (`github.com/glebarez/sqlite`) + GORM (`gorm.io/gorm`)
  - Cコンパイラ不要の純Goドライバーを採用し、Windows/Linuxクロスコンパイルに対応
  - WALモード（Write-Ahead Logging）およびビジータスクタイムアウトにより高並行性を確保
  - GORM の `AutoMigrate` による起動時スキーマ自動同期
- **添付ファイル処理**: `xberg CLI` 連携 (`internal/services/xberg_service.go`)
  - PDF, DOCX, XLSX, PPTX, HTML, 画像等のドキュメントをMarkdownテキストに変換・正規化。未配置環境では安全なUTF-8平文フォールバックが稼働。
- **認証**:
  - mini-id OIDC: `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` (PKCE S256対応)
  - ローカル認証: `golang.org/x/crypto/argon2` (Argon2id による高強度ハッシュ)
- **AWS SDK**: AWS SDK for Go v2 (`bedrockruntime`, `bedrockagent`, `bedrockagentruntime`, `s3`, `sts`, `config`)
- **管理画面 (完全閉域・オフライン対応)**:
  - Go標準 `html/template`
  - **Bootstrap 5.3** + **Bootstrap Icons** (CSS/JS/フォントを内包配信)
  - **htmx 2.x** (最新安定版 JS を内包配信し、SPAライクな非同期操作を実現)
  - Go標準 `embed.FS`（`web` パッケージ）により、HTML/CSS/JS/画像がすべて単一バイナリに自己完結内包され、外部CDN依存を完全排除

---

## 2. 認証・認可アーキテクチャ

### 2.1 ハイブリッド認証モデル
ユーザーはログイン画面または環境設定に応じて、「mini-id SSO」または「ローカル認証」を選択できる。

```
                    ┌─ [mini-id SSO] ── Authorization Code Flow + PKCE (S256) ── sub照合
[ ログイン画面 ] ──┤
                    └─ [ローカル認証] ── ID / Password (Argon2id) ── username照合
```

### 2.2 mini-id 規約準拠ポイント (`rp-integration-guide.md`)
1. **識別子**: mini-id 連携時は不変の `sub`（UUID）を主識別キーとしてユーザーを特定。
2. **フロー**: Authorization Code Flow + PKCE (`S256`)。`state`, `nonce`, `code_verifier` はサーバー側セッションで保持・検証。
3. **セッション**: 認可完了時にアプリ独自セッションCookieを発行（`HttpOnly`, `SameSite=Lax`, HTTPS時 `Secure`）。ログイン成功時にセッションIDを再生成（セッション固定攻撃対策）。
4. **ログアウト**: アプリセッション破棄後に mini-id の RP-Initiated Logout エンドポイントへ遷移可能。

### 2.3 業務チャット向け APIキー認証
- チャットボット側には `/v1/*` 呼び出し用の API Key（`sk-live-...`）を発行。
- SHA-256でハッシュ化してSQLiteに保存（DB漏洩時にも平文キーを保護）。
- トークン単位でアクセス可能な「モデル」「許可ナレッジグループ」「事前定義システムプロンプト」を制御。

---

## 3. OpenAI互換 ⇆ Bedrock Converse API 変換仕様

### 3.1 エンドポイント構造
- `POST /v1/chat/completions`: チャット対話エンドポイント
- `GET /v1/models`: 利用可能モデル一覧エンドポイント
- リクエストヘッダー: `Authorization: Bearer <API_KEY>`

### 3.2 添付ファイル（Base64）のハンドリングと xberg 統合
チャットから送信されるBase64ファイルデータは、対象モデルの能力に関わらず安定して処理できるよう `xberg CLI` を用いてテキスト正規化を行う。

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
       │             [xberg CLI で Markdownテキストへ変換]
       │             (未配置時は UTF-8 平文フォールバック)
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
- `stream=true` 時は、Bedrock の `ConverseStream` を受け取り、OpenAI互換の `data: {"choices": [{"delta": {"content": "..."}}]}\n\n` 形式にチャンクをリアルタイム変換（SSE: Server-Sent Events）して送信。

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
[同期完了確認後: SQLite レコードを物理削除]
```

### 4.3 S3 Vectors / Managed Store プリフィルタリング (Pre-Filtering) とタグ活用
RAG 検索時、ユーザーのグループ・権限に基づき、Bedrock の `Retrieve` API (`managedSearchConfiguration`) に `filter` を設定。
pre-filtering 機構により、ベクトル類似度計算の前に権限外のドキュメントを厳格に除外する。

また、`.metadata.json` の `metadataAttributes`（`tags`, `document_id`, `filename`）は本文 Embedding の対象外であるため、検索クエリ内に登録済みタグやファイル名が検知された場合（または明示的にタグが指定された場合）は、`andAll` と `listContains(tags)` / `in(document_id)` を組み合わせたメタデータフィルタ検索を自動実行する。

### 4.4 Bedrock Managed KB 特有の検索仕様と対策
1. **自然文クエリの優先と単語単体フォールバック**:
   - Managed KB は助詞を含まない単語単体（例: `統計検定`）で 0 件を返す特性があるため、Tool Use ではユーザーの自然な質問文をそのまま渡し、0 件時は `BuildFallbackNaturalQuery` により `"{query}について"` で自動再検索する。
2. **Pre-filtering 時のスコア減衰と閾値 (`RAG_SCORE_THRESHOLD=0.30`)**:
   - `filter` 適用時は類似度スコアが `0.30〜0.42` 前後に正規化されるため、デフォルト閾値を `0.30` とする。
3. **PDF 解析設定 (`SMART_PARSING` 非推奨)**:
   - 日本語の細かい表組み PDF に `SMART_PARSING`（OCR）を適用すると文字化けが発生するため、データソースでは標準テキストパーサーを使用する。詳細は `docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md` を参照。

---

## 5. 管理画面・閉域 UI 設計

### 5.1 フロントエンド構成
- **静的アセット**: `web/static/vendor/`（Bootstrap 5.3, Bootstrap Icons, htmx 2.x）
- **テンプレート**: `web/templates/`（Go `html/template`）
- **UI 機能一覧**:
  1. **ダッシュボード (`/`)**: 統計情報、リージョン、SDK接続サンプル
  2. **ログイン (`/login`)**: ローカル認証 / mini-id SSO 切り替え
  3. **ナレッジ管理 (`/knowledge`)**: S3ファイル一覧、ドラッグ&ドロップアップロード、登録グループ設定、削除 & KB再同期トリガー
  4. **RAGテスト画面 (`/rag-test`)**:
     - 検索単体テスト（Retrieve）: 取得チャンク、スコア、メタデータのリアルタイム一覧表示
     - 対話テスト（Converse + RAG）: 引用元（Citations）付きチャットシミュレータ
  5. **APIキー管理 (`/api-keys`)**: 業務チャット用APIキーの発行・失効・権限グループ・固定モデル・事前定義プロンプト設定
  6. **モデル管理 (`/models`)**: Bedrock 基底モデル・推論プロファイル ID の動的登録・有効無効切り替え・既定モデル設定（取得・登録手順は [docs/GET_FOUNDATION_MODELS.md](docs/GET_FOUNDATION_MODELS.md) を参照）

---

## 6. データベーススキーマ (SQLite / GORM)

```sql
-- ユーザー (models.User)
CREATE TABLE users (
    id TEXT PRIMARY KEY,
    auth_type TEXT NOT NULL,         -- 'oidc' | 'local'
    oidc_sub TEXT UNIQUE,            -- mini-id sub (UUID)
    username TEXT NOT NULL UNIQUE,   -- ログインID
    password_hash TEXT,             -- ローカル認証用ハッシュ (Argon2id)
    email TEXT,
    display_name TEXT,
    roles TEXT NOT NULL,             -- カンマ区切りロール (例: 'admin,sales')
    groups TEXT NOT NULL,            -- カンマ区切り所属グループ (例: 'sales,dev')
    created_at DATETIME,
    updated_at DATETIME
);

-- APIキートークン (models.ApiKey)
CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT,
    name TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,   -- SHA-256
    prefix TEXT NOT NULL,           -- 表示用 (例: 'sk-live-ab12...')
    allowed_groups TEXT NOT NULL,   -- 検索を許可するS3グループ (カンマ区切り)
    model_id TEXT,                  -- 固定モデルID (任意)
    system_prompt TEXT,             -- 事前定義システムプロンプト (任意)
    created_at DATETIME
);

-- ナレッジ管理テーブル (models.KnowledgeDocument)
CREATE TABLE knowledge_documents (
    id TEXT PRIMARY KEY,
    s3_key TEXT NOT NULL UNIQUE,
    s3_bucket TEXT NOT NULL,
    filename TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    content_type TEXT NOT NULL,
    group_name TEXT NOT NULL,       -- S3 Vectors フィルタ用キー
    tags TEXT,                      -- カンマ区切りタグ
    uploader_id TEXT,
    sync_status TEXT NOT NULL,      -- 'PENDING' | 'SYNCED' | 'DELETING' | 'ERROR'
    data_source_id TEXT,
    created_at DATETIME,
    updated_at DATETIME
);

-- セッション管理 (models.Session)
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    raw_id_token TEXT,              -- OIDC RP-Initiated Logout用
    expires_at DATETIME NOT NULL,
    created_at DATETIME
);

-- LLM基盤モデル管理 (models.LLMModel)
CREATE TABLE llm_models (
    id TEXT PRIMARY KEY,            -- Bedrock profileId または modelId (例: 'apac.anthropic.claude-3-5-sonnet-20241022-v2:0')
    name TEXT NOT NULL,             -- 表示名 (例: 'Claude 3.5 Sonnet v2')
    description TEXT,               -- 説明・用途メモ
    provider TEXT NOT NULL,         -- 'bedrock'
    is_active INTEGER NOT NULL,     -- 有効フラグ (0 or 1)
    is_default INTEGER NOT NULL,    -- 既定モデルフラグ (0 or 1)
    created_at DATETIME
);
```

---

## 7. EC2 デプロイメント・運用要件

1. **閉域環境ネットワーク**:
   - VPC エンドポイント（S3, Bedrock Runtime, Bedrock Agent Runtime, Bedrock Agent, SSM）の設定。
   - mini-id サーバーへのDNS名前解決および通信経路（443ポート）の確保。
2. **単一バイナリによる配布**:
   - クロスコンパイルスクリプト（`scripts/release`）により、ランタイム・依存関係不要のアーカイブを一括生成。
3. **IAM ロール**: EC2 インスタンスプロファイルで Bedrock および S3 操作権限を付与（静的キー管理不要）。
4. **SQLite 並行性**: `PRAGMA journal_mode=WAL;` / `PRAGMA busy_timeout=5000;` を接続時に設定。

---

## 8. 仕様書・関連技術ドキュメント

- [README.md - mini-aigw 全体仕様・起動手順](README.md)
- [docs/GET_FOUNDATION_MODELS.md - Bedrock モデル情報取得・登録ガイド](docs/GET_FOUNDATION_MODELS.md)
- [docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md - Bedrock Managed Knowledge Base 検索仕様・トラブルシューティング](docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md)
- [docs/OFFLINE_DEPLOYMENT_GUIDE.md - 閉域・オフライン環境向けデプロイ・運用手順書](docs/OFFLINE_DEPLOYMENT_GUIDE.md)