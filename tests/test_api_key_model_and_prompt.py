import json
from unittest.mock import MagicMock, patch
from fastapi.testclient import TestClient
from sqlmodel import Session, select
from app.core.database import engine, init_db
from app.core.security import generate_api_key, hash_password
from app.main import app
from app.models.api_key import ApiKey
from app.models.llm_model import LlmModel
from app.models.user import User


def setup_api_key_with_config(model_id: str | None = None, system_prompt: str | None = None) -> str:
    """モデルIDとシステムプロンプトを指定したテスト用APIキーを作成する"""
    init_db()
    raw_key, key_hash, prefix = generate_api_key()
    with Session(engine) as session:
        key_obj = ApiKey(
            name="Configured Test Key",
            key_hash=key_hash,
            prefix=prefix,
            allowed_groups='["public"]',
            model_id=model_id,
            system_prompt=system_prompt,
        )
        session.add(key_obj)
        session.commit()
    return raw_key


def test_api_key_model_override():
    """APIキーにモデルが指定されている場合、リクエストの指定に関わらずキーのモデルが優先されることの検証"""
    api_key = setup_api_key_with_config(model_id="amazon.nova-lite-v1:0")

    with patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_client:
        mock_client = MagicMock()
        mock_get_client.return_value = mock_client
        mock_client.converse.return_value = {
            "output": {"message": {"content": [{"text": "Hello from Nova Lite"}]}},
            "usage": {"inputTokens": 10, "outputTokens": 5, "totalTokens": 15},
            "stopReason": "end_turn",
        }

        client = TestClient(app)
        # クライアントは claude-3-5-sonnet を要求
        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {api_key}"},
            json={
                "model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
                "messages": [{"role": "user", "content": "Hi"}],
            },
        )
        assert resp.status_code == 200
        # Bedrock converse の呼び出し引数 modelId が APIキー指定の nova-lite になっていること
        called_kwargs = mock_client.converse.call_args[1]
        assert called_kwargs["modelId"] == "amazon.nova-lite-v1:0"
        assert resp.json()["model"] == "amazon.nova-lite-v1:0"


def test_api_key_system_prompt_injection():
    """APIキーにシステムプロンプトが設定されている場合、messagesに基底注入・合成されることの検証"""
    api_key = setup_api_key_with_config(
        system_prompt="あなたは組織内のの業アシスタントです。"
    )

    with patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_client:
        mock_client = MagicMock()
        mock_get_client.return_value = mock_client
        mock_client.converse.return_value = {
            "output": {"message": {"content": [{"text": "はい、承知いたしました。"}]}},
            "usage": {"inputTokens": 15, "outputTokens": 8, "totalTokens": 23},
            "stopReason": "end_turn",
        }

        client = TestClient(app)
        # 1. クライアントが system プロンプトを送っていない場合
        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {api_key}"},
            json={
                "messages": [{"role": "user", "content": "営業資料を教えて"}],
            },
        )
        assert resp.status_code == 200
        called_kwargs = mock_client.converse.call_args[1]
        # Bedrock Converse の system フィールドに APIキーのプロンプトが設定されていること
        assert "system" in called_kwargs
        assert "あなたは組織内の営業アシスタントです。" in called_kwargs["system"][0]["text"]

        # 2. クライアントも system プロンプトを送ってきた場合（合成の検証）
        resp2 = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {api_key}"},
            json={
                "messages": [
                    {"role": "system", "content": "返答は箇条書きでお願いします。"},
                    {"role": "user", "content": "製品価格について"},
                ],
            },
        )
        assert resp2.status_code == 200
        called_kwargs2 = mock_client.converse.call_args[1]
        assert "system" in called_kwargs2
        combined_text = called_kwargs2["system"][0]["text"]
        # APIキーのプロンプトが先頭、その後にクライアントのプロンプトが連結されていること
        assert "あなたは組織内の営業アシスタントです。" in combined_text
        assert "返答は箇条書きでお願いします。" in combined_text
        assert combined_text.index("あなたは組織内の営業アシスタントです。") < combined_text.index("返答は箇条書きでお願いします。")


