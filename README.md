# mini-aigw (AI Gateway)

**mini-aigw** は、閉域AWS環境（EC2）上で稼働し、業務チャットツール（OpenAI互換リクエスト）と基幹LLM/ナレッジ基盤（Amazon Bedrock Converse API / Bedrock Knowledge Bases with S3 Vectors）の中間に位置する**Go製 AIミドルウェア（ゲートウェイ）**です。

CGO フリー（純Go SQLite採用）かつ HTML テンプレート・静的 UI アセット（Bootstrap 5.3 + htmx 2.x）をバイナリ内部に完全内包（Go `embed.FS`）しているため、外部依存のない単一バイナリとして完全閉域環境で動作します。

---

## 1. 主な機能

- **OpenAI 互換 API (`/v1/chat/completions`, `/v1/models`)**:
  - 公式 `openai` SDK、Dify、業務ボット等からそのまま呼び出し可能。
  - テキスト対話および SSE リアルタイムストリーミング (`stream=true`) に対応。
- **添付ファイル処理 (`xberg CLI` 統合 & 平文フォールバック)**:
  - 画像データ（PNG, JPEG 等）は Bedrock `ImageBlock` へ自動マッピング。
  - ドキュメント（PDF, DOCX, XLSX, CSV, TXT 等）は `xberg CLI` により Markdown テキストに自動抽出・正規化してプロンプトへ注入（xberg 未配置環境では安全な UTF-8 平文フォールバックが稼働）。
- **S3 Vectors サイドカーメタデータ管理**:
  - ナレッジアップロード時に同一ディレクトリへ `.metadata.json` サイドカーを同時作成。
  - 削除時は「ステータス更新 (`DELETING`) ➔ S3実体およびメタデータ削除 ➔ Bedrock KB同期実行 ➔ DB物理削除」の整合性保証フロー。
- **Bedrock マネージド Knowledge Base 連携 (`managedSearchConfiguration`)**:
  - Amazon Bedrock の**マネージド Knowledge Base（Managed Vector Store）専用**として設計。
  - 検索クエリには `managedSearchConfiguration` を標準使用し、Pre-filtering によるグループ分離（`public` およびユーザーグループ）を実行。
  - **短文・単語単体クエリ補完 & 自然文クエリ最適化**: Managed KB が単語単体クエリで 0 件を返す仕様に対処するため、Tool Use 時にはユーザーの自然な質問文をそのまま検索に使用し、単語単体で 0 件だった場合は自動的に自然文（`〜について`）へ補完して再検索。
  - **`.metadata.json` タグ・ドキュメントID ハイブリッド検索**: クエリ内に登録ドキュメントのタグ（`tags`）やファイル名が含まれる場合、`.metadata.json` の `listContains(tags)` / `in(document_id)` フィルタ検索を自動統合。
  - ※ 詳細は [Amazon Bedrock Managed Knowledge Base 検索仕様・トラブルシューティングガイド (docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md)](docs/BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md) を参照。
- **S3 Vectors プリフィルタリング**:
  - `retrievalFilter` により、グループ（`sales`, `dev` 等）および `public` のドキュメントのみを厳格に分離して検索。
- **ハイブリッド認証**:
  - **mini-id OIDC**: Authorization Code Flow + PKCE (`S256`)、不変の `sub` (UUID) 主識別子、RP-Initiated Logout 対応。
  - **ローカル認証**: Argon2id による高強度なパスワード認証。
  - セッション固定攻撃対策（ログイン時セッション ID 再生成）、Cookie 属性 (`HttpOnly`, `SameSite=Lax`, HTTPS時 `Secure`)。
- **業務チャット向け API キー管理 & プロンプト/モデル紐付け**:
  - 権限グループを指定した `sk-live-...` トークンの発行・失効（SHA-256 で安全に保存）。
  - **固定モデル指定**: キーごとに特定の Bedrock モデル（Claude5.5Haiku, AmazonNovaLite2 等）を固定化可能（用途制限・コスト統制）。
  - **事前定義システムプロンプト**: キーごとに前提ポリシー・ペルソナプロンプトを設定し、リクエストに基底注入・合成。
