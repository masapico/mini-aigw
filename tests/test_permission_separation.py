import io
import json
from unittest.mock import MagicMock, patch
from fastapi.testclient import TestClient
from sqlmodel import Session, select
from app.core.database import engine, init_db
from app.core.security import hash_password
from app.main import app
from app.models.api_key import ApiKey
from app.models.group import Group
from app.models.knowledge import KnowledgeDocument
from app.models.user import User


def setup_test_users_and_groups():
    init_db()
    with Session(engine) as session:
        # グループ作成
        for g_name, g_disp in [("sales", "営業部"), ("dev", "開発部")]:
            grp = session.exec(select(Group).where(Group.name == g_name)).first()
            if not grp:
                grp = Group(name=g_name, display_name=g_disp)
                session.add(grp)

        # 管理者
        admin = session.exec(select(User).where(User.username == "test_admin")).first()
        if not admin:
            admin = User(
                auth_type="local",
                username="test_admin",
                display_name="システム管理者",
                password_hash=hash_password("admin_pass"),
                roles='["admin"]',
                groups='["dev", "sales"]',
            )
            session.add(admin)

        # 営業部一般ユーザー
        sales_user = session.exec(select(User).where(User.username == "sales_user")).first()
        if not sales_user:
            sales_user = User(
                auth_type="local",
                username="sales_user",
                display_name="営業 太郎",
                password_hash=hash_password("user_pass"),
                roles='["user"]',
                groups='["sales"]',
            )
            session.add(sales_user)

        session.commit()


def login_as(client: TestClient, username: str, password: str):
    resp = client.post(
        "/login",
        data={"username": username, "password": password},
        follow_redirects=False,
    )
    assert resp.status_code == 302


def test_api_keys_permission_denied_for_regular_user():
    """一般ユーザーは APIキー一覧・作成・削除にアクセスできない（403 Forbidden）"""
    setup_test_users_and_groups()
    client = TestClient(app)
    login_as(client, "sales_user", "user_pass")

    # 1. 一覧アクセス
    resp = client.get("/api-keys")
    assert resp.status_code == 403
    assert "管理者権限が必要です" in resp.json()["detail"]

    # 2. キー作成
    create_resp = client.post(
        "/api-keys/create",
        data={"name": "Attacker Key", "allowed_groups": ["dev"]},
    )
    assert create_resp.status_code == 403

    # 3. キー削除 (管理者で1つ作ってから削除試行)
    admin_client = TestClient(app)
    login_as(admin_client, "test_admin", "admin_pass")
    admin_create = admin_client.post(
        "/api-keys/create",
        data={"name": "Admin Key", "allowed_groups": ["public"]},
    )
    assert admin_create.status_code == 200

    with Session(engine) as session:
        key = session.exec(select(ApiKey).where(ApiKey.name == "Admin Key")).first()
        assert key is not None
        key_id = key.id

    del_resp = client.delete(f"/api-keys/{key_id}")
    assert del_resp.status_code == 403


@patch("app.api.web.knowledge.get_s3_client")
def test_knowledge_upload_group_isolation(mock_s3_client):
    """一般ユーザーは自身の登録グループにのみアップロードでき、他グループおよび public への登録は拒否される"""
    setup_test_users_and_groups()
    client = TestClient(app)
    login_as(client, "sales_user", "user_pass")

    mock_s3_client.return_value.put_object.return_value = {}

    # 1. 自身の登録グループ grp000 へのアップロード ➔ 成功 (200 OK & S3_UPLOADED)
    file_content = b"Sales report content"
    resp_sales = client.post(
        "/knowledge/upload",
        files={"file": ("sales_doc.txt", io.BytesIO(file_content), "text/plain")},
        data={"group_name": "grp000"},
        headers={"HX-Request": "true"},
    )
    assert resp_sales.status_code == 200
    assert "sales_doc.txt" in resp_sales.text
    assert "S3保管" in resp_sales.text

    # 2. 所属外グループ dev へのアップロード ➔ 拒否 (403 Forbidden)
    resp_dev = client.post(
        "/knowledge/upload",
        files={"file": ("hacked_doc.txt", io.BytesIO(file_content), "text/plain")},
        data={"group_name": "grp001"},
        headers={"HX-Request": "true"},
    )
    assert resp_dev.status_code == 403
    assert "ドキュメント登録権限がありません" in resp_dev.json()["detail"]

    # 3. 一般ユーザーによる public へのアップロード ➔ 拒否 (403 Forbidden)
    resp_pub = client.post(
        "/knowledge/upload",
        files={"file": ("public_doc.txt", io.BytesIO(file_content), "text/plain")},
        data={"group_name": "public"},
        headers={"HX-Request": "true"},
    )
    assert resp_pub.status_code == 403

    # 4. 管理者による public へのアップロード ➔ 成功 (200 OK)
    admin_client = TestClient(app)
    login_as(admin_client, "test_admin", "admin_pass")
    admin_pub = admin_client.post(
        "/knowledge/upload",
        files={"file": ("company_rules.txt", io.BytesIO(file_content), "text/plain")},
        data={"group_name": "public"},
        headers={"HX-Request": "true"},
    )
    assert admin_pub.status_code == 200
    assert "company_rules.txt" in admin_pub.text