def test_models_endpoint_dynamic_db():
    """GET /v1/models が DB のアクティブモデルを反映することの検証"""
    init_db()
    test_model_id = "test.custom-model-v1:0"
    with Session(engine) as session:
        # テスト用モデルを追加
        existing = session.get(LlmModel, test_model_id)
        if not existing:
            session.add(
                LlmModel(
                    id=test_model_id,
                    name="Test Custom Model",
                    provider="bedrock",
                    is_active=True,
                )
            )
            session.commit()

    client = TestClient(app)
    resp = client.get("/v1/models")
    assert resp.status_code == 200
    data = resp.json()
    model_ids = [m["id"] for m in data["data"]]
    assert test_model_id in model_ids

    # 非アクティブに更新
    with Session(engine) as session:
        m = session.get(LlmModel, test_model_id)
        if m:
            m.is_active = False
            session.add(m)
            session.commit()

    resp_after = client.get("/v1/models")
    model_ids_after = [m["id"] for m in resp_after.json()["data"]]
    assert test_model_id not in model_ids_after

    # クリーンアップ
    with Session(engine) as session:
        m = session.get(LlmModel, test_model_id)
        if m:
            session.delete(m)
            session.commit()


def test_models_web_management():
    """Web モデル管理画面 (/models) の権限およびCRUD・トグル操作の検証"""
    init_db()
    with Session(engine) as session:
        # admin ユーザー
        admin_user = session.exec(select(User).where(User.username == "admin_test_m")).first()
        if not admin_user:
            admin_user = User(
                auth_type="local",
                username="admin_test_m",
                password_hash=hash_password("pass123"),
                roles='["admin"]',
            )
            session.add(admin_user)
            session.commit()
            session.refresh(admin_user)

        # 一般ユーザー
        normal_user = session.exec(select(User).where(User.username == "normal_test_m")).first()
        if not normal_user:
            normal_user = User(
                auth_type="local",
                username="normal_test_m",
                password_hash=hash_password("pass123"),
                roles='["viewer"]',
            )
            session.add(normal_user)
            session.commit()
            session.refresh(normal_user)

    client = TestClient(app)

    # 1. 未ログインアクセスはログインへリダイレクト
    resp = client.get("/models", follow_redirects=False)
    assert resp.status_code in (302, 303, 307)

    # 2. 一般ユーザーは 403 Forbidden
    login_resp = client.post(
        "/login",
        data={"username": "normal_test_m", "password": "pass123"},
        follow_redirects=False,
    )
    normal_cookie = login_resp.cookies.get("aigw_session")
    resp_normal = client.get("/models", cookies={"aigw_session": normal_cookie})
    assert resp_normal.status_code == 403

    # 3. 管理者ログイン
    login_admin = client.post(
        "/login",
        data={"username": "admin_test_m", "password": "pass123"},
        follow_redirects=False,
    )
    admin_cookie = login_admin.cookies.get("aigw_session")

    # 管理者は一覧を取得できる
    resp_admin = client.get("/models", cookies={"aigw_session": admin_cookie})
    assert resp_admin.status_code == 200
    assert "LLM モデル管理" in resp_admin.text

    # モデル追加
    resp_create = client.post(
        "/models/create",
        cookies={"aigw_session": admin_cookie},
        data={
            "id": "amazon.titan-text-express-v1",
            "name": "Titan Text Express",
            "description": "テスト用モデル",
            "provider": "bedrock",
            "is_active": "true",
            "is_default": "false",
        },
    )
    assert resp_create.status_code == 200
    assert "Titan Text Express" in resp_create.text

    # トグル切り替え
    resp_toggle = client.post(
        "/models/amazon.titan-text-express-v1/toggle",
        cookies={"aigw_session": admin_cookie},
    )
    assert resp_toggle.status_code == 200
    assert "○ 無効" in resp_toggle.text

    # 削除
    resp_del = client.delete(
        "/models/amazon.titan-text-express-v1",
        cookies={"aigw_session": admin_cookie},
    )
    assert resp_del.status_code == 200


