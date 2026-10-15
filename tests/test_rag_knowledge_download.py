import io
import time
from unittest.mock import MagicMock, patch
from fastapi.testclient import TestClient
from sqlmodel import Session, select
from app.core.database import engine, init_db
from app.core.security import generate_api_key, generate_download_token, verify_download_token
from app.main import app
from app.models.api_key import ApiKey
from app.models.knowledge import KnowledgeDocument


def test_download_token_generation_and_verification():
    """HMAC署名付きダウンロードトークンの生成、検証、期限切れ、改ざん耐性のテスト"""
    doc_id = "test-doc-12345"

    # 1. 正常なトークン
    token = generate_download_token(doc_id, expires_in_seconds=3600)
    assert verify_download_token(doc_id, token) is True

    # 2. 異なる doc_id に対する検証は失敗
    assert verify_download_token("other-doc-67890", token) is False

    # 3. 期限切れトークン
    expired_token = generate_download_token(doc_id, expires_in_seconds=-10)
    assert verify_download_token(doc_id, expired_token) is False

    # 4. 改ざんされたトークン (署名改ざん)
    parts = token.split(".", 1)
    tampered_token = f"{parts[0]}.badsignature123"
    assert verify_download_token(doc_id, tampered_token) is False

    # 5. 不正な形式のトークン
    assert verify_download_token(doc_id, "invalid_token_without_period") is False


def test_v1_knowledge_download_endpoint():
    """GET /v1/knowledge/download/{doc_id} エンドポイントのトークン認証・APIキー認証・アクセス制御テスト"""
    init_db()

    # テスト用ドキュメント作成
    doc_id = "doc-sales-policy-001"
    with Session(engine) as session:
        existing = session.get(KnowledgeDocument, doc_id)
        if not existing:
            doc = KnowledgeDocument(
                id=doc_id,
                s3_key="groups/sales/sales_policy.pdf",
                s3_bucket="sample-knowledge-bucket",
                filename="営業ポリシー.pdf",
                file_size=1024,
                content_type="application/pdf",
                group_name="sales",
                sync_status="SYNCED",
            )
            session.add(doc)
            session.commit()

    # テスト用 API キー作成 (sales グループ許可)
    raw_key_sales, key_hash_sales, prefix_sales = generate_api_key()
    # テスト用 API キー作成 (dev グループのみ許可)
    raw_key_dev, key_hash_dev, prefix_dev = generate_api_key()

    with Session(engine) as session:
        session.add(
            ApiKey(
                name="Sales Key",
                key_hash=key_hash_sales,
                prefix=prefix_sales,
                allowed_groups='["sales"]',
            )
        )
        session.add(
            ApiKey(
                name="Dev Key",
                key_hash=key_hash_dev,
                prefix=prefix_dev,
                allowed_groups='["dev"]',
            )
        )
        session.commit()

    client = TestClient(app)

    # モック S3 クライアント
    fake_body = io.BytesIO(b"Fake PDF Content for sales policy")
    mock_s3_response = {
        "Body": fake_body,
        "ContentType": "application/pdf",
        "ContentLength": len(b"Fake PDF Content for sales policy"),
    }

    with patch("app.api.v1.knowledge.get_s3_client") as mock_get_s3:
        mock_s3 = MagicMock()
        mock_get_s3.return_value = mock_s3
        mock_s3.get_object.return_value = mock_s3_response

        # 1. 有効な署名付きトークンでダウンロード成功
        token = generate_download_token(doc_id, expires_in_seconds=3600)
        resp1 = client.get(f"/v1/knowledge/download/{doc_id}?token={token}")
        assert resp1.status_code == 200
        assert resp1.content == b"Fake PDF Content for sales policy"
        assert "Content-Disposition" in resp1.headers
        assert "filename*=UTF-8''" in resp1.headers["Content-Disposition"]

        # 2. 期限切れトークンは 403
        fake_body.seek(0)
        expired_token = generate_download_token(doc_id, expires_in_seconds=-10)
        resp2 = client.get(f"/v1/knowledge/download/{doc_id}?token={expired_token}")
        assert resp2.status_code == 403

        # 3. 認証なしは 401
        fake_body.seek(0)
        resp3 = client.get(f"/v1/knowledge/download/{doc_id}")
        assert resp3.status_code == 401

        # 4. Bearer API キー (sales許可) でダウンロード成功
        fake_body.seek(0)
        resp4 = client.get(
            f"/v1/knowledge/download/{doc_id}",
            headers={"Authorization": f"Bearer {raw_key_sales}"},
        )
        assert resp4.status_code == 200
        assert resp4.content == b"Fake PDF Content for sales policy"

        # 5. クエリ api_key (sales許可) でダウンロード成功
        fake_body.seek(0)
        resp5 = client.get(f"/v1/knowledge/download/{doc_id}?api_key={raw_key_sales}")
        assert resp5.status_code == 200

        # 6. グループ権限のない API キー (dev のみ許可) は 403
        resp6 = client.get(
            f"/v1/knowledge/download/{doc_id}",
            headers={"Authorization": f"Bearer {raw_key_dev}"},
        )
        assert resp6.status_code == 403


