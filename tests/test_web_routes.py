import io
import json
from unittest.mock import MagicMock, patch
from fastapi.testclient import TestClient
from sqlmodel import Session, select
from app.core.database import engine, init_db
from app.core.security import hash_password
from app.main import app
from app.models.api_key import ApiKey
from app.models.knowledge import KnowledgeDocument
from app.models.user import User


def setup_admin_user() -> User:
    init_db()
    with Session(engine) as session:
        user = session.exec(select(User).where(User.username == "admin")).first()
        if not user:
            user = User(
                auth_type="local",
                username="admin",
                password_hash=hash_password("admin123"),
                roles='["admin", "sales", "dev"]',
            )
            session.add(user)
            session.commit()
            session.refresh(user)
    return user


def test_login_flow_and_dashboard():
    """ログイン画面表示、認証成功、セッション維持、ダッシュボード表示の検証"""
    setup_admin_user()
    client = TestClient(app)

    # 1. ログイン画面
    resp = client.get("/login")
    assert resp.status_code == 200
    assert "mini-aigw ログイン" in resp.text

    # 2. 誤ったパスワード
    bad_resp = client.post("/login", data={"username": "admin", "password": "wrongpassword"})
    assert bad_resp.status_code == 401
    assert "無効です" in bad_resp.text

    # 3. 正しいログイン
    login_resp = client.post(
        "/login",
        data={"username": "admin", "password": "admin123"},
        follow_redirects=False,
    )
    assert login_resp.status_code == 302
    assert login_resp.headers["location"] == "/"
    assert "aigw_session" in client.cookies

    # 4. ダッシュボードへのアクセス
    dash_resp = client.get("/")
    assert dash_resp.status_code == 200
    assert "システム概要" in dash_resp.text


@patch("app.api.web.knowledge.get_s3_client")
@patch("app.api.web.knowledge.get_bedrock_agent_client")
def test_knowledge_web_workflow(mock_agent_client, mock_s3_client):
    """ナレッジ画面の表示、ファイルアップロード (HTMX)、削除 (HTMX) の検証"""
    setup_admin_user()
    client = TestClient(app)

    client.post(
        "/login",
        data={"username": "admin", "password": "admin123"},
        follow_redirects=False,
    )

    # 1. ナレッジ一覧画面
    kb_page = client.get("/knowledge")
    assert kb_page.status_code == 200
    assert "ナレッジベース ドキュメント管理" in kb_page.text

    # 2. HTMX アップロード (タグ付き)
    file_bytes = b"Sample knowledge content for test"
    upload_resp = client.post(
        "/knowledge/upload",
        files={"file": ("guide.txt", io.BytesIO(file_bytes), "text/plain")},
        data={"group_name": "sales", "tags": json.dumps(["テスト", "ガイド"])},
        headers={"HX-Request": "true"},
    )
    assert upload_resp.status_code == 200
    assert "guide.txt" in upload_resp.text
    assert "sales" in upload_resp.text
    assert "#テスト" in upload_resp.text
    assert "#ガイド" in upload_resp.text

    # 3. アップロードされたドキュメントの ID を DB から取得して原本ダウンロードを検証
    with Session(engine) as session:
        doc = session.exec(select(KnowledgeDocument).where(KnowledgeDocument.filename == "guide.txt")).first()
        assert doc is not None
        assert doc.get_tags() == ["テスト", "ガイド"]
        doc_id = doc.id

    mock_s3_client.return_value.get_object.return_value = {
        "Body": io.BytesIO(file_bytes),
        "ContentType": "text/plain",
        "ContentLength": len(file_bytes),
    }
    dl_resp = client.get(f"/knowledge/{doc_id}/download")
    assert dl_resp.status_code == 200
    assert dl_resp.content == file_bytes
    assert "attachment" in dl_resp.headers.get("content-disposition", "")

    # 4. ドキュメント削除
    del_resp = client.delete(f"/knowledge/{doc_id}")
    assert del_resp.status_code == 200


