import io
import json
from unittest.mock import MagicMock, patch
from fastapi.testclient import TestClient
from sqlmodel import Session, select
from app.core.database import engine, init_db
from app.core.security import hash_password
from app.main import app
from app.models.knowledge import KnowledgeDocument
from app.models.user import User


def setup_test_users():
    init_db()
    with Session(engine) as session:
        admin = session.exec(select(User).where(User.username == "admin_page_test")).first()
        if not admin:
            admin = User(
                id="user-admin-page-test",
                auth_type="local",
                username="admin_page_test",
                password_hash=hash_password("pass123"),
                roles='["admin", "sales", "dev"]',
            )
            session.add(admin)

        sales_user = session.exec(select(User).where(User.username == "sales_page_test")).first()
        if not sales_user:
            sales_user = User(
                id="user-sales-page-test",
                auth_type="local",
                username="sales_page_test",
                password_hash=hash_password("pass123"),
                roles='["user"]',
                groups='["sales"]',
            )
            session.add(sales_user)

        session.commit()


def test_knowledge_filter_by_keyword_group_and_status():
    """ナレッジ一覧のキーワード、グループ、同期状態フィルターの検証"""
    setup_test_users()
    client = TestClient(app)

    # ログイン
    login_resp = client.post(
        "/login",
        data={"username": "admin_page_test", "password": "pass123"},
        follow_redirects=False,
    )
    assert login_resp.status_code == 302

    # テスト用ドキュメント作成
    with Session(engine) as session:
        for old in session.exec(select(KnowledgeDocument).where(KnowledgeDocument.id.in_(["doc-filter-001", "doc-filter-002", "doc-filter-003"]))).all():
            session.delete(old)
        session.commit()

        doc1 = KnowledgeDocument(
            id="doc-filter-001",
            s3_key="groups/sales/sales_report_2024.pdf",
            s3_bucket="sample-bucket",
            filename="営業売上報告書2024.pdf",
            file_size=2048,
            content_type="application/pdf",
            group_name="sales",
            sync_status="SYNCED",
        )
        doc1.set_tags(["2024", "売上"])

        doc2 = KnowledgeDocument(
            id="doc-filter-002",
            s3_key="groups/dev/system_architecture.pdf",
            s3_bucket="sample-bucket",
            filename="システム構成図.pdf",
            file_size=4096,
            content_type="application/pdf",
            group_name="dev",
            sync_status="S3_UPLOADED",
        )
        doc2.set_tags(["インフラ", "設計"])

        doc3 = KnowledgeDocument(
            id="doc-filter-003",
            s3_key="public/company_rule.pdf",
            s3_bucket="sample-bucket",
            filename="就業規則.pdf",
            file_size=1024,
            content_type="application/pdf",
            group_name="public",
            sync_status="SYNCED",
        )
        doc3.set_tags(["規程", "人事"])

        session.add_all([doc1, doc2, doc3])
        session.commit()

    try:
        with patch("app.api.web.knowledge.get_bedrock_agent_client"):
            # 1. フィルターなし (全件取得)
            resp_all = client.get("/knowledge/table")
            assert resp_all.status_code == 200
            assert "営業売上報告書2024.pdf" in resp_all.text
            assert "システム構成図.pdf" in resp_all.text
            assert "就業規則.pdf" in resp_all.text

            # 2. キーワード検索 (ファイル名マッチ: "売上")
            resp_q1 = client.get("/knowledge/table?q=売上")
            assert resp_q1.status_code == 200
            assert "営業売上報告書2024.pdf" in resp_q1.text
            assert "システム構成図.pdf" not in resp_q1.text
            assert "就業規則.pdf" not in resp_q1.text

            # 3. キーワード検索 (タグマッチ: "インフラ")
            resp_q2 = client.get("/knowledge/table?q=インフラ")
            assert resp_q2.status_code == 200
            assert "システム構成図.pdf" in resp_q2.text
            assert "営業売上報告書2024.pdf" not in resp_q2.text

            # 4. グループ絞り込み (group=sales)
            resp_grp = client.get("/knowledge/table?group=sales")
            assert resp_grp.status_code == 200
            assert "営業売上報告書2024.pdf" in resp_grp.text
            assert "システム構成図.pdf" not in resp_grp.text
            assert "就業規則.pdf" not in resp_grp.text

            # 5. ステータス絞り込み (status=S3_UPLOADED)
            resp_st = client.get("/knowledge/table?status=S3_UPLOADED")
            assert resp_st.status_code == 200
            assert "システム構成図.pdf" in resp_st.text
            assert "営業売上報告書2024.pdf" not in resp_st.text

            # 6. 一致なし検索
            resp_empty = client.get("/knowledge/table?q=存在しないドキュメント")
            assert resp_empty.status_code == 200
            assert "条件に一致するドキュメントは見つかりませんでした" in resp_empty.text
    finally:
        with Session(engine) as session:
            for d in session.exec(select(KnowledgeDocument).where(KnowledgeDocument.id.in_(["doc-filter-001", "doc-filter-002", "doc-filter-003"]))).all():
                session.delete(d)
            session.commit()