- **LLM モデル動的 DB 管理 (`/models`)**:
  - Bedrock 基底モデルの追加、有効/無効の切り替え、既定モデル設定を Web 管理画面から動的に管理。
  - `/v1/models` や APIキー発行画面のモデル候補に即時反映。
  - ※ 利用可能な Bedrock 基底モデル・推論プロファイル ID の取得・登録手順は [Bedrock モデル情報取得・登録ガイド (docs/GET_FOUNDATION_MODELS.md)](docs/GET_FOUNDATION_MODELS.md) を参照。
- **データベース自動マイグレーション & 高並行性 (GORM + SQLite WAL)**:
  - サーバー起動時に GORM の `AutoMigrate` によりテーブル定義を自動初期化・同期。
  - 純Go SQLite ドライバー（`glebarez/sqlite`）による WAL モード強制適用で高並行アクセスに対応。
- **完全閉域・オフライン対応 Web UI**:
  - 外部 CDN への依存を完全排除（Bootstrap 5.3 + Bootstrap Icons + htmx 2.x をバイナリ内に完全内包）。
  - UX に配慮した洗練されたモダンデザイン。
- **リバースプロキシ & サブパス配下運用対応**:
  - `BASE_PATH`（例: `/aigw`）指定や `PUBLIC_BASE_URL` からの自動抽出により、既存 Web サイトのサブパス（`https://sample.com/aigw/`）や専用ドメインでの公開に柔軟に対応。
  - ルートとサブパスのデュアルマウント、API CORS プリフライト、動的 HTTPS Cookie `Secure` 判定を内包（Nginx 設定テンプレート `deploy/nginx/` を同梱）。

---

## 2. 必要要件 (Requirements)

