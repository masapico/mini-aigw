# CLAUDE.md

本ドキュメントは、AI実装エージェントおよび開発者が本システム（Go + Pure-Go SQLite + AWS SDK for Go v2 + Bedrock + mini-id + S3 Vectors）を実装・保守するための指示書である。

---

## 1. プロジェクト構造と環境方針

### 1.1 ディレクトリ構成
```
mini-aigw/
├── cmd/
│   ├── mini-aigw/
│   │   └── main.go               # 統合CLI・サーバーエントリポイント
│   ├── server/
│   │   └── main.go               # サーバー起動エントリポイント (互換)
│   └── eval/
│       └── main.go               # 評価CLIエントリポイント (互換)
├── internal/
│   ├── server/                   # サーバー初期化・ルーティング (SetupRouter)
│   ├── api/
│   │   ├── middleware/           # 認証・セッション・ロギングミドルウェア
│   │   ├── v1/                   # OpenAI互換 API (/v1/chat/completions, /v1/models)
│   │   └── web/                  # Web管理画面ハンドラー (HTMX / SSR)
│   ├── auth/                     # パスワードハッシュ (Argon2id), セッション, PKCE
│   ├── config/                   # 環境変数・設定ロード (godotenv)
│   ├── db/                       # SQLite 初期化 (WAL設定, GORM AutoMigrate)
│   ├── models/                   # GORM エンティティ定義 (User, ApiKey, Knowledge 等)
│   ├── prompts/                  # 前提システムプロンプト定数
│   ├── services/                 # Bedrock, Knowledge (S3), OIDC, Xberg ドキュメント抽出
│   ├── version/                  # バージョン・ビルド情報
│   └── views/                    # テンプレートレンダリングエンジン (html/template)
├── web/                          # 内包Webアセット (embed.FS 完全内包)
│   ├── embed.go                  # 静的アセット・テンプレートの埋め込み定義
│   ├── static/                   # UX配慮モダンデザイン CSS / JS / アイコン
│   │   └── vendor/               # Bootstrap 5.3, Bootstrap Icons, htmx 2.x
│   └── templates/                # html/template テンプレート群
├── scripts/
│   └── release/                  # マルチプラットフォーム向け配布パッケージ生成スクリプト
├── docs/                         # 仕様書・モデル取得・トラブルシューティングガイド
├── go.mod
├── go.sum
└── README.md
```

### 1.2 閉域環境・依存ライブラリ方針
- **外部CDNへの依存禁止**: HTMLテンプレート内で `cdn.jsdelivr.net` 等の外部URLを参照してはならない。必ず `web` に同梱された `/static/vendor/...` のローカルファイルを指定する。
- **UIフレームワーク**:
  - CSS/UI: **Bootstrap 5.3**, **Bootstrap Icons**
  - 動的インタラクション: **htmx 2.x**（静的JS配置）
  - Go標準の `embed.FS` により、全アセットが単一バイナリに自己完結内包される。
- **ファイル解析**: `xberg CLI` を用いてドキュメント（PDF, Office文書, CSV等）をMarkdownテキスト化する（未配置時は自動的にセキュアなUTF-8平文フォールバックが稼働）。

---

## 2. 実装詳細とコーディング規約

### 2.1 添付ファイル処理（`XbergService` 連携）
OpenAI互換リクエストで届いたBase64添付ファイルは、モデルの種類（マルチモーダル対応・非対応）に関わらず安全に扱えるよう処理する。

```go
// internal/services/xberg_service.go
type DocumentExtractor interface {
    ExtractContentFromBytes(ctx context.Context, fileBytes []byte, filename string) string
    ExtractContentFromBase64(ctx context.Context, b64String string, filename string) string
}
```
- 画像ファイル（PNG, JPEG等）: Bedrock `ImageBlock` へ自動マッピング。
- ドキュメント（PDF, DOCX, XLSX, CSV等）: `XbergService` により Markdown テキストに自動抽出・正規化してプロンプトまたは `DocumentBlock` へ注入。
  - **サニタイズ**: ヌルバイト（`\x00`）および改行・タブ以外の制御文字（`0x00`-`0x1F`, `0x7F`）を自動除去し、Bedrock の `ValidationException`（Malformed request）を防止。
  - **抽出テキスト上限（安全トランケート）**: 1ファイルあたり最大 **100,000 文字（`DefaultMaxExtractedLength`、約30,000〜50,000トークン相当）**。上限超過時は末尾に警告注記を付与して安全に切り詰め、モデルのコンテキスト長超過を防止。
  - **合計文字数上限**: 1リクエスト内の全添付の抽出テキスト合計は `MAX_TOTAL_ATTACHMENT_CHARS`（既定 **50,000** 文字、0 以下で無制限）まで。超過分は警告注記付きで切り詰め、上限到達後の添付は `[Attachment omitted: ...]` 注記のみとする。
  - **xberg 呼び出し**: `extract <file> --content-format markdown --extract-images false --pdf-extract-images false --ocr <XBERG_OCR> --no-config-discovery --log-level error`。画像抽出は常に無効、OCR は既定 false（`XBERG_OCR=true` で有効化）。タイムアウトは `XBERG_TIMEOUT_SEC`（既定 60 秒）。
  - **ストリーミングエラー耐性**: Bedrock エラー発生時、無言切断せず OpenAI 互換 SSE エラーチャンク（`data: {"error": ...}`）を送信して正常クローズ。
  - **エラーレスポンス**: 非ストリーム時は AWS エラーコードに応じて 400（ValidationException）/ 429（Throttling 等）/ 504（タイムアウト）/ 502（その他）を OpenAI 互換 JSON で返す。一律 500 を返さない。
  - **エラーログ**: チャット処理のエラー・xberg フォールバック・panic は `ERROR_LOG_FILE`（既定 `data/logs/error.log`）へ JSON Lines で追記する（`internal/logging`）。request_id、モデル、添付メタ（ファイル名・サイズ・xberg 終了コード/stderr 末尾・抽出文字数）、AWS エラーコード/RequestID を記録し、**base64 本文や抽出テキストは記録しない**。
  - **再現手段**: `scripts/repro/send_pdf.{sh,ps1}`（業務チャットと同形式で送信）、`scripts/repro/xberg_check.{sh,ps1}`（本番と同じ引数で xberg 単体実行）。実ファイルは `error_sample/`（git 管理外）に置き、`go test ./internal/services -run RealSamplePDFs -v` で抽出を検証できる。

