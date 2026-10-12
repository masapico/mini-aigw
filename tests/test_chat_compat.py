import base64
import json
from unittest.mock import MagicMock, patch
from openai import OpenAI
from fastapi.testclient import TestClient
from sqlmodel import Session
from app.core.database import engine, init_db
from app.core.security import generate_api_key
from app.main import app
from app.models.api_key import ApiKey
from app.services.bedrock_service import convert_openai_messages_to_bedrock


def setup_test_api_key() -> str:
    """テスト用 API キーを作成し、未ハッシュの raw キーを返す"""
    init_db()
    raw_key, key_hash, prefix = generate_api_key()
    with Session(engine) as session:
        key_obj = ApiKey(
            name="Test Bot Key",
            key_hash=key_hash,
            prefix=prefix,
            allowed_groups='["public", "sales"]',
        )
        session.add(key_obj)
        session.commit()
    return raw_key


def test_list_models():
    """GET /v1/models の検証"""
    client = TestClient(app)
    resp = client.get("/v1/models")
    assert resp.status_code == 200
    data = resp.json()
    assert data["object"] == "list"
    model_ids = [m["id"] for m in data["data"]]
    assert "anthropic.claude-3-5-sonnet-20241022-v2:0" in model_ids


def test_chat_unauthorized():
    """認証ヘッダーなしの場合は 401 を返却することの検証"""
    client = TestClient(app)
    resp = client.post(
        "/v1/chat/completions",
        json={"messages": [{"role": "user", "content": "hello"}]},
    )
    assert resp.status_code == 401


@patch("app.api.v1.chat.get_bedrock_runtime_client")
def test_openai_sdk_non_streaming_chat(mock_get_client):
    """公式 openai SDK クライアントを使用した非ストリーミングチャット補完の検証"""
    api_key = setup_test_api_key()

    mock_client = MagicMock()
    mock_get_client.return_value = mock_client
    mock_client.converse.return_value = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [{"text": "Hello! I am Claude on Amazon Bedrock."}],
            }
        },
        "stopReason": "end_turn",
        "usage": {
            "inputTokens": 15,
            "outputTokens": 25,
            "totalTokens": 40,
        },
    }

    test_client = TestClient(app, base_url="http://testserver")
    client = OpenAI(
        base_url="http://testserver/v1",
        api_key=api_key,
        http_client=test_client,
    )

    response = client.chat.completions.create(
        model="anthropic.claude-3-5-sonnet-20241022-v2:0",
        messages=[
            {"role": "system", "content": "You are a helpful assistant."},
            {"role": "user", "content": "Hello Bedrock!"},
        ],
        temperature=0.7,
        max_tokens=100,
    )

    assert response.id.startswith("chatcmpl-")
    assert response.choices[0].message.role == "assistant"
    assert response.choices[0].message.content == "Hello! I am Claude on Amazon Bedrock."
    assert response.usage.total_tokens == 40


@patch("app.api.v1.chat.get_bedrock_runtime_client")
def test_openai_sdk_streaming_chat(mock_get_client):
    """公式 openai SDK クライアントを使用したストリーミングチャット補完の検証"""
    api_key = setup_test_api_key()

    mock_client = MagicMock()
    mock_get_client.return_value = mock_client
    # ConverseStream のイベントストリームモック
    mock_client.converse_stream.return_value = {
        "stream": [
            {"messageStart": {"role": "assistant"}},
            {"contentBlockDelta": {"delta": {"text": "Streamed "}}},
            {"contentBlockDelta": {"delta": {"text": "Response"}}},
            {"contentBlockStop": {}},
            {"messageStop": {"stopReason": "end_turn"}},
        ]
    }

    test_client = TestClient(app, base_url="http://testserver")
    client = OpenAI(
        base_url="http://testserver/v1",
        api_key=api_key,
        http_client=test_client,
    )

    stream = client.chat.completions.create(
        model="anthropic.claude-3-5-sonnet-20241022-v2:0",
        messages=[{"role": "user", "content": "Stream to me"}],
        stream=True,
    )

    chunks = []
    for chunk in stream:
        delta = chunk.choices[0].delta.content or ""
        chunks.append(delta)

    full_text = "".join(chunks)
    assert full_text == "Streamed Response"


def test_convert_openai_messages_with_multimodal_and_documents():
    """画像 Base64 および添付ファイル Base64 の Bedrock 形式への変換検証"""
    dummy_image = base64.b64encode(b"\x89PNG\r\n\x1a\n\x00\x00").decode("utf-8")
    doc_text = "Internal guideline for operations"
    dummy_doc_b64 = base64.b64encode(doc_text.encode("utf-8")).decode("utf-8")

    openai_messages = [
        {"role": "system", "content": "You are a test evaluator."},
        {
            "role": "user",
            "content": [
                {"type": "text", "text": "Please analyze this image and document:"},
                {
                    "type": "image_url",
                    "image_url": {"url": f"data:image/png;base64,{dummy_image}"},
                },
                {
                    "type": "file",
                    "name": "guideline.txt",
                    "data": dummy_doc_b64,
                },
            ],
        },
    ]

    systems, converse_messages = convert_openai_messages_to_bedrock(openai_messages)

    assert len(systems) == 1
    assert systems[0]["text"] == "You are a test evaluator."

    assert len(converse_messages) == 1
    user_msg = converse_messages[0]
    assert user_msg["role"] == "user"

    blocks = user_msg["content"]
    assert len(blocks) == 3

    # 1. Text block
    assert blocks[0]["text"] == "Please analyze this image and document:"

    # 2. Image block
    assert "image" in blocks[1]
    assert blocks[1]["image"]["format"] == "png"
    assert blocks[1]["image"]["source"]["bytes"] == b"\x89PNG\r\n\x1a\n\x00\x00"

    # 3. Document block (normalized via markitdown)
    assert "text" in blocks[2]
    assert "guideline.txt" in blocks[2]["text"]
    assert "Internal guideline for operations" in blocks[2]["text"]
