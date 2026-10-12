# 開発ガイドライン・実装指示書 (AGENTS.md)

本ドキュメントは、AI実装エージェントおよび開発者が本システム（FastAPI + SQLite + Bedrock + mini-id + S3 Vectors）を実装・保守するための指示書である。

---

## 1. プロジェクト構造と環境方針

### 1.1 ディレクトリ構成
```
ai-gateway/
├── app/
│   ├── api/
│   │   ├── v1/
│   │   │   ├── chat.py           # OpenAI互換 /v1/chat/completions
│   │   │   └── models.py         # /v1/models
│   │   └── web/
│   │       ├── auth.py           # mini-id OIDC & ローカル認証
│   │       ├── knowledge.py      # ナレッジアップロード/削除/一覧
│   │       └── rag_test.py       # RAGテスト画面・HTMXエンドポイント
│   ├── core/
│   │   ├── config.py             # 環境変数・設定 (pydantic-settings)
│   │   ├── database.py           # SQLite (WAL設定付きエンジン)
│   │   └── security.py           # パスワードハッシュ(Argon2id), セッション
│   ├── models/                   # SQLModel 定義
│   ├── services/
│   │   ├── bedrock_service.py    # Converse API & ConverseStream
│   │   ├── knowledge_service.py  # S3 Vectors & Metadata JSON, Ingestion
│   │   ├── markitdown_service.py # ファイルテキスト抽出・正規化
│   │   └── oidc_service.py       # mini-id OIDCクライアント (Authlib)
│   ├── static/
│   │   └── vendor/
│   │       ├── bootstrap/        # bootstrap.min.css, bootstrap.bundle.min.js (同梱)
│   │       └── htmx/             # htmx.min.js (v2.x 同梱)
│   └── templates/                # Jinja2 テンプレート (Bootstrap 5 + htmx)
├── tests/
├── pyproject.toml
└── README.md
```

### 1.2 閉域環境・依存ライブラリ方針
- **外部CDNへの依存禁止**: HTMLテンプレート内で `cdn.jsdelivr.net` 等の外部URLを参照してはならない。必ず `/static/vendor/...` のローカルファイルを指定する。
- **UIフレームワーク**:
  - CSS/UI: **Bootstrap 5.3**
  - 動的インタラクション: **htmx 2.x**（静的JS配置）
- **ファイル解析**: `markitdown` を用いてドキュメント（PDF, Office文書, CSV等）を統一的にMarkdown文字列化する。

---

## 2. 実装詳細とコーディング規約

### 2.1 添付ファイル処理（`markitdown` 連携）
OpenAI互換リクエストで届いたBase64添付ファイルは、モデルの種類（マルチモーダル対応・非対応）に関わらず安全に扱えるよう処理する。

```python
# app/services/markitdown_service.py
import io
import base64
from markitdown import MarkItDown

md_converter = MarkItDown()

def extract_content_from_base64(b64_string: str, filename: str) -> str:
    """Base64文字列からバイナリを復元し、markitdownでMarkdownテキストを抽出する"""
    file_bytes = base64.b64decode(b64_string)
    file_io = io.BytesIO(file_bytes)
    file_io.name = filename
    result = md_converter.convert_stream(file_io)
    return result.text_content
```

### 2.2 S3 Vectors サイドカーメタデータ管理
S3 アップロード時には、必ず同一ディレクトリに `.metadata.json` を付与し、プレフィックスと属性を整合させる。

```python
# app/services/knowledge_service.py
import json
import boto3

async def upload_document_with_metadata(s3_client, bucket: str, prefix: str, filename: str, content: bytes, group: str):
    obj_key = f"{prefix}/{filename}"
    meta_key = f"{obj_key}.metadata.json"
    
    metadata_content = {
        "metadataAttributes": {
            "group": group,
            "filename": filename
        }
    }
    
    # 1. ファイル本体アップロード
    s3_client.put_object(Bucket=bucket, Key=obj_key, Body=content)
    # 2. サイドカーメタデータ JSON アップロード
    s3_client.put_object(
        Bucket=bucket,
        Key=meta_key,
        Body=json.dumps(metadata_content).encode("utf-8"),
        ContentType="application/json"
    )
```

### 2.3 削除処理（整合性保証）
ドキュメント削除時は、DB上のステータス更新 ➔ S3実体およびメタデータの削除 ➔ Bedrock KB同期実行 の順で安全に処理する。

```python
async def delete_knowledge_document(db, s3_client, bedrock_agent, kb_id: str, doc_id: str):
    doc = db.get(KnowledgeDocument, doc_id)
    doc.sync_status = "DELETING"
    db.commit()

    # S3 削除 (本体 & メタデータ)
    s3_client.delete_object(Bucket=doc.s3_bucket, Key=doc.s3_key)
    s3_client.delete_object(Bucket=doc.s3_bucket, Key=f"{doc.s3_key}.metadata.json")

    # KB Ingestion ジョブのトリガー
    bedrock_agent.start_ingestion_job(
        knowledgeBaseId=kb_id,
        dataSourceId=doc.data_source_id
    )

    # 完了後レコード削除
    db.delete(doc)
    db.commit()
```

### 2.4 mini-id (OIDC) 規約チェックリスト
- [ ] 認可リクエストは `response_type=code` + PKCE (`code_challenge_method=S256`) を使用。
- [ ] `state`, `nonce` はリクエスト毎に暗号学的乱数で生成・検証。
- [ ] ユーザーの主識別子は必ず `sub`（UUID）を使用し、`username` や `email` をユニークキーとしない。
- [ ] アプリセッションCookieは `HttpOnly`, `SameSite=Lax`, HTTPS環境時は `Secure` を付与。
- [ ] ログイン成功時にセッションIDを再生成（セッション固定攻撃対策）。
- [ ] ログアウト時はアプリセッション破棄後に RP-Initiated Logout へ遷移可能にする。

### 2.5 SQLite 接続設定（高並行性対策）
```python
# app/core/database.py
from sqlmodel import create_engine
from sqlalchemy import event

engine = create_engine(
    "sqlite:///data/gateway.db",
    connect_args={"check_same_thread": False, "timeout": 15}
)

@event.listens_for(engine, "connect")
def set_sqlite_pragma(dbapi_connection, connection_record):
    cursor = dbapi_connection.cursor()
    cursor.execute("PRAGMA journal_mode=WAL")
    cursor.execute("PRAGMA synchronous=NORMAL")
    cursor.execute("PRAGMA foreign_keys=ON")
    cursor.close()
```

---

## 3. テストと動作検証

1. **OpenAI 互換性テスト**:
   - 公式の `openai` Python SDK クライアントを用いて、`base_url="http://localhost:8000/v1"` 経由でテキストおよび添付ファイル付きリクエストが Bedrock Converse API に正常に転送され、回答が得られるか検証。
2. **閉域UI表示テスト**:
   - ブラウザの開発者ツールで外部ドメイン（CDN等）への通信が一切発生せず、Bootstrap 5.3 と htmx 2.x がローカルから 200 OK でロードされていることを確認。
3. **RAG 権限分離テスト**:
   - `group: "sales"` のユーザーで RAG 検索を実行した際、S3 Vectors フィルタにより `dev` 専用のドキュメントが取得チャンクに含まれないことを検証。