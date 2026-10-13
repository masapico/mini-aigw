import base64
import logging
from unittest.mock import MagicMock
import pytest
from app.core.config import settings
from app.services.bedrock_service import (
    call_converse,
    call_converse_stream,
    convert_openai_messages_to_bedrock,
)


@pytest.fixture
def mock_bedrock_client():
    client = MagicMock()
    client.converse.return_value = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [{"text": "This is a test response from Claude."}],
            }
        },
        "stopReason": "end_turn",
        "usage": {
            "inputTokens": 20,
            "outputTokens": 10,
            "totalTokens": 30,
        },
    }
    client.converse_stream.return_value = {
        "stream": [
            {"messageStart": {"role": "assistant"}},
            {"contentBlockDelta": {"delta": {"text": "Hello "}}},
            {"contentBlockDelta": {"delta": {"text": "world!"}}},
            {"contentBlockStop": {}},
            {"messageStop": {"stopReason": "end_turn"}},
        ]
    }
    return client


def test_debug_logging_enabled_non_streaming(mock_bedrock_client, monkeypatch, caplog):
    """DEBUG=True 時に非ストリーミング呼び出しのリクエストと回答がログ出力されることを検証"""
    monkeypatch.setattr(settings, "DEBUG", True)
    caplog.set_level(logging.INFO)

    messages = [
        {"role": "system", "content": "You are a test assistant."},
        {"role": "user", "content": "What is the capital of Japan?"},
    ]

    result = call_converse(
        client=mock_bedrock_client,
        model_id="anthropic.claude-3-5-sonnet-20241022-v2:0",
        messages=messages,
        temperature=0.7,
        max_tokens=100,
    )

    assert result["choices"][0]["message"]["content"] == "This is a test response from Claude."

    log_output = caplog.text
    assert "[DEBUG] AI Request" in log_output
    assert "Model: anthropic.claude-3-5-sonnet-20241022-v2:0" in log_output
    assert "You are a test assistant." in log_output
    assert "What is the capital of Japan?" in log_output
    assert "[DEBUG] AI Response" in log_output
    assert "This is a test response from Claude." in log_output
    assert "Usage Tokens: Input=20, Output=10, Total=30" in log_output


@pytest.mark.asyncio
async def test_debug_logging_enabled_streaming(mock_bedrock_client, monkeypatch, caplog):
    """DEBUG=True 時にストリーミング呼び出しのリクエストと集約回答がログ出力されることを検証"""
    monkeypatch.setattr(settings, "DEBUG", True)
    caplog.set_level(logging.INFO)

    messages = [
        {"role": "user", "content": "Tell me a stream story"},
    ]

    stream_gen = call_converse_stream(
        client=mock_bedrock_client,
        model_id="anthropic.claude-3-5-sonnet-20241022-v2:0",
        messages=messages,
    )

    chunks = []
    async for chunk in stream_gen:
        chunks.append(chunk)

    assert len(chunks) > 0

    log_output = caplog.text
    assert "[DEBUG] AI Request" in log_output
    assert "Tell me a stream story" in log_output
    assert "[DEBUG] AI Response (Stream Completed)" in log_output
    assert "Hello world!" in log_output


def test_debug_logging_disabled(mock_bedrock_client, monkeypatch, caplog):
    """DEBUG=False 時にデバッグログが出力されないことを検証"""
    monkeypatch.setattr(settings, "DEBUG", False)
    caplog.set_level(logging.INFO)

    messages = [{"role": "user", "content": "Quiet request"}]

    call_converse(
        client=mock_bedrock_client,
        model_id="anthropic.claude-3-5-sonnet-20241022-v2:0",
        messages=messages,
    )

    log_output = caplog.text
    assert "[DEBUG] AI Request" not in log_output
    assert "[DEBUG] AI Response" not in log_output


def test_debug_logging_attachment_filename_only(mock_bedrock_client, monkeypatch, caplog):
    """添付ファイルがある場合、ファイル名のみが出力され長文テキストが出力されないことを検証"""
    monkeypatch.setattr(settings, "DEBUG", True)
    caplog.set_level(logging.INFO)

    dummy_doc_b64 = base64.b64encode(b"Confidential internal detailed report content...").decode("utf-8")
    openai_messages = [
        {
            "role": "user",
            "content": [
                {"type": "text", "text": "Please summarize this document:"},
                {
                    "type": "file",
                    "name": "financial_report_2026.pdf",
                    "data": dummy_doc_b64,
                },
            ],
        }
    ]

    call_converse(
        client=mock_bedrock_client,
        model_id="anthropic.claude-3-5-sonnet-20241022-v2:0",
        messages=openai_messages,
    )

    log_output = caplog.text
    assert "[Attached Document: financial_report_2026.pdf]" in log_output
    # 本文テキストはログに含まれていないことを確認
    assert "Confidential internal detailed report content" not in log_output


def test_debug_logging_image_sanitized(mock_bedrock_client, monkeypatch, caplog):
    """画像添付がある場合、生バイナリが出力されず要約形式で出力されることを検証"""
    monkeypatch.setattr(settings, "DEBUG", True)
    caplog.set_level(logging.INFO)

    dummy_png_bytes = b"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
    dummy_image_b64 = base64.b64encode(dummy_png_bytes).decode("utf-8")
    openai_messages = [
        {
            "role": "user",
            "content": [
                {"type": "text", "text": "What is in this image?"},
                {
                    "type": "image_url",
                    "image_url": {"url": f"data:image/png;base64,{dummy_image_b64}"},
                },
            ],
        }
    ]

    call_converse(
        client=mock_bedrock_client,
        model_id="anthropic.claude-3-5-sonnet-20241022-v2:0",
        messages=openai_messages,
    )

    log_output = caplog.text
    assert "[Attached Image: format=png, size=" in log_output
    assert str(dummy_png_bytes) not in log_output

