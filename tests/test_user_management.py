import json
from fastapi.testclient import TestClient
from sqlmodel import Session, select
from app.core.database import engine, init_db
from app.core.security import hash_password, verify_password
from app.main import app
from app.models.group import Group
from app.models.user import User


def setup_users():
    init_db()
    with Session(engine) as session:
        admin = session.exec(select(User).where(User.username == "admin")).first()
        if not admin:
            admin = User(
                auth_type="local",
                username="admin",
                password_hash=hash_password("admin123"),
                roles='["admin"]',
                groups='["sales", "dev"]',
            )
            session.add(admin)
            session.commit()
            session.refresh(admin)

        user = session.exec(select(User).where(User.username == "regular_user")).first()
        if not user:
            user = User(
                auth_type="local",
                username="regular_user",
                password_hash=hash_password("userpass123"),
                roles='["user"]',
                groups='["sales"]',
            )
            session.add(user)
            session.commit()
            session.refresh(user)


def test_user_management_access_control():
    """管理者のみが /users にアクセスでき、一般ユーザーは 403 になることの検証"""
    setup_users()
    client = TestClient(app)

    # 1. 未ログイン時 -> リダイレクト
    resp = client.get("/users", follow_redirects=False)
    assert resp.status_code in (302, 307)

    # 2. 一般ユーザーでログイン -> 403 Forbidden
    login_user = client.post(
        "/login",
        data={"username": "regular_user", "password": "userpass123"},
        follow_redirects=False,
    )
    assert login_user.status_code == 302
    resp_user = client.get("/users")
    assert resp_user.status_code == 403

    # 3. 管理者でログイン -> 200 OK
    client_admin = TestClient(app)
    client_admin.post(
        "/login",
        data={"username": "admin", "password": "admin123"},
        follow_redirects=False,
    )
    resp_admin = client_admin.get("/users")
    assert resp_admin.status_code == 200
    assert "ユーザー & グループ管理" in resp_admin.text


def test_create_edit_delete_user():
    """管理者がローカルユーザーを新規作成、編集、削除できることの検証"""
    setup_users()
    client = TestClient(app)
    client.post(
        "/login",
        data={"username": "admin", "password": "admin123"},
        follow_redirects=False,
    )

    # 1. 新規ユーザー作成
    create_resp = client.post(
        "/users/create",
        data={
            "username": "tanaka_sales",
            "password": "Password123!",
            "display_name": "田中 営業",
            "email": "tanaka@example.com",
            "role": "user",
            "selected_groups": ["sales"],
        },
        headers={"HX-Request": "true"},
    )
    assert create_resp.status_code == 200
    assert "tanaka_sales" in create_resp.text
    assert "田中 営業" in create_resp.text

    # DB で確認
    with Session(engine) as session:
        created = session.exec(select(User).where(User.username == "tanaka_sales")).first()
        assert created is not None
        assert verify_password("Password123!", created.password_hash) is True
        assert created.get_roles() == ["user"]
        assert created.get_groups() == ["sales"]
        created_id = created.id

    # 2. ユーザー編集（表示名変更、グループ追加）
    edit_resp = client.post(
        f"/users/{created_id}/edit",
        data={
            "display_name": "田中 マネージャー",
            "email": "tanaka_mgr@example.com",
            "password": "",
            "role": "user",
            "selected_groups": ["sales", "dev"],
        },
        headers={"HX-Request": "true"},
    )
    assert edit_resp.status_code == 200
    assert "田中 マネージャー" in edit_resp.text

    with Session(engine) as session:
        updated = session.get(User, created_id)
        assert updated.display_name == "田中 マネージャー"
        assert set(updated.get_groups()) == {"sales", "dev"}
        # パスワードは未入力なので変更されていないこと
        assert verify_password("Password123!", updated.password_hash) is True

    # 3. 自分自身の削除防止
    with Session(engine) as session:
        admin_obj = session.exec(select(User).where(User.username == "admin")).first()
        admin_id = admin_obj.id

    self_del_resp = client.delete(f"/users/{admin_id}")
    assert self_del_resp.status_code == 400
    assert "自身" in self_del_resp.text

    # 4. 作成したユーザーの削除
    del_resp = client.delete(f"/users/{created_id}")
    assert del_resp.status_code == 200

    with Session(engine) as session:
        assert session.get(User, created_id) is None


def test_create_edit_and_delete_group():
    """グループの追加、編集（プレフィックス不変確認）、削除、予約語チェック、権限検証"""
    setup_users()
    client = TestClient(app)
    client.post(
        "/login",
        data={"username": "admin", "password": "admin123"},
        follow_redirects=False,
    )

    # 1. 'public' の作成試行 -> 400
    bad_resp = client.post(
        "/groups/create",
        data={"name": "public", "display_name": "Public Group"},
    )
    assert bad_resp.status_code == 400

    # 2. 新規グループ 'marketing' 作成
    create_resp = client.post(
        "/groups/create",
        data={
            "name": "marketing",
            "display_name": "マーケティング部",
            "description": "マーケティング部門用ナレッジ",
        },
        headers={"HX-Request": "true"},
    )
    assert create_resp.status_code == 200
    assert "marketing" in create_resp.text
    assert "マーケティング部" in create_resp.text

    with Session(engine) as session:
        grp = session.exec(select(Group).where(Group.name == "marketing")).first()
        assert grp is not None
        grp_id = grp.id

    # 3. グループ編集のバリデーション (表示名が空 -> 400)
    bad_edit = client.post(
        f"/groups/{grp_id}/edit",
        data={"display_name": "   ", "description": "テスト"},
        headers={"HX-Request": "true"},
    )
    assert bad_edit.status_code == 400
    assert "表示名を入力してください" in bad_edit.text

    # 4. グループ編集の正常系 (表示名・説明の更新 & プレフィックス不変の検証)
    edit_resp = client.post(
        f"/groups/{grp_id}/edit",
        data={
            "display_name": "マーケティング部 (Global)",
            "description": "全社向けマーケティングナレッジ",
        },
        headers={"HX-Request": "true"},
    )
    assert edit_resp.status_code == 200
    assert "マーケティング部 (Global)" in edit_resp.text
    assert "全社向けマーケティングナレッジ" in edit_resp.text

    with Session(engine) as session:
        updated_grp = session.get(Group, grp_id)
        assert updated_grp is not None
        assert updated_grp.display_name == "マーケティング部 (Global)"
        assert updated_grp.description == "全社向けマーケティングナレッジ"
        # プレフィックス(name)は marketing のまま変化していないこと
        assert updated_grp.name == "marketing"

    # 5. 一般ユーザーによる編集試行 -> 403 Forbidden
    client_user = TestClient(app)
    client_user.post(
        "/login",
        data={"username": "regular_user", "password": "userpass123"},
        follow_redirects=False,
    )
    forbidden_edit = client_user.post(
        f"/groups/{grp_id}/edit",
        data={"display_name": "不正更新"},
    )
    assert forbidden_edit.status_code == 403

    # 6. グループ削除
    del_resp = client.delete(f"/groups/{grp_id}")
    assert del_resp.status_code == 200

    with Session(engine) as session:
        assert session.get(Group, grp_id) is None