def test_chat_completions_rag_grounding_and_download_link():
    """
    APIキーでナレッジ検索が実行され、回答根拠の提示指示と mini-aigw ダウンロードリンクが
    プロンプトおよびレスポンスに注入されることの検証
    """
    init_db()

    # テスト用ドキュメント作成
    doc_id = "doc-contract-002"
    with Session(engine) as session:
        existing = session.get(KnowledgeDocument, doc_id)
        if not existing:
            doc = KnowledgeDocument(
                id=doc_id,
                s3_key="groups/sales/standard_contract.pdf",
                s3_bucket="sample-knowledge-bucket",
                filename="標準契約書.pdf",
                file_size=2048,
                content_type="application/pdf",
                group_name="sales",
                sync_status="SYNCED",
            )
            session.add(doc)
            session.commit()

    # API キー作成 (sales 許可, システムプロンプトあり)
    raw_key, key_hash, prefix = generate_api_key()
    with Session(engine) as session:
        session.add(
            ApiKey(
                name="RAG Grounding Test Key",
                key_hash=key_hash,
                prefix=prefix,
                allowed_groups='["sales"]',
                system_prompt="あなたは営業支援AIです。",
            )
        )
        session.commit()

    client = TestClient(app)

    # モック Bedrock Agent Runtime (retrieve)
    mock_retrieval_results = [
        {
            "content": "契約書の第3条により、解約通知は30日前までに行う必要があります。",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/groups/sales/standard_contract.pdf"}},
            "metadata": {
                "document_id": doc_id,
                "filename": "標準契約書.pdf",
                "group": "sales",
            },
            "score": 0.88,
        }
    ]

    # モック Bedrock Runtime (converse): 1 ターン目で tool_use、2 ターン目で最終回答
    mock_tool_use_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "toolUse": {
                            "toolUseId": "tooluse_contract_01",
                            "name": "retrieve_knowledge",
                            "input": {"query": "解約通知 期日"},
                        }
                    }
                ],
            }
        },
        "usage": {"inputTokens": 30, "outputTokens": 15, "totalTokens": 45},
        "stopReason": "tool_use",
    }

    mock_converse_resp = {
        "output": {
            "message": {
                "content": [
                    {
                        "text": "契約の解約通知は30日前までに行う必要があります [1]。\n\n### 根拠資料 (参照ドキュメント)\n- [1] [標準契約書.pdf](http://testserver/v1/knowledge/download/doc-contract-002?token=fake) (sales)"
                    }
                ]
            }
        },
        "usage": {"inputTokens": 50, "outputTokens": 30, "totalTokens": 80},
        "stopReason": "end_turn",
    }

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        mock_retrieve.return_value = mock_retrieval_results
        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.side_effect = [mock_tool_use_resp, mock_converse_resp]

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [
                    {"role": "user", "content": "解約通知は何日前に必要ですか？"}
                ]
            },
        )

        assert resp.status_code == 200
        resp_json = resp.json()

        # 1. Turn 1 で LLM が組み立てたクエリで retrieve が呼ばれていること
        assert mock_retrieve.called
        retrieve_kwargs = mock_retrieve.call_args[1]
        assert retrieve_kwargs["query"] == "解約通知 期日"
        assert retrieve_kwargs["allowed_groups"] == ["sales"]

        # 2. Turn 2 の converse 呼び出しに toolResult が含まれ、根拠指示とナレッジが渡されていること
        assert mock_runtime.converse.call_count == 2
        turn2_call_args = mock_runtime.converse.call_args_list[1][1]
        turn2_messages = turn2_call_args["messages"]

        # 最後のメッセージは user の toolResult であること
        last_msg = turn2_messages[-1]
        assert last_msg["role"] == "user"
        tool_result_block = last_msg["content"][0]["toolResult"]
        assert tool_result_block["toolUseId"] == "tooluse_contract_01"
        result_text = tool_result_block["content"][0]["text"]

        assert "標準契約書.pdf" in result_text
        assert f"/v1/knowledge/download/{doc_id}?token=" in result_text
        assert "契約書の第3条により、解約通知は30日前までに" in result_text
        assert "【回答方針・根拠提示ルール】" in result_text
        assert "### 根拠資料 (参照ドキュメント)" in result_text

        # 3. レスポンス JSON に citations メタデータが付与されていること
        assert "citations" in resp_json
        assert len(resp_json["citations"]) == 1
        cite = resp_json["citations"][0]
        assert cite["index"] == 1
        assert cite["filename"] == "標準契約書.pdf"
        assert cite["group"] == "sales"
        assert cite["document_id"] == doc_id
        assert f"/v1/knowledge/download/{doc_id}?token=" in cite["download_url"]
        assert cite["score"] == 0.88

        # 4. トークン使用量が合算されていること (Turn 1: 45 + Turn 2: 80 = 125)
        assert resp_json["usage"]["prompt_tokens"] == 80
        assert resp_json["usage"]["completion_tokens"] == 45
        assert resp_json["usage"]["total_tokens"] == 125


