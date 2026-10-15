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
from app.services.bedrock_service import (
    build_inference_config,
    convert_openai_messages_to_bedrock,
)


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
    assert len(model_ids) > 0



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


def test_build_inference_config_logic():
    """推論パラメータのサニタイズ・クランプ・排他制御ロジックの検証"""
    # 1. 未指定時のデフォルト maxTokens
    cfg1 = build_inference_config()
    assert cfg1["maxTokens"] == 4096
    assert "temperature" not in cfg1
    assert "topP" not in cfg1

    # 2. temperature の 0.0〜1.0 クランプ (OpenAI の 1.5 や負の値)
    cfg2 = build_inference_config(temperature=1.8, max_tokens=20000)
    assert cfg2["temperature"] == 1.0
    assert cfg2["maxTokens"] == 8192  # MAX_ALLOWED_TOKENS にキャップ

    cfg3 = build_inference_config(temperature=-0.5, max_tokens=-5)
    assert cfg3["temperature"] == 0.0
    assert cfg3["maxTokens"] == 1  # 最小値 1

    # 3. top_p 単体指定
    cfg4 = build_inference_config(top_p=0.85)
    assert cfg4["topP"] == 0.85
    assert "temperature" not in cfg4

    # 4. 排他制御: top_p=1.0 (デフォルト) と temperature=0.7 の場合、temperature を優先
    cfg5 = build_inference_config(temperature=0.7, top_p=1.0)
    assert cfg5["temperature"] == 0.7
    assert "topP" not in cfg5

    # 5. 排他制御: temperature=1.0 (デフォルト) と top_p=0.9 の場合、topP を優先
    cfg6 = build_inference_config(temperature=1.0, top_p=0.9)
    assert cfg6["topP"] == 0.9
    assert "temperature" not in cfg6

    # 6. 排他制御: 両方が非デフォルトで指定された場合、temperature を優先
    cfg7 = build_inference_config(temperature=0.6, top_p=0.8)
    assert cfg7["temperature"] == 0.6
    assert "topP" not in cfg7


@patch("app.api.v1.chat.get_bedrock_runtime_client")
def test_openai_sdk_inference_config_clamping_and_forwarding(mock_get_client):
    """OpenAI クライアントからの過大 temperature / max_tokens が安全にクランプされて Bedrock に渡されることの検証"""
    api_key = setup_test_api_key()

    mock_client = MagicMock()
    mock_get_client.return_value = mock_client
    mock_client.converse.return_value = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [{"text": "Deterministic response"}],
            }
        },
        "stopReason": "end_turn",
        "usage": {"inputTokens": 10, "outputTokens": 20, "totalTokens": 30},
    }

    test_client = TestClient(app, base_url="http://testserver")
    client = OpenAI(
        base_url="http://testserver/v1",
        api_key=api_key,
        http_client=test_client,
    )

    # temperature=1.8 (OpenAIでは許容、Bedrockでは不可), top_p=0.9, max_tokens=50000 (上限超え)
    response = client.chat.completions.create(
        model="anthropic.claude-3-5-sonnet-20241022-v2:0",
        messages=[{"role": "user", "content": "Test clamping"}],
        temperature=1.8,
        top_p=0.9,
        max_tokens=50000,
    )

    assert response.choices[0].message.content == "Deterministic response"

    # Bedrock converse への実際の引数を検証
    mock_client.converse.assert_called_once()
    called_kwargs = mock_client.converse.call_args.kwargs
    assert "inferenceConfig" in called_kwargs
    inf_cfg = called_kwargs["inferenceConfig"]

    # temperature は 1.0 にクランプされ、両方指定時は temperature が優先
    assert inf_cfg["temperature"] == 1.0
    assert "topP" not in inf_cfg
    # maxTokens は MAX_ALLOWED_TOKENS (8192) にキャップ
    assert inf_cfg["maxTokens"] == 8192


@patch("app.api.v1.chat.get_bedrock_runtime_client")
def test_openai_sdk_top_p_streaming_forwarding(mock_get_client):
    """ストリーミング呼び出しで top_p が正しく Bedrock ConverseStream に渡されることの検証"""
    api_key = setup_test_api_key()

    mock_client = MagicMock()
    mock_get_client.return_value = mock_client
    mock_client.converse_stream.return_value = {
        "stream": [
            {"contentBlockDelta": {"delta": {"text": "TopP Stream"}}},
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
        messages=[{"role": "user", "content": "Stream test"}],
        top_p=0.85,
        stream=True,
    )
    chunks = [c.choices[0].delta.content or "" for c in stream]
    assert "".join(chunks) == "TopP Stream"

    mock_client.converse_stream.assert_called_once()
    called_kwargs = mock_client.converse_stream.call_args.kwargs
    inf_cfg = called_kwargs["inferenceConfig"]
    assert inf_cfg["topP"] == 0.85
    assert "temperature" not in inf_cfg

