import json
from unittest.mock import MagicMock, patch
from fastapi.testclient import TestClient
from sqlmodel import Session
from app.core.config import settings
from app.core.database import engine, init_db
from app.core.prompts import (
    format_rag_empty_tool_result,
    format_rag_grounding_tool_result,
    get_knowledge_tool_config,
)
from app.core.security import generate_api_key
from app.main import app
from app.models.api_key import ApiKey
from app.services.bedrock_service import filter_knowledge_results_by_score


def setup_knowledge_api_key(name: str = "Knowledge Test Bot", groups: str = '["public", "sales"]') -> str:
    """ナレッジグループが許可されたテスト用 API キーを作成する"""
    init_db()
    raw_key, key_hash, prefix = generate_api_key()
    with Session(engine) as session:
        key_obj = ApiKey(
            name=name,
            key_hash=key_hash,
            prefix=prefix,
            allowed_groups=groups,
            system_prompt="あなたは組織内のアシスタントです。",
        )
        session.add(key_obj)
        session.commit()
    return raw_key


def test_score_filtering_helper():
    """filter_knowledge_results_by_score の単体テスト"""
    raw_results = [
        {"content": "Chunk 1", "score": 0.85},
        {"content": "Chunk 2", "score": 0.39},
        {"content": "Chunk 3", "score": 0.40},
        {"content": "Chunk 4", "score": 0.12},
    ]
    filtered = filter_knowledge_results_by_score(raw_results, threshold=0.40)
    assert len(filtered) == 2
    assert filtered[0]["content"] == "Chunk 1"
    assert filtered[1]["content"] == "Chunk 3"


def test_prompts_helpers():
    """prompts.py のフォーマット関数の単体テスト"""
    tool_config = get_knowledge_tool_config()
    assert "tools" in tool_config
    assert tool_config["tools"][0]["toolSpec"]["name"] == "retrieve_knowledge"

    empty_msg = format_rag_empty_tool_result(query="雑談", threshold=0.40)
    assert "雑談" in empty_msg
    assert "0.40" in empty_msg

    grounding_msg = format_rag_grounding_tool_result(["[1] doc1: 内容"])
    assert "[1] doc1: 内容" in grounding_msg
    assert "【回答方針・根拠提示ルール】" in grounding_msg
    assert "### 根拠資料 (参照ドキュメント)" in grounding_msg


def test_chat_branch_no_tool_used_for_greetings():
    """
    ナレッジ不要な場合（挨拶、翻訳、要約、コード生成等）:
    LLMがツールを使わず stopReason: "end_turn" で直接回答し、
    Retrieve API が呼ばれず LLM 1 回で高速完了することの検証。
    """
    raw_key = setup_knowledge_api_key("Greeting Key")
    client = TestClient(app)

    # 1 ターン目で end_turn（ツール未呼び出し）
    mock_turn1_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [{"text": "こんにちは！本日はどのようなご用件でしょうか？"}],
            }
        },
        "usage": {"inputTokens": 20, "outputTokens": 15, "totalTokens": 35},
        "stopReason": "end_turn",
    }

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.return_value = mock_turn1_resp

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [{"role": "user", "content": "こんにちは"}]
            },
        )

        assert resp.status_code == 200
        data = resp.json()

        # 1. 回答内容の検証
        assert data["choices"][0]["message"]["content"] == "こんにちは！本日はどのようなご用件でしょうか？"

        # 2. Bedrock Converse は 1 回のみ呼び出されたこと
        assert mock_runtime.converse.call_count == 1

        # 3. Retrieve API は一切呼ばれていないこと（不要検索の防止）
        assert not mock_retrieve.called

        # 4. citations メタデータは付与されないこと
        assert "citations" not in data

        # 5. トークン使用量は Turn 1 のみであること
        assert data["usage"]["total_tokens"] == 35


def test_chat_branch_tool_use_and_score_filtering_success():
    """
    ナレッジ必要な場合:
    LLM が文脈から検索クエリを組み立てて stopReason: "tool_use" を返し、
    アプリ側で Retrieve API を実行してスコア足切り後に toolResult を渡し、
    2 ターン目で最終回答と citations を返却することの検証。
    """
    raw_key = setup_knowledge_api_key("RAG Tool Key")
    client = TestClient(app)

    # Turn 1: LLM が retrieve_knowledge ツールを呼び出す
    mock_turn1_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "toolUse": {
                            "toolUseId": "tooluse_rules_99",
                            "name": "retrieve_knowledge",
                            "input": {"query": "就業規則 慶弔休暇 日数"},
                        }
                    }
                ],
            }
        },
        "usage": {"inputTokens": 40, "outputTokens": 20, "totalTokens": 60},
        "stopReason": "tool_use",
    }

    # Turn 2: toolResult を受けて LLM が最終回答を生成
    mock_turn2_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "text": "慶弔休暇の日数は本人の結婚で5日、配偶者の出産で3日です [1]。\n\n### 根拠資料 (参照ドキュメント)\n- [1] 就業規則.pdf (public)"
                    }
                ],
            }
        },
        "usage": {"inputTokens": 100, "outputTokens": 50, "totalTokens": 150},
        "stopReason": "end_turn",
    }

    # Retrieve API の戻り値 (1件は閾値以上、もう1件は閾値未満で足切り対象)
    mock_retrieved_results = [
        {
            "content": "慶弔休暇規定：本人の結婚5日、配偶者の出産3日。",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/rules.pdf"}},
            "metadata": {"filename": "就業規則.pdf", "group": "public"},
            "score": 0.82,  # 閾値 (0.40) 以上 -> 採用
        },
        {
            "content": "無関係な古いメモ",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/memo.txt"}},
            "metadata": {"filename": "memo.txt", "group": "public"},
            "score": 0.15,  # 閾値 (0.40) 未満 -> 足切り破棄
        },
    ]

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        mock_retrieve.return_value = mock_retrieved_results
        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.side_effect = [mock_turn1_resp, mock_turn2_resp]

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [
                    {"role": "user", "content": "慶弔休暇は何日取得できますか？"}
                ]
            },
        )

        assert resp.status_code == 200
        data = resp.json()

        # 1. Retrieve API が LLM の生成したクエリで呼ばれていること
        assert mock_retrieve.called
        retrieve_call = mock_retrieve.call_args[1]
        assert retrieve_call["query"] == "就業規則 慶弔休暇 日数"

        # 2. Bedrock Converse が 2 回呼び出されていること
        assert mock_runtime.converse.call_count == 2

        # 3. Turn 2 に渡された toolResult に足切り後のチャンクのみが含まれていること (memo.txt は含まれない)
        turn2_kwargs = mock_runtime.converse.call_args_list[1][1]
        user_tool_msg = turn2_kwargs["messages"][-1]
        assert user_tool_msg["role"] == "user"
        tool_content_text = user_tool_msg["content"][0]["toolResult"]["content"][0]["text"]
        assert "就業規則.pdf" in tool_content_text
        assert "memo.txt" not in tool_content_text

        # 4. citations メタデータに足切り後の 1 件のみが含まれていること
        assert "citations" in data
        assert len(data["citations"]) == 1
        assert data["citations"][0]["filename"] == "就業規則.pdf"
        assert data["citations"][0]["score"] == 0.82

        # 5. トークン使用量が Turn 1 (60) + Turn 2 (150) = 210 に合算されていること
        assert data["usage"]["prompt_tokens"] == 140
        assert data["usage"]["completion_tokens"] == 70
        assert data["usage"]["total_tokens"] == 210