def test_chat_completions_llm_only_key_does_not_call_rag():
    """ナレッジグループ未選択（allowed_groups=[]）の LLM 専用キーでは RAG 検索が実行されないことの検証"""
    init_db()

    raw_key, key_hash, prefix = generate_api_key()
    with Session(engine) as session:
        session.add(
            ApiKey(
                name="Pure LLM Key",
                key_hash=key_hash,
                prefix=prefix,
                allowed_groups="[]",
            )
        )
        session.commit()

    client = TestClient(app)

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.return_value = {
            "output": {"message": {"content": [{"text": "汎用知識からの回答です。"}]}},
            "usage": {"inputTokens": 10, "outputTokens": 10, "totalTokens": 20},
            "stopReason": "end_turn",
        }

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [{"role": "user", "content": "こんにちは"}]
            },
        )

        assert resp.status_code == 200
        # retrieve は呼ばれていないこと
        assert not mock_retrieve.called
        # citations は空（未付与）であること
        assert "citations" not in resp.json()


def test_chat_completions_streaming_rag():
    """ストリーミング呼び出し (stream=True) では現時点で直接ストリーミング配信されることの検証"""
    init_db()

    raw_key, key_hash, prefix = generate_api_key()
    with Session(engine) as session:
        session.add(
            ApiKey(
                name="Stream Key",
                key_hash=key_hash,
                prefix=prefix,
                allowed_groups='["public"]',
                system_prompt="あなたはストリーミングボットです。",
            )
        )
        session.commit()

    client = TestClient(app)

    async def fake_stream_generator(*args, **kwargs):
        yield "data: {\"choices\": [{\"delta\": {\"content\": \"回答\"}}]}\n\n"
        yield "data: [DONE]\n\n"

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.call_converse_stream") as mock_converse_stream:

        mock_converse_stream.return_value = fake_stream_generator()

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [{"role": "user", "content": "ストリーミングテスト"}],
                "stream": True,
            },
        )

        assert resp.status_code == 200
        # 現状ストリーミングでは RAG 検索は行われず直接 converse_stream が呼ばれること
        assert not mock_retrieve.called
        assert mock_converse_stream.called
        stream_kwargs = mock_converse_stream.call_args[1]
        messages = stream_kwargs["messages"]
        system_msg = next((m for m in messages if m["role"] == "system"), None)
        assert system_msg is not None
        assert "あなたはストリーミングボットです。" in system_msg["content"]


def test_rag_test_web_chat_download_link():
    """Web RAG テスト (/rag-test/chat) の引用元チップに mini-aigw ダウンロードリンクが含まれることの検証"""
    init_db()
    with Session(engine) as session:
        from app.models.user import User
        from app.core.security import hash_password
        admin_user = session.exec(select(User).where(User.username == "admin_rag_test")).first()
        if not admin_user:
            admin_user = User(
                auth_type="local",
                username="admin_rag_test",
                password_hash=hash_password("admin123"),
                roles='["admin"]',
            )
            session.add(admin_user)
            session.commit()

    client = TestClient(app)
    login_resp = client.post(
        "/login",
        data={"username": "admin_rag_test", "password": "admin123"},
        follow_redirects=False,
    )
    cookie = login_resp.cookies.get("aigw_session")

    with patch("app.api.web.rag_test.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.web.rag_test.call_converse") as mock_converse:

        mock_retrieve.return_value = [
            {
                "content": "テストナレッジ本文",
                "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/test_manual.pdf"}},
                "metadata": {"document_id": "doc-manual-999", "filename": "test_manual.pdf", "group": "public"},
                "score": 0.9,
            }
        ]
        mock_converse.return_value = {
            "choices": [{"message": {"content": "回答本文です [1]"}}],
        }

        resp = client.post(
            "/rag-test/chat",
            cookies={"aigw_session": cookie},
            data={"query": "テスト手順", "group": "public", "model_id": "amazon.nova-lite-v1:0"},
        )

        assert resp.status_code == 200
        # 引用元チップに mini-aigw ダウンロードリンクが含まれていること
        assert "test_manual.pdf" in resp.text
        assert "/v1/knowledge/download/doc-manual-999?token=" in resp.text
        assert "📥 DL" in resp.text

