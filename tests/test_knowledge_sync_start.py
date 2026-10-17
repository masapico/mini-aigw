from unittest.mock import MagicMock, patch

from fastapi.testclient import TestClient

from app.main import app
from tests.test_knowledge_delete_sync_ux import setup_admin_and_document


def _admin_client() -> TestClient:
    setup_admin_and_document()
    client = TestClient(app)
    client.post("/login", data={"username": "admin_delete_test", "password": "admin123"}, follow_redirects=False)
    return client


@patch("app.api.web.knowledge.get_bedrock_agent_status_client")
@patch("app.api.web.knowledge.get_bedrock_agent_client")
def test_sync_start_shows_syncing_even_if_list_returns_stale_complete(mock_agent_client, mock_status_client):
    """開始直後に list が古い COMPLETE を返しても、開始応答の STARTING が即時表示される"""
    client = _admin_client()
    agent = MagicMock()
    mock_agent_client.return_value = agent
    mock_status_client.return_value = agent
    agent.start_ingestion_job.return_value = {"ingestionJob": {"ingestionJobId": "job-new", "status": "STARTING"}}
    agent.list_ingestion_jobs.return_value = {"ingestionJobSummaries": [{"ingestionJobId": "old", "status": "COMPLETE"}]}

    resp = client.post("/knowledge/sync", headers={"HX-Request": "true"})
    assert resp.status_code == 200
    assert "同期中 (STARTING)" in resp.text
    assert 'hx-trigger="every 3s"' in resp.text
    assert 'hx-post="/knowledge/sync"' not in resp.text


@patch("app.api.web.knowledge.get_bedrock_agent_status_client")
@patch("app.api.web.knowledge.get_bedrock_agent_client")
def test_sync_start_failure_is_shown_in_ui(mock_agent_client, mock_status_client):
    """開始失敗時は 200 でエラーメッセージを表示し、ボタンは活性のまま"""
    client = _admin_client()
    agent = MagicMock()
    mock_agent_client.return_value = agent
    mock_status_client.return_value = agent
    agent.start_ingestion_job.side_effect = RuntimeError("boom")
    agent.list_ingestion_jobs.return_value = {"ingestionJobSummaries": []}

    resp = client.post("/knowledge/sync", headers={"HX-Request": "true"})
    assert resp.status_code == 200
    assert "KB同期の開始に失敗しました" in resp.text
    assert 'hx-post="/knowledge/sync"' in resp.text


@patch("app.api.web.knowledge.get_bedrock_agent_status_client")
def test_status_fetch_failure_keeps_button_active_with_warning(mock_status_client):
    """ステータス取得失敗時は警告表示のみで、ボタンは活性のまま"""
    client = _admin_client()
    agent = MagicMock()
    mock_status_client.return_value = agent
    agent.list_ingestion_jobs.side_effect = RuntimeError("timeout")

    resp = client.get("/knowledge/sync/status")
    assert resp.status_code == 200
    assert "ステータス取得失敗" in resp.text
    assert 'hx-post="/knowledge/sync"' in resp.text