def test_api_key_web_create_with_model_and_prompt():
    """Web画面 (/api-keys/create) からモデルとシステムプロンプトを指定して作成するテスト"""
    init_db()
    with Session(engine) as session:
        user = session.exec(select(User).where(User.username == "admin_key_user")).first()
        if not user:
            user = User(
                auth_type="local",
                username="admin_key_user",
                password_hash=hash_password("pass123"),
                roles='["admin"]',
            )
            session.add(user)
            session.commit()
            session.refresh(user)

    client = TestClient(app)
    login_resp = client.post(
        "/login",
        data={"username": "admin_key_user", "password": "pass123"},
        follow_redirects=False,
    )
    cookie = login_resp.cookies.get("aigw_session")

    # キー作成
    create_resp = client.post(
        "/api-keys/create",
        cookies={"aigw_session": cookie},
        data={
            "name": "営業特化Botキー",
            "allowed_groups": ["public", "sales"],
            "model_id": "anthropic.claude-3-5-sonnet-20241022-v2:0",
            "system_prompt": "営業資料を元に提案書を作成してください。",
        },
    )
    assert create_resp.status_code == 200
    assert "営業特化Botキー" in create_resp.text
    assert "anthropic.claude-3-5-sonnet-20241022-v2:0" in create_resp.text
    assert "設定あり (基底注入)" in create_resp.text

    # DB に正しく保存されているか確認
    with Session(engine) as session:
        created_key = session.exec(
            select(ApiKey).where(ApiKey.name == "営業特化Botキー")
        ).first()
        assert created_key is not None
        assert created_key.model_id == "anthropic.claude-3-5-sonnet-20241022-v2:0"
        assert created_key.system_prompt == "営業資料を元に提案書を作成してください。"


def test_api_key_web_create_without_allowed_groups():
    """Web画面 (/api-keys/create) でナレッジグループ未選択でも登録でき、LLM専用キーとして保存・表示されるテスト"""
    init_db()
    with Session(engine) as session:
        user = session.exec(select(User).where(User.username == "admin_key_user")).first()
        if not user:
            user = User(
                auth_type="local",
                username="admin_key_user",
                password_hash=hash_password("pass123"),
                roles='["admin"]',
            )
            session.add(user)
            session.commit()
            session.refresh(user)

    client = TestClient(app)
    login_resp = client.post(
        "/login",
        data={"username": "admin_key_user", "password": "pass123"},
        follow_redirects=False,
    )
    cookie = login_resp.cookies.get("aigw_session")

    # allowed_groups を一切送信しない（チェックボックス未選択時ブラウザの挙動）
    create_resp = client.post(
        "/api-keys/create",
        cookies={"aigw_session": cookie},
        data={
            "name": "純粋LLM専用キー",
        },
    )
    assert create_resp.status_code == 200
    assert "純粋LLM専用キー" in create_resp.text
    assert "なし (LLM専用)" in create_resp.text

    # DB に正しく空リストとして保存されているか確認
    with Session(engine) as session:
        created_key = session.exec(
            select(ApiKey).where(ApiKey.name == "純粋LLM専用キー")
        ).first()
        assert created_key is not None
        assert created_key.allowed_groups == "[]"
        assert created_key.get_allowed_groups() == []