def test_knowledge_pagination():
    """ナレッジ一覧のページネーション（10件区切り・ページ送り・件数表示）の検証"""
    setup_test_users()
    client = TestClient(app)

    # ログイン
    client.post(
        "/login",
        data={"username": "admin_page_test", "password": "pass123"},
        follow_redirects=False,
    )

    page_ids = [f"doc-page-{i:03d}" for i in range(1, 16)]
    with Session(engine) as session:
        for old in session.exec(select(KnowledgeDocument).where(KnowledgeDocument.id.in_(page_ids))).all():
            session.delete(old)
        session.commit()

        for i in range(1, 16):
            doc = KnowledgeDocument(
                id=f"doc-page-{i:03d}",
                s3_key=f"public/test_doc_{i:03d}.pdf",
                s3_bucket="sample-bucket",
                filename=f"ページネーション検証ファイル_{i:02d}.pdf",
                file_size=1024 * i,
                content_type="application/pdf",
                group_name="public",
                sync_status="SYNCED",
            )
            session.add(doc)
        session.commit()

    try:
        with patch("app.api.web.knowledge.get_bedrock_agent_client"):
            # 1ページ目 (最新10件)
            resp_p1 = client.get("/knowledge/table?page=1")
            assert resp_p1.status_code == 200
            assert "全 <strong>" in resp_p1.text or "全 15" in resp_p1.text or "件中" in resp_p1.text
            assert "次へ" in resp_p1.text

            # 2ページ目 (残り件数)
            resp_p2 = client.get("/knowledge/table?page=2")
            assert resp_p2.status_code == 200
            assert "前へ" in resp_p2.text
    finally:
        with Session(engine) as session:
            for d in session.exec(select(KnowledgeDocument).where(KnowledgeDocument.id.in_(page_ids))).all():
                session.delete(d)
            session.commit()


def test_knowledge_ui_icons_and_collapse():
    """
    ナレッジ画面のUI検証:
    - 人型絵文字 (👤) がテーブル表示に含まれないこと
    - Bootstrap Icons (bi bi-file-earmark-text, bi bi-download) が使われていること
    - 新規ドキュメントアップロードカードが初期折りたたみ (collapse) になっていること
    """
    setup_test_users()
    client = TestClient(app)

    client.post(
        "/login",
        data={"username": "admin_page_test", "password": "pass123"},
        follow_redirects=False,
    )

    with Session(engine) as session:
        if not session.get(KnowledgeDocument, "doc-icon-test-001"):
            session.add(
                KnowledgeDocument(
                    id="doc-icon-test-001",
                    s3_key="public/icon_test.pdf",
                    s3_bucket="sample-bucket",
                    filename="アイコン検証.pdf",
                    file_size=1024,
                    content_type="application/pdf",
                    group_name="public",
                    sync_status="SYNCED",
                )
            )
            session.commit()

    try:
        with patch("app.api.web.knowledge.get_bedrock_agent_client"):
            # メインページ (/knowledge)
            resp = client.get("/knowledge")
            assert resp.status_code == 200
            html = resp.text

            # 1. アップロードカードの Collapse 検証 (初期状態は show クラスなし)
            assert 'id="uploadCollapse"' in html
            assert 'class="collapse mb-4"' in html
            assert 'id="btn-toggle-upload"' in html
            assert '新規ドキュメントをアップロード' in html

            # 2. 検索・フィルターバーの検証
            assert 'id="knowledge-filter-form"' in html
            assert 'name="q"' in html
            assert 'name="group"' in html
            assert 'name="status"' in html
            assert '検索' in html

            # 3. Bootstrap Icons の検証
            assert "bi bi-file-earmark-text" in html or "bi-file-earmark-text" in html
            assert "bi bi-download" in html or "bi-download" in html
            assert "bi bi-cloud-arrow-up" in html

            # 4. 人型絵文字 👤 がテーブル内に含まれていないこと
            assert "👤" not in html
    finally:
        with Session(engine) as session:
            doc = session.get(KnowledgeDocument, "doc-icon-test-001")
            if doc:
                session.delete(doc)
                session.commit()