def test_chat_branch_all_chunks_filtered_by_score():
    """
    ナレッジ検索されたが、全チャンクのスコアが閾値未満で破棄された場合:
    toolResult に該当なしメッセージが渡され、citations が空で最終回答が得られることの検証。
    """
    raw_key = setup_knowledge_api_key("Low Score Key")
    client = TestClient(app)

    mock_turn1_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "toolUse": {
                            "toolUseId": "tooluse_vague_01",
                            "name": "retrieve_knowledge",
                            "input": {"query": "社内旅行の持ち物"},
                        }
                    }
                ],
            }
        },
        "usage": {"inputTokens": 30, "outputTokens": 10, "totalTokens": 40},
        "stopReason": "tool_use",
    }

    mock_turn2_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "text": "ナレッジには社員旅行に関する規定はありませんでした。一般的には着替えや身の回り品をお持ちください。"
                    }
                ],
            }
        },
        "usage": {"inputTokens": 60, "outputTokens": 30, "totalTokens": 90},
        "stopReason": "end_turn",
    }

    # 全てスコアが 0.40 未満のチャンク
    mock_low_score_results = [
        {
            "content": "関係のない別部署の雑談ログ",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/chat.txt"}},
            "metadata": {"filename": "chat.txt", "group": "public"},
            "score": 0.22,
        }
    ]

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        mock_retrieve.return_value = mock_low_score_results
        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.side_effect = [mock_turn1_resp, mock_turn2_resp]

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [{"role": "user", "content": "社員旅行は何を持っていけばいい？"}]
            },
        )

        assert resp.status_code == 200
        data = resp.json()

        # Turn 2 のメッセージに空ナレッジ用の案内が含まれること
        turn2_kwargs = mock_runtime.converse.call_args_list[1][1]
        user_tool_msg = turn2_kwargs["messages"][-1]
        tool_content_text = user_tool_msg["content"][0]["toolResult"]["content"][0]["text"]
        assert "ナレッジベースを検索しましたが、クエリ「社内旅行の持ち物」に関連するドキュメントは見つかりませんでした" in tool_content_text

        # citations は空であること
        assert "citations" not in data or len(data["citations"]) == 0
        assert data["choices"][0]["message"]["content"] == "ナレッジには社員旅行に関する規定はありませんでした。一般的には着替えや身の回り品をお持ちください。"


def test_chat_branch_retrieval_exception_resilience():
    """
    Retrieve API 呼び出し時に例外が発生した場合でも 500 エラーにならず、
    toolResult でエラーが伝達されて回答が生成されること（耐障害性）の検証。
    """
    raw_key = setup_knowledge_api_key("Resilience Key")
    client = TestClient(app)

    mock_turn1_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "toolUse": {
                            "toolUseId": "tooluse_err_01",
                            "name": "retrieve_knowledge",
                            "input": {"query": "トラブル対応"},
                        }
                    }
                ],
            }
        },
        "usage": {"inputTokens": 30, "outputTokens": 10, "totalTokens": 40},
        "stopReason": "tool_use",
    }

    mock_turn2_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "text": "現在ナレッジシステムが一時的に利用できないため、一般的な対応手順をご案内します。"
                    }
                ],
            }
        },
        "usage": {"inputTokens": 60, "outputTokens": 30, "totalTokens": 90},
        "stopReason": "end_turn",
    }

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        # Retrieve API が一時的なネットワークやサービス障害で例外を送出
        mock_retrieve.side_effect = Exception("Bedrock Knowledge Base service temporarily unavailable")
        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.side_effect = [mock_turn1_resp, mock_turn2_resp]

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [{"role": "user", "content": "トラブル対応について教えて"}]
            },
        )

        assert resp.status_code == 200
        data = resp.json()
        assert "現在ナレッジシステムが一時的に利用できないため" in data["choices"][0]["message"]["content"]