### 2.2 S3 Vectors サイドカーメタデータ管理
S3 アップロード時には、必ず同一ディレクトリに `.metadata.json` を付与し、プレフィックスと属性を整合させる。

```go
// internal/services/knowledge_service.go
func (s *KnowledgeService) UploadDocumentWithMetadata(ctx context.Context, doc *models.KnowledgeDocument, content []byte) error {
    objKey := doc.S3Key
    metaKey := objKey + ".metadata.json"

    metadata := map[string]interface{}{
        "metadataAttributes": map[string]interface{}{
            "group":       doc.GroupName,
            "filename":    doc.Filename,
            "document_id": doc.ID,
        },
    }
    // 1. ファイル本体アップロード (s3Client.PutObject)
    // 2. サイドカーメタデータ JSON アップロード (s3Client.PutObject)
}
```

### 2.3 削除処理（整合性保証）
ドキュメント削除時は、DB上のステータス更新 ➔ S3実体およびメタデータの削除 ➔ Bedrock KB同期実行 ➔ DB物理削除 の順で安全に処理する。

```go
// internal/services/knowledge_service.go
func (s *KnowledgeService) DeleteKnowledgeDocument(ctx context.Context, docID string) error {
    // 1. ステータスを "DELETING" に更新
    // 2. S3 からファイル実体および .metadata.json を削除
    // 3. Bedrock KB Ingestion ジョブのトリガー (StartIngestionJob)
    // 4. DB レコードの削除
}
```

### 2.4 mini-id (OIDC) 規約チェックリスト
- [ ] 認可リクエストは `response_type=code` + PKCE (`code_challenge_method=S256`) を使用。
- [ ] `state`, `nonce` はリクエスト毎に暗号学的乱数で生成・検証。
- [ ] ユーザーの主識別子は必ず `sub`（UUID）を使用し、`username` や `email` をユニークキーとしない。
- [ ] アプリセッションCookieは `HttpOnly`, `SameSite=Lax`, HTTPS環境時は `Secure` を付与。
- [ ] ログイン成功時にセッションIDを再生成（セッション固定攻撃対策）。
- [ ] ログアウト時はアプリセッション破棄後に RP-Initiated Logout へ遷移可能にする。

### 2.5 SQLite 接続設定（高並行性対策）
CGO を使わない純 Go SQLite ドライバー（`glebarez/sqlite`）と GORM を使用し、接続時に WAL モードおよび適切な PRAGMA を確実に適用する。

```go
// internal/db/db.go
func applyPragmas(sqlDB *sql.DB) error {
    pragmas := []string{
        "PRAGMA journal_mode=WAL;",
        "PRAGMA busy_timeout=5000;",
        "PRAGMA synchronous=NORMAL;",
        "PRAGMA foreign_keys=ON;",
    }
    for _, pragma := range pragmas {
        if _, err := sqlDB.Exec(pragma); err != nil {
            return fmt.Errorf("executing '%s': %w", pragma, err)
        }
    }
    return nil
}
```
- コネクションプール設定: `SetMaxOpenConns(25)`, `SetMaxIdleConns(10)`
- スキーマ初期化: GORM の `db.AutoMigrate(...)` により、起動時に全モデルが自動同期される。

---

## 3. テストと動作検証

1. **Go ユニット & 結合テスト**:
   - `go test -v ./...` により、API、ハンドラー、DB、Bedrock/Knowledge 連携テストがすべて PASS することを確認。
2. **OpenAI 互換性テスト**:
   - 公式の `openai` SDK または `curl` を用いて、`base_url="http://localhost:8000/v1"` 経由でテキストおよび添付ファイル付きリクエストが正常に推論・ストリーミングされるか検証。
3. **閉域UI表示テスト**:
   - ブラウザの開発者ツールで外部ドメイン（CDN等）への通信が一切発生せず、内包アセット（Bootstrap 5.3 + htmx 2.x）が 200 OK でロードされていることを確認。
4. **RAG 権限分離テスト**:
   - `group: "sales"` のユーザー/APIキーで RAG 検索を実行した際、S3 Vectors プリフィルタにより他グループ専用のドキュメントが取得チャンクに含まれないことを検証。