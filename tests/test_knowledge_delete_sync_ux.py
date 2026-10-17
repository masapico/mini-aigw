import io
import json
from unittest.mock import MagicMock, patch
from fastapi.testclient import TestClient
from sqlmodel import Session, select
from app.core.config import settings
from app.core.database import engine, init_db
from app.core.security import hash_password
from app.main import app
from app.models.knowledge import KnowledgeDocument
from app.models.user import User


def setup_admin_and_document() -> tuple[User, str]:
    init_db()
    with Session(engine) as session:
        user = session.exec(select(User).where(User.username == "admin_delete_test")).first()
        if not user:
            user = User(
                auth_type="local",
                username="admin_delete_test",
                password_hash=hash_password("admin123"),
                roles='["admin"]',
            )
            session.add(user)
            session.commit()
            session.refresh(user)

        import uuid
        uid = str(uuid.uuid4())
        doc = KnowledgeDocument(
            id=f"doc-{uid}",
            s3_key=f"public/delete_test_{uid}.txt",
            s3_bucket=settings.KNOWLEDGE_S3_BUCKET or "test-bucket",
            filename=f"delete_test_{uid}.txt",
            file_size=100,
            content_type="text/plain",
            group_name="public",
            uploader_id=user.id,
            sync_status="SYNCED",
            data_source_id=settings.BEDROCK_DATA_SOURCE_ID or "ds-test-123",
        )
        session.add(doc)
        session.commit()
        session.refresh(doc)
        return user, doc.id


@patch("app.api.web.knowledge.get_bedrock_agent_status_client")
@patch("app.api.web.knowledge.get_s3_client")
@patch("app.api.web.knowledge.get_bedrock_agent_client")
def test_delete_triggers_immediate_sync_and_oob_status_update(mock_agent_client, mock_s3_client, mock_status_client):
    """
    ドキュメント削除時に即時KB同期が発火し、
    HTMXレスポンスに #kb-sync-status-container の OOB swap が含まれること、
    同期中のステータスと非活性ボタンが画面に反映されることを検証。
    """
    user, doc_id = setup_admin_and_document()
    client = TestClient(app)

    # ログイン
    client.post("/login", data={"username": "admin_delete_test", "password": "admin123"}, follow_redirects=False)

    mock_agent = MagicMock()
    mock_agent_client.return_value = mock_agent
    mock_status_client.return_value = mock_agent

    # start_ingestion_job の戻り値
    mock_agent.start_ingestion_job.return_value = {
        "ingestionJob": {"ingestionJobId": "job-del-sync-999", "status": "STARTING"}
    }
    # list_ingestion_jobs (同期中を模擬)
    mock_agent.list_ingestion_jobs.return_value = {
        "ingestionJobSummaries": [
            {
                "ingestionJobId": "job-del-sync-999",
                "status": "IN_PROGRESS",
                "updatedAt": "2026-10-17T01:00:00Z",
                "statistics": {"numberOfNewDocumentsIndexed": 0, "numberOfDocumentsScanned": 1},
            }
        ]
    }

    # HTMX による削除リクエスト
    del_resp = client.delete(f"/knowledge/{doc_id}", headers={"HX-Request": "true"})
    assert del_resp.status_code == 200

    # 1. start_ingestion_job が即時発火されていること
    assert mock_agent.start_ingestion_job.call_count >= 1

    # 2. レスポンスに OOB swap 要素が含まれること
    html = del_resp.text
    assert 'id="kb-sync-status-container"' in html
    assert 'hx-swap-oob="innerHTML"' in html

    # 3. 画面上に「同期中 (IN_PROGRESS)」が反映されていること
    assert "同期中 (IN_PROGRESS)" in html

    # 4. 同期中は同期ボタンが disabled かつ「同期実行中...」となり押せないこと
    assert "disabled" in html
    assert "同期実行中..." in html
    assert 'title="同期ジョブ実行中のため再同期できません"' in html
    # 誤実行を防ぐため hx-post="/knowledge/sync" は付与されていないこと
    assert 'hx-post="/knowledge/sync"' not in html

    # 5. 同期中であるため、ステータスバーが自動ポーリング属性を持つこと
    assert 'hx-get="/knowledge/sync/status"' in html
    assert 'hx-trigger="every 3s"' in html


@patch("app.api.web.knowledge.get_bedrock_agent_status_client")
def test_sync_status_polling_and_button_reactivation(mock_agent_client):
    """
    GET /knowledge/sync/status によるポーリング動作の検証:
    1. 同期中はボタンが非活性 (disabled)
    2. 同期完了 (COMPLETE) 後はボタンが再活性化され、HX-Trigger ヘッダーが付与されること
    """
    user, _ = setup_admin_and_document()
    client = TestClient(app)
    client.post("/login", data={"username": "admin_delete_test", "password": "admin123"}, follow_redirects=False)

    mock_agent = MagicMock()
    mock_agent_client.return_value = mock_agent

    # 1. 同期中の場合 (IN_PROGRESS)
    mock_agent.list_ingestion_jobs.return_value = {
        "ingestionJobSummaries": [
            {
                "ingestionJobId": "job-poll-001",
                "status": "IN_PROGRESS",
            }
        ]
    }
    resp_syncing = client.get("/knowledge/sync/status")
    assert resp_syncing.status_code == 200
    assert "同期中 (IN_PROGRESS)" in resp_syncing.text
    assert "disabled" in resp_syncing.text
    assert "同期実行中..." in resp_syncing.text
    assert 'hx-get="/knowledge/sync/status"' in resp_syncing.text
    assert "refreshKnowledgeTable" not in resp_syncing.headers.get("HX-Trigger", "")

    # 2. 同期完了の場合 (COMPLETE)
    mock_agent.list_ingestion_jobs.return_value = {
        "ingestionJobSummaries": [
            {
                "ingestionJobId": "job-poll-001",
                "status": "COMPLETE",
                "updatedAt": "2026-10-17T01:05:00Z",
                "statistics": {"numberOfNewDocumentsIndexed": 1, "numberOfDocumentsScanned": 1},
            }
        ]
    }
    resp_complete = client.get("/knowledge/sync/status")
    assert resp_complete.status_code == 200
    assert "✓ 同期完了 (COMPLETE)" in resp_complete.text
    assert "今すぐKB同期を実行" in resp_complete.text
    assert 'hx-post="/knowledge/sync"' in resp_complete.text
    # COMPLETE になったのでポーリング属性は除去されていること
    assert 'hx-trigger="every 3s"' not in resp_complete.text
    # ドキュメントテーブルを自動再読み込みするためのイベントヘッダーが付与されていること
    assert resp_complete.headers.get("HX-Trigger") == "refreshKnowledgeTable"


@patch("app.api.web.knowledge.get_s3_client")
@patch("app.api.web.knowledge.get_bedrock_agent_client")
def test_delete_non_htmx_compatibility(mock_agent_client, mock_s3_client):
    """
    HX-Request ヘッダーなしの通常 DELETE リクエストでも正常に同期が発火し、
    HTTP 200 空レスポンスが返ること（後方互換性）の検証。
    """
    user, doc_id = setup_admin_and_document()
    client = TestClient(app)
    client.post("/login", data={"username": "admin_delete_test", "password": "admin123"}, follow_redirects=False)

    mock_agent = MagicMock()
    mock_agent_client.return_value = mock_agent

    del_resp = client.delete(f"/knowledge/{doc_id}")
    assert del_resp.status_code == 200
    assert del_resp.text == ""
    assert mock_agent.start_ingestion_job.call_count >= 1