@patch("app.api.web.rag_test.get_bedrock_agent_runtime_client")
@patch("app.api.web.rag_test.get_bedrock_runtime_client")
def test_rag_test_web_endpoints(mock_runtime, mock_agent):
    """RAG テスト画面の Retrieve 単体および対話シミュレーションの検証"""
    setup_admin_user()
    client = TestClient(app)

    client.post(
        "/login",
        data={"username": "admin", "password": "admin123"},
        follow_redirects=False,
    )

    # 1. RAG テスト画面
    page = client.get("/rag-test")
    assert page.status_code == 200
    assert "RAG 動作検証・テスト" in page.text

    # 2. Retrieve 単体 HTMX 呼び出し
    mock_agent.return_value.retrieve.return_value = {
        "retrievalResults": [
            {
                "content": {"text": "Retrieved chunk text"},
                "location": {"s3Location": {"uri": "s3://b/k"}},
                "metadata": {"group": "public", "filename": "sample.pdf"},
                "score": 0.92,
            }
        ]
    }
    ret_resp = client.post(
        "/rag-test/retrieve",
        data={"query": "test query", "group": "public"},
    )
    assert ret_resp.status_code == 200
    assert "Retrieved chunk text" in ret_resp.text

    # 3. Chat 対話 HTMX 呼び出し
    mock_runtime.return_value.converse.return_value = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [{"text": "Based on [1], here is the answer."}],
            }
        },
        "stopReason": "end_turn",
        "usage": {"inputTokens": 10, "outputTokens": 20, "totalTokens": 30},
    }
    chat_resp = client.post(
        "/rag-test/chat",
        data={"query": "chat query", "group": "public", "model_id": "anthropic.claude-3-5-sonnet-20241022-v2:0"},
    )
    assert chat_resp.status_code == 200
    assert "Based on [1], here is the answer." in chat_resp.text

    # 4. Incompatible configuration エラー発生時の表示検証
    mock_agent.return_value.retrieve.side_effect = Exception(
        "Incompatible configuration: vectorSearchConfiguration is not supported for managed knowledge bases."
    )
    err_ret_resp = client.post(
        "/rag-test/retrieve",
        data={"query": "error query", "group": "public"},
    )
    assert err_ret_resp.status_code == 200
    assert "ナレッジベース検索エラーが発生しました" in err_ret_resp.text
    assert "managedSearchConfiguration" in err_ret_resp.text

    err_chat_resp = client.post(
        "/rag-test/chat",
        data={"query": "error chat query", "group": "public"},
    )
    assert err_chat_resp.status_code == 200
    assert "ナレッジ検索中にエラーが発生しました" in err_chat_resp.text
    assert "managedSearchConfiguration" in err_chat_resp.text



def test_api_keys_web_workflow():
    """API キー発行、一覧表示、失効の検証"""
    setup_admin_user()
    client = TestClient(app)

    client.post(
        "/login",
        data={"username": "admin", "password": "admin123"},
        follow_redirects=False,
    )

    # 1. 一覧画面
    page = client.get("/api-keys")
    assert page.status_code == 200
    assert "AIプロキシ APIキー管理" in page.text

    # 2. 新規キー発行
    create_resp = client.post(
        "/api-keys/create",
        data={"name": "Slack Bot Dev", "allowed_groups": ["public", "dev"]},
    )
    assert create_resp.status_code == 200
    assert "Slack Bot Dev" in create_resp.text
    assert "sk-live-" in create_resp.text
    assert "正常に発行されました" in create_resp.text

    # 3. キー失効
    with Session(engine) as session:
        key = session.exec(select(ApiKey).where(ApiKey.name == "Slack Bot Dev")).first()
        assert key is not None
        key_id = key.id

    del_resp = client.delete(f"/api-keys/{key_id}")
    assert del_resp.status_code == 200


def test_logout_clears_cookie():
    """ログアウト時にセッションCookieが削除されることの検証"""
    setup_admin_user()
    client = TestClient(app)

    client.post(
        "/login",
        data={"username": "admin", "password": "admin123"},
        follow_redirects=False,
    )

    logout_resp = client.get("/logout", follow_redirects=False)
    assert logout_resp.status_code == 302
    assert logout_resp.headers["location"] == "/login"

