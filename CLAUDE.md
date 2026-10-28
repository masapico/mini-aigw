# CLAUDE.md

mini-aigw は OpenAI 互換 API を提供する社内向け AI ゲートウェイ。Go + Pure-Go SQLite (GORM) + AWS SDK for Go v2 (Bedrock / Bedrock KB / S3 Vectors) + mini-id (OIDC) で構成され、Web 管理画面を含め単一バイナリで動作する。閉域環境での運用が前提。

## コマンド

```bash
go run ./cmd/mini-aigw            # サーバー起動（既定 PORT=8000、設定は .env / .env.sample 参照）
go run ./cmd/mini-aigw eval ...   # RAG 評価 CLI（docs/RAG_EVALUATION_GUIDE.md）
go run ./cmd/mini-aigw backup ... # バックアップ

go vet ./...                      # CI と同じ静的チェック
go test ./...                     # 全テスト（CI は go test -v ./...）
go test ./internal/services -run RealSamplePDFs -v   # error_sample/ の実 PDF で抽出検証

go run ./scripts/release --dry-run              # 配布パッケージ生成の確認
go run ./scripts/release -version v1.0.0        # 配布パッケージ生成
```

`cmd/server` と `cmd/eval` は互換用のエントリポイント。新機能は `cmd/mini-aigw` のサブコマンドに追加する。

## コード構成

- `internal/server` — 初期化・ルーティング (`SetupRouter`)
- `internal/api/v1` — OpenAI 互換 API (`/v1/chat/completions`, `/v1/models`)、エラー変換は `chat_errors.go`
- `internal/api/web` — 管理画面ハンドラー (htmx / SSR)、`internal/api/middleware` — 認証・セッション・ロギング
- `internal/services` — Bedrock、Knowledge (S3 / KB)、OIDC、xberg ドキュメント抽出
- `internal/auth` (Argon2id・セッション・PKCE)、`internal/config` (環境変数)、`internal/db` (SQLite)、`internal/models` (GORM エンティティ)
- `internal/logging` — エラーログ (JSON Lines)、`internal/eval` — RAG 評価
- `web/` — `embed.FS` で内包する静的アセット (`static/vendor/` に Bootstrap 5.3 / Bootstrap Icons / htmx 2.x) と `html/template` テンプレート
- `scripts/repro/` — 添付ファイル不具合の再現スクリプト、`docs/` — 仕様・運用・トラブルシューティング

## 必須ルール

### 閉域環境
- IMPORTANT: テンプレートから外部 URL（CDN 等）を参照しない。アセットは必ず `web/static/vendor/` に同梱し `/static/vendor/...` で読み込む。
- UI は Bootstrap 5.3 + Bootstrap Icons + htmx 2.x で実装する。別の UI ライブラリを追加しない。

### 添付ファイル処理 (`internal/services/xberg_service.go`, `bedrock_service.go`)
- 画像は Bedrock `ImageBlock` へ、ドキュメント (PDF/Office/CSV 等) は `DocumentExtractor` (xberg CLI) で Markdown 化してプロンプトへ注入する。xberg 未配置時は UTF-8 平文フォールバックが動く。
- 抽出テキストは改行・タブ以外の制御文字 (`0x00`-`0x1F`, `0x7F`) を除去する。除去しないと Bedrock が `ValidationException` を返す。
- 上限: 1 ファイル `DefaultMaxExtractedLength` (100,000 文字)、1 リクエスト合計 `MAX_TOTAL_ATTACHMENT_CHARS` (既定 50,000、0 以下で無制限)。超過時は警告注記付きで切り詰め、上限到達後の添付は `[Attachment omitted: ...]` のみにする。
- xberg の引数は `extract <file> --content-format markdown --extract-images false --pdf-extract-images false --ocr <XBERG_OCR> --no-config-discovery --log-level error` を維持する（画像抽出は常に無効、OCR は既定 false、タイムアウト `XBERG_TIMEOUT_SEC` 既定 60 秒）。変更時は `scripts/repro/xberg_check.{sh,ps1}` も合わせて更新する。

### エラー処理
- ストリーミング中の Bedrock エラーは無言で切断せず、OpenAI 互換 SSE エラーチャンク (`data: {"error": ...}`) を送ってからクローズする。
- 非ストリームは AWS エラーコードに応じて 400 (ValidationException) / 429 (Throttling 等) / 504 (タイムアウト) / 502 (その他) を OpenAI 互換 JSON で返す。一律 500 にしない。
- チャット処理のエラー・xberg フォールバック・panic は `internal/logging` 経由で `ERROR_LOG_FILE` (既定 `data/logs/error.log`) に記録する。request_id・モデル・添付メタ・AWS エラーコード/RequestID は残す。
- IMPORTANT: base64 本文や抽出テキストはログに出さない。

### Knowledge / S3 Vectors (`internal/services/knowledge_service.go`)
- アップロード時は本体と同じ場所に `<key>.metadata.json` を必ず置き、`metadataAttributes` に `group` / `filename` / `document_id` を入れる（`UploadDocumentWithMetadata`）。`group` は RAG のグループ別プリフィルタに使われるため欠落させない。
- 削除は「ステータスを `DELETING` に更新 → S3 本体と `.metadata.json` を削除 → `StartIngestionJob` で KB 同期 → DB レコード削除」の順を守る（`DeleteKnowledgeDocument`）。

### 認証 (mini-id / OIDC)
- 認可コードフロー + PKCE (`S256`)。`state` / `nonce` はリクエストごとに暗号学的乱数で生成し検証する。
- ユーザーの主キーは `sub` を使う。`username` や `email` をユニークキーにしない。
- セッション Cookie は `HttpOnly` + `SameSite=Lax`、HTTPS 時は `Secure`。ログイン成功時にセッション ID を再生成する。
- ログアウトはアプリセッション破棄後に RP-Initiated Logout へ遷移できるようにする。

### SQLite (`internal/db/db.go`)
- CGO 不使用の `glebarez/sqlite` を使う。CGO が必要なドライバーに替えない（単一バイナリ配布が崩れる）。
- 接続時に `applyPragmas` で WAL / `busy_timeout=5000` / `synchronous=NORMAL` / `foreign_keys=ON` を適用する。プールは `MaxOpenConns=25`, `MaxIdleConns=10`。
- スキーマは起動時の GORM `AutoMigrate` で同期する。新しいモデルを追加したら AutoMigrate の対象にも加える。

## 動作確認

- コード変更後は `go vet ./...` と `go test ./...` を通す。
- API 変更時は `curl` か `openai` SDK (`base_url=http://localhost:8000/v1`) でテキスト・添付付き・ストリーミングの各リクエストを確認する。添付付きは `scripts/repro/send_pdf.{sh,ps1}` が使える。
- テンプレート変更時は、ブラウザで外部ドメインへの通信が発生していないことを確認する。
- RAG 関連の変更時は、あるグループ (例: `sales`) のユーザー/API キーで検索して他グループのドキュメントがチャンクに含まれないことを確認する。
- 実ファイルの再現サンプルは `error_sample/`（git 管理外）に置く。