def test_api_key_web_edit_system_prompt():
    """Web画面 (/api-keys/{key_id}/edit) からシステムプロンプトやモデル、グループを後から修正できることの検証"""
    init_db()
    with Session(engine) as session:
        user = session.exec(select(User).where(User.username == "admin_key_user")).first()
        if not user:
            user = User(
                auth_type="local",
                username="admin_key_user",
                password_hash=hash_password("pass123"),
                roles='["admin"]',
            )
            session.add(user)
            session.commit()
            session.refresh(user)

    client = TestClient(app)
    login_resp = client.post(
        "/login",
        data={"username": "admin_key_user", "password": "pass123"},
        follow_redirects=False,
    )
    cookie = login_resp.cookies.get("aigw_session")

    # 1. キーを発行
    create_resp = client.post(
        "/api-keys/create",
        cookies={"aigw_session": cookie},
        data={
            "name": "初期キー",
            "allowed_groups": ["public"],
            "model_id": "",
            "system_prompt": "初期プロンプト",
        },
    )
    assert create_resp.status_code == 200

    with Session(engine) as session:
        key_obj = session.exec(select(ApiKey).where(ApiKey.name == "初期キー")).first()
        assert key_obj is not None
        assert key_obj.raw_key is not None
        assert key_obj.raw_key.startswith("sk-live-")
        key_id = key_obj.id
        raw_key = key_obj.raw_key

    # 2. キーのシステムプロンプト等を編集 (POST /api-keys/{key_id}/edit)
    new_prompt = "参照可能なナレッジ（検索された参考資料・コンテキスト）がある場合は、必ずその内容に基づいて正確・誠実に回答してください。ナレッジに記載がない情報については憶測で回答せず、「参照可能なナレッジに記載がありません」と明記してください。"
    edit_resp = client.post(
        f"/api-keys/{key_id}/edit",
        cookies={"aigw_session": cookie},
        data={
            "name": "更新後キー",
            "allowed_groups": ["public", "sales"],
            "model_id": "amazon.nova-lite-v1:0",
            "system_prompt": new_prompt,
        },
    )
    assert edit_resp.status_code == 200
    assert "更新後キー" in edit_resp.text
    assert "amazon.nova-lite-v1:0" in edit_resp.text
    assert raw_key in edit_resp.text  # シークレットキーが編集モーダルやコピーボタン内に保持されていること

    # 3. DBの更新を確認
    with Session(engine) as session:
        updated_key = session.get(ApiKey, key_id)
        assert updated_key is not None
        assert updated_key.name == "更新後キー"
        assert updated_key.model_id == "amazon.nova-lite-v1:0"
        assert updated_key.system_prompt == new_prompt
        assert updated_key.get_allowed_groups() == ["public", "sales"]
        assert updated_key.raw_key == raw_key  # raw_key は変化せず保持されていること


def test_api_key_web_edit_unauthorized():
    """一般ユーザーによる API キー編集 (/api-keys/{key_id}/edit) が 403 Forbidden となることの検証"""
    init_db()
    with Session(engine) as session:
        user = session.exec(select(User).where(User.username == "normal_key_user")).first()
        if not user:
            user = User(
                auth_type="local",
                username="normal_key_user",
                password_hash=hash_password("pass123"),
                roles='["viewer"]',
            )
            session.add(user)
            session.commit()
            session.refresh(user)

        key_obj = session.exec(select(ApiKey)).first()
        if not key_obj:
            _, key_hash, prefix = generate_api_key()
            key_obj = ApiKey(
                name="既存キー",
                key_hash=key_hash,
                prefix=prefix,
            )
            session.add(key_obj)
            session.commit()
            session.refresh(key_obj)
        test_key_id = key_obj.id

    client = TestClient(app)
    login_resp = client.post(
        "/login",
        data={"username": "normal_key_user", "password": "pass123"},
        follow_redirects=False,
    )
    cookie = login_resp.cookies.get("aigw_session")

    edit_resp = client.post(
        f"/api-keys/{test_key_id}/edit",
        cookies={"aigw_session": cookie},
        data={"name": "不正更新", "system_prompt": "不正プロンプト"},
    )
    assert edit_resp.status_code == 403