@patch("app.api.web.knowledge.get_s3_client")
def test_knowledge_list_and_download_permissions(mock_s3_client):
    """一般ユーザーは他グループのドキュメントを一覧で見えず、ダウンロードも拒否される"""
    setup_test_users_and_groups()
    admin_client = TestClient(app)
    login_as(admin_client, "test_admin", "admin_pass")

    # 管理者が dev グループの機密ドキュメントを登録
    mock_s3_client.return_value.put_object.return_value = {}
    admin_client.post(
        "/knowledge/upload",
        files={"file": ("dev_secret.txt", io.BytesIO(b"Secret dev source code"), "text/plain")},
        data={"group_name": "dev"},
    )

    with Session(engine) as session:
        dev_doc = session.exec(select(KnowledgeDocument).where(KnowledgeDocument.filename == "dev_secret.txt")).first()
        assert dev_doc is not None
        dev_doc_id = dev_doc.id

    # 営業部一般ユーザーで一覧取得
    sales_client = TestClient(app)
    login_as(sales_client, "sales_user", "user_pass")

    page = sales_client.get("/knowledge")
    assert page.status_code == 200
    assert "dev_secret.txt" not in page.text  # 一覧に表示されない
    assert "営業 太郎" in page.text or "登録者" in page.text  # 登録者列が存在

    # 営業部一般ユーザーが dev ドキュメントのダウンロードを試行 ➔ 403 Forbidden
    dl_resp = sales_client.get(f"/knowledge/{dev_doc_id}/download")
    assert dl_resp.status_code == 403
    assert "ダウンロードする権限がありません" in dl_resp.json()["detail"]


@patch("app.api.web.knowledge.get_bedrock_agent_client")
def test_knowledge_sync_admin_only(mock_agent_client):
    """管理者のみが KB同期ジョブを手動トリガーできる"""
    setup_test_users_and_groups()
    mock_agent_client.return_value.start_ingestion_job.return_value = {
        "ingestionJob": {"ingestionJobId": "JOB-12345"}
    }
    mock_agent_client.return_value.list_ingestion_jobs.return_value = {
        "ingestionJobSummaries": [
            {
                "ingestionJobId": "JOB-12345",
                "status": "STARTING",
                "statistics": {"numberOfNewDocumentsIndexed": 0, "numberOfDocumentsScanned": 0},
            }
        ]
    }

    # 一般ユーザー ➔ 403 Forbidden
    sales_client = TestClient(app)
    login_as(sales_client, "sales_user", "user_pass")
    user_sync = sales_client.post("/knowledge/sync")
    assert user_sync.status_code == 403

    # 管理者 ➔ 200 OK (HTMX)
    admin_client = TestClient(app)
    login_as(admin_client, "test_admin", "admin_pass")
    admin_sync = admin_client.post("/knowledge/sync", headers={"HX-Request": "true"})
    assert admin_sync.status_code == 200
    assert "JOB-12345" in admin_sync.text


@patch("app.api.web.rag_test.get_bedrock_agent_runtime_client")
@patch("app.api.web.rag_test.get_bedrock_runtime_client")
def test_rag_test_permissions_plan_a(mock_runtime, mock_agent):
    """案A: 一般ユーザーは所属グループの単体検索のみ可能、他グループ検索および対話テストは拒否"""
    setup_test_users_and_groups()
    sales_client = TestClient(app)
    login_as(sales_client, "sales_user", "user_pass")

    # 1. 所属グループ grp000 での Retrieve 単体検索 ➔ 成功 (200 OK)
    mock_agent.return_value.retrieve.return_value = {
        "retrievalResults": [
            {
                "content": {"text": "Sales plan chunk"},
                "location": {"s3Location": {"uri": "s3://b/k"}},
                "metadata": {"group": "group000", "filename": "sales.pdf"},
                "score": 0.9,
            }
        ]
    }
    ret_sales = sales_client.post("/rag-test/retrieve", data={"query": "test query", "group": "group000"})
    assert ret_sales.status_code == 200
    assert "Sales plan chunk" in ret_sales.text

    # 2. 所属外グループ grp001 での Retrieve 単体検索 ➔ 拒否 (403 Forbidden)
    ret_dev = sales_client.post("/rag-test/retrieve", data={"query": "dev query", "group": "group001"})
    assert ret_dev.status_code == 403
    assert "検索する権限がありません" in ret_dev.json()["detail"]

    # 3. 一般ユーザーによる対話テスト (Chat) ➔ 拒否 (403 Forbidden)
    chat_user = sales_client.post(
        "/rag-test/chat",
        data={"query": "hello", "group": "group000", "model_id": "test-model"},
    )
    assert chat_user.status_code == 403

    # 4. 管理者による対話テスト ➔ 成功 (200 OK)
    admin_client = TestClient(app)
    login_as(admin_client, "test_admin", "admin_pass")
    mock_runtime.return_value.converse.return_value = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [{"text": "Admin RAG response."}],
            }
        },
        "stopReason": "end_turn",
        "usage": {"inputTokens": 10, "outputTokens": 20, "totalTokens": 30},
    }
    chat_admin = admin_client.post(
        "/rag-test/chat",
        data={"query": "admin query", "group": "group000", "model_id": "test-model"},
    )
    assert chat_admin.status_code == 200
    assert "Admin RAG response." in chat_admin.text