- **Go**: 1.22+（ビルド環境のみ。デプロイ先サーバーには言語ランタイムのインストールは不要です）
- **AWS 資格情報**: EC2 インスタンスプロファイル、または `~/.aws/credentials` / 環境変数（Bedrock Runtime, Bedrock Agent Runtime, S3 操作権限）
- **ドキュメント抽出ツール（任意）**: [xberg CLI](https://github.com/xberg-io/xberg)（PDF/Office等の高度なMarkdown抽出を行う場合に配置。未配置時は自動的にセキュアなUTF-8平文フォールバックが稼働）

---

## 3. ビルドと起動手順

Windows・macOS・Linux のいずれの開発環境でも、外部ツールを一切使わず標準の `go` コマンドだけでビルド・実行・リリースを行えます。

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

以下のコマンド一発で全プラットフォーム（Windows, Linux, macOS 各 amd64/arm64）向けの配布パッケージを一括生成できます。

```bash
go run ./scripts/release -version v1.0.0
```

各パッケージには実行バイナリのほか、`README.md` および `.env.sample` が自動同梱されます。

**生成される成果物 (`dist/` 配下):**
- `mini-aigw_v1.0.0_windows_amd64.zip` (Windows 64-bit x64)
- `mini-aigw_v1.0.0_windows_arm64.zip` (Windows arm64)
- `mini-aigw_v1.0.0_darwin_arm64.tar.gz` (macOS Apple Silicon Mシリーズ)
- `mini-aigw_v1.0.0_darwin_amd64.tar.gz` (macOS Intel)
- `mini-aigw_v1.0.0_linux_amd64.tar.gz` (Linux 64-bit x86_64)
- `mini-aigw_v1.0.0_linux_arm64.tar.gz` (Linux arm64 / AWS Graviton)
- `SHA256SUMS.txt` (全アーカイブの SHA-256 チェックサム一覧)

### 3.3 コンテナでのビルド & 起動 (Docker / Docker Compose)

Alpine ベースのマルチステージビルドにより、イメージサイズ約 35MB の軽量・セキュア（非rootユーザー実行 / ヘルスチェック内包）なコンテナを構築できます。

#### Docker Compose を使用する場合（推奨）
```bash
# ビルド & バックグラウンド起動
docker compose up -d --build

# ログ確認
docker compose logs -f

# 停止
docker compose down
```

#### Docker 単体コマンドを使用する場合
```bash
# イメージのビルド
docker build -t mini-aigw:latest .

# コンテナ起動 (SQLite データを名前付きボリュームで永続化)
docker run -d \
  --name mini-aigw \
  -p 8000:8000 \
  -v mini-aigw-data:/app/data \
  -e AWS_REGION=ap-northeast-1 \
  mini-aigw:latest
```

※ **xberg CLI（高度なドキュメント抽出）をコンテナで利用する場合**:
イメージをスリム（約35MB）に維持するため、Linux 版の `xberg` ディレクトリをホストからボリュームマウントして利用する設計になっています（Alpine ランタイムには glibc 互換レイヤー `gcompat` を同梱済み）。
```bash
# 例: ホスト上の ./xberg-cli を /opt/xberg にマウントして起動
docker run -d \
  --name mini-aigw \
  -p 8000:8000 \
  -v mini-aigw-data:/app/data \
  -v ./xberg-cli:/opt/xberg:ro \
  mini-aigw:latest
```

ヘルスチェックの確認:
```bash
curl http://localhost:8000/health
# {"status":"healthy","database":"ok","version":"dev"}
```

### 3.4 テスト実行

```bash
go test -v ./...
```

---

## 4. 環境変数の設定 (`.env`)

必要に応じてバイナリと同一ディレクトリに `.env` ファイルを作成して設定します（詳細は `.env.sample` を参照）。

```env
# アプリケーション設定
APP_NAME=mini-aigw
DEBUG=false
HOST=0.0.0.0
PORT=8000
SECRET_KEY=your-secret-key-change-in-production

# SQLite データベース (起動時に自動マイグレーション & WAL モード強制適用)
DATABASE_URL=sqlite:///data/aigw.db

# AWS 設定
AWS_REGION=ap-northeast-1
BEDROCK_KB_ID=your-bedrock-knowledge-base-id
BEDROCK_DATA_SOURCE_ID=your-bedrock-data-source-id
KNOWLEDGE_S3_BUCKET=your-s3-knowledge-bucket
DEFAULT_MODEL_ID=jp.amazon.nova-2-lite-v1:0 # 既定モデルID (取得方法は docs/GET_FOUNDATION_MODELS.md 参照)

# ドキュメント抽出 CLI パス (任意: 未指定時は PATH 上の "xberg" を探索、見つからない場合は平文フォールバック)
# XBERG_PATH=/usr/local/bin/xberg

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
   - グループ（`public`, `sales` 等）を指定してドキュメントをドラッグ＆ドロップまたはファイル選択でアップロード。
   - S3 実体および `.metadata.json` が同期され、Bedrock KB Ingestion ジョブがトリガーされます。
   - 不要なドキュメントの安全な削除（S3 実体・メタデータ・DB 連動削除）。
3. **RAG テスト画面 (`/rag-test`)**:
   - **Retrieve 単体テスト**: クエリとグループフィルタを入力し、取得チャンク、スコア（%）、メタデータをリアルタイム確認。
   - **対話シミュレーション**: モデルとグループを指定して質問し、引用元（Citations）付きの回答をシミュレート。
4. **API キー管理 (`/api-keys`)**:
   - 業務チャットボット用にアクセス可能なナレッジグループ、固定LLMモデル、事前定義システムプロンプトを指定して API キー（`sk-live-...`）を発行・失効。
5. **モデル管理 (`/models`)**:
   - Bedrock 基底モデルの新規登録、有効/無効の切り替え、既定モデルの設定、削除をブラウザ上で実行。登録するモデル ID / 推論プロファイル ID の確認方法は [Bedrock モデル情報取得・登録ガイド (docs/GET_FOUNDATION_MODELS.md)](docs/GET_FOUNDATION_MODELS.md) を参照。

---

## 6. OpenAI 互換 API の利用例

### 6.1 Python (`openai` 公式 SDK) によるクライアント呼び出し

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8000/v1",
    api_key="sk-live-...",  # 管理画面で発行した API キー
)

# 1. 通常チャット補完
response = client.chat.completions.create(
    model="jp.amazon.nova-2-lite-v1:0",
    messages=[
        {"role": "system", "content": "あなたは優秀な業務アシスタントです。"},
        {"role": "user", "content": "こんにちは！"},
    ],
    temperature=0.7,
)
print(response.choices[0].message.content)

# 2. ストリーミング応答 (SSE)
stream = client.chat.completions.create(
    model="jp.amazon.nova-2-lite-v1:0",
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
    "model": "jp.amazon.nova-2-lite-v1:0",
    "messages": [
      {"role": "user", "content": "Hello Bedrock!"}
    ],
    "stream": false
  }'
```

### 6.3 添付ファイル（Base64 ドキュメント・画像）の送信

画像（PNG/JPEG等）および各種ドキュメント（PDF, DOCX, XLSX, CSV等）を Base64 文字列として含めることで、自動的にテキスト抽出（`xberg CLI` / 平文抽出）または Bedrock マルチモーダル画像ブロックとして処理されます。

```python
import base64
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8000/v1", api_key="sk-live-...")

with open("spec.pdf", "rb") as f:
    pdf_b64 = base64.b64encode(f.read()).decode("utf-8")

response = client.chat.completions.create(
    model="jp.amazon.nova-2-lite-v1:0",
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

## 7. 完全閉域環境向けデプロイ手順

インターネット通信の一切ない AWS 完全閉域 VPC（エアギャップ環境）への本番デプロイ手順、VPC エンドポイント設計、Linux (systemd) および Windows (NSSM) での常駐サービス設定については、以下のドキュメントを参照してください：

- [閉域・オフライン環境向けデプロイ・運用手順書 (docs/OFFLINE_DEPLOYMENT_GUIDE.md)](docs/OFFLINE_DEPLOYMENT_GUIDE.md)

---

## 8. ディレクトリ構成

```
mini-aigw/
├── cmd/
│   └── server/
│       ├── main.go               # アプリケーションエントリポイント
│       └── server_test.go        # サーバー・ルーティング結合テスト
├── internal/
│   ├── api/
│   │   ├── middleware/           # 認証・セッション・ロギングミドルウェア
│   │   ├── v1/                   # OpenAI互換 API (/v1/chat/completions, /v1/models)
│   │   └── web/                  # Web管理画面ハンドラー (HTMX / SSR)
│   ├── auth/                     # パスワードハッシュ (Argon2id), セッション, PKCE
│   ├── config/                   # 環境変数・設定ロード
│   ├── db/                       # SQLite 初期化 (WAL設定, GORM AutoMigrate)
│   ├── models/                   # GORM エンティティ定義 (User, ApiKey, Knowledge 等)
│   ├── prompts/                  # 前提システムプロンプト定数
│   ├── services/                 # Bedrock, Knowledge (S3), OIDC, Xberg ドキュメント抽出
│   ├── version/                  # バージョン・ビルド情報
│   └── views/                    # テンプレートレンダリングエンジン
├── web/                          # 内包Webアセット (embed.FS 完全内包)
│   ├── embed.go                  # 静的アセット・テンプレート埋め込み定義
│   ├── static/                   # UX配慮モダンデザイン CSS / JS / アイコン (Bootstrap 5.3, htmx 2.x)
│   └── templates/                # html/template テンプレート群
├── scripts/
│   └── release/                  # クロスプラットフォーム配布パッケージ生成スクリプト
├── docs/                         # 仕様書・運用手順書
│   ├── BEDROCK_KB_SPEC_AND_TROUBLESHOOTING.md
│   ├── GET_FOUNDATION_MODELS.md  # Bedrock モデル情報取得・登録ガイド
│   └── OFFLINE_DEPLOYMENT_GUIDE.md
├── go.mod                        # Go モジュール定義
├── go.sum                        # Go 依存関係チェックサム
├── AGENTS.md                     # 開発ガイドライン・実装指示書
├── DESIGN.md                     # システム設計書
└── README.md                     # 本ドキュメント
```