def test_jst_datetime_filter():
    """JST (UTC+9) 変換フィルタの検証"""
    from datetime import datetime, timezone
    from app.core.templates import to_jst

    # 1. naive UTC: 2026-10-13 02:30:00 -> JST: 2026-10-13 11:30 (+9時間)
    dt_naive = datetime(2026, 10, 13, 2, 30, 0)
    assert to_jst(dt_naive) == "2026-10-13 11:30"

    # 2. tz-aware UTC: 2026-10-13 15:00:00 -> JST: 2026-10-14 00:00 (日付跨ぎ)
    dt_aware = datetime(2026, 10, 13, 15, 0, 0, tzinfo=timezone.utc)
    assert to_jst(dt_aware) == "2026-10-14 00:00"

    # 3. ISO 文字列
    assert to_jst("2026-10-13T02:30:00Z") == "2026-10-13 11:30"
    assert to_jst(None) == "-"


@patch("app.api.web.knowledge.get_bedrock_agent_client")
@patch("app.api.web.knowledge.get_s3_client")
def test_knowledge_sync_status_lifecycle_and_error_handling(mock_s3, mock_agent):
    """
    KB同期のライフサイクルおよびエラー時の整合性検証:
    1. アップロード直後は 'S3_UPLOADED' (S3保管)
    2. 同期ジョブが FAILED (エラー) の場合、'S3_UPLOADED' のまま維持される
    3. 同期ジョブが COMPLETE の場合、'SYNCED' (同期済) に自動昇格する
    """
    from datetime import datetime, timezone, timedelta
    setup_test_users_and_groups()
    admin_client = TestClient(app)
    login_as(admin_client, "test_admin", "admin_pass")

    mock_s3.return_value.put_object.return_value = {}

    # 1. ドキュメントアップロード ➔ S3_UPLOADED
    upload_resp = admin_client.post(
        "/knowledge/upload",
        files={"file": ("sync_test.txt", io.BytesIO(b"Sync content"), "text/plain")},
        data={"group_name": "group000"},
        headers={"HX-Request": "true"},
    )
    assert upload_resp.status_code == 200
    assert "S3保管" in upload_resp.text

    with Session(engine) as session:
        doc = session.exec(select(KnowledgeDocument).where(KnowledgeDocument.filename == "sync_test.txt")).first()
        assert doc is not None
        assert doc.sync_status == "S3_UPLOADED"

    # 2. 同期ジョブが FAILED の場合 ➔ S3_UPLOADED のまま安全に維持される
    now_utc = datetime.now(timezone.utc)
    mock_agent.return_value.list_ingestion_jobs.return_value = {
        "ingestionJobSummaries": [
            {
                "ingestionJobId": "JOB-FAILED-999",
                "status": "FAILED",
                "startedAt": now_utc,
                "updatedAt": now_utc,
            }
        ]
    }

    # 一覧取得
    page_fail = admin_client.get("/knowledge")
    assert page_fail.status_code == 200

    with Session(engine) as session:
        doc_after_fail = session.exec(select(KnowledgeDocument).where(KnowledgeDocument.filename == "sync_test.txt")).first()
        assert doc_after_fail.sync_status == "S3_UPLOADED"  # 昇格されず維持

    # 3. 同期ジョブが COMPLETE の場合 ➔ SYNCED (同期済) に昇格
    job_complete_time = now_utc + timedelta(minutes=5)
    mock_agent.return_value.list_ingestion_jobs.return_value = {
        "ingestionJobSummaries": [
            {
                "ingestionJobId": "JOB-SUCCESS-111",
                "status": "COMPLETE",
                "startedAt": job_complete_time,
                "updatedAt": job_complete_time,
            }
        ]
    }

    # 一覧取得で同期検知
    page_success = admin_client.get("/knowledge")
    assert page_success.status_code == 200

    with Session(engine) as session:
        doc_after_success = session.exec(select(KnowledgeDocument).where(KnowledgeDocument.filename == "sync_test.txt")).first()
        assert doc_after_success.sync_status == "SYNCED"  # SYNCED に昇格

