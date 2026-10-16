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


def test_chat_streaming_rag_tool_use_and_citations():
    """
    ストリーミング (stream=True) かつナレッジ必要な場合:
    Turn 1 で tool_use 判定 -> Retrieve API 実行 -> Turn 2 で converse_stream が呼ばれ、
    SSE ストリームの終了チャンクに citations と合算 usage が付与され、ChatUsageLog が記録されることの検証。
    """
    from sqlmodel import select
    from app.models.usage_log import ChatUsageLog

    raw_key = setup_knowledge_api_key("Streaming RAG Bot")
    client = TestClient(app)

    mock_turn1_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "toolUse": {
                            "toolUseId": "tooluse_stream_01",
                            "name": "retrieve_knowledge",
                            "input": {"query": "リモートワーク 手当"},
                        }
                    }
                ],
            }
        },
        "usage": {"inputTokens": 35, "outputTokens": 15, "totalTokens": 50},
        "stopReason": "tool_use",
    }

    mock_retrieved_results = [
        {
            "content": "在宅勤務手当は月額5,000円を支給します。",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/remote_work.pdf"}},
            "metadata": {
                "filename": "在宅勤務規程.pdf",
                "group": "public",
                "tags": ["規程", "手当"],
            },
            "score": 0.91,
        }
    ]

    mock_turn2_stream = {
        "stream": [
            {"messageStart": {"role": "assistant"}},
            {"contentBlockDelta": {"delta": {"text": "在宅勤務手当は月額5,000円です [1]。\n\n"}}},
            {"contentBlockDelta": {"delta": {"text": "### 根拠資料 (参照ドキュメント)\n- [1] 在宅勤務規程.pdf"}}},
            {"messageStop": {"stopReason": "end_turn"}},
            {"metadata": {"usage": {"inputTokens": 120, "outputTokens": 40, "totalTokens": 160}}},
        ]
    }

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        mock_retrieve.return_value = mock_retrieved_results
        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.return_value = mock_turn1_resp
        mock_runtime.converse_stream.return_value = mock_turn2_stream

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [{"role": "user", "content": "リモートワーク手当はいくらですか？"}],
                "stream": True,
            },
        )

        assert resp.status_code == 200
        assert mock_retrieve.called
        assert mock_runtime.converse.call_count == 1
        assert mock_runtime.converse_stream.call_count == 1

        # Turn 2 にタグ情報付きのコンテキストが渡されていること
        turn2_kwargs = mock_runtime.converse_stream.call_args[1]
        tool_msg = turn2_kwargs["messages"][-1]
        tool_text = tool_msg["content"][0]["toolResult"]["content"][0]["text"]
        assert "タグ: 規程, 手当" in tool_text

        # SSE パース検証
        sse_Events = []
        for line in resp.text.splitlines():
            if line.startswith("data: ") and line != "data: [DONE]":
                sse_Events.append(json.loads(line[6:]))

        full_text = "".join(
            ev["choices"][0]["delta"].get("content", "") for ev in sse_Events
        )
        assert "在宅勤務手当は月額5,000円です [1]。" in full_text

        # 最終チャンクに citations と合算 usage (50 + 160 = 210) が含まれること
        last_chunk = sse_Events[-1]
        assert "citations" in last_chunk
        assert last_chunk["citations"][0]["filename"] == "在宅勤務規程.pdf"
        assert "usage" in last_chunk
        assert last_chunk["usage"]["prompt_tokens"] == 155
        assert last_chunk["usage"]["completion_tokens"] == 55
        assert last_chunk["usage"]["total_tokens"] == 210

        # ChatUsageLog に監査ログが保存されていること
        with Session(engine) as session:
            log_rec = session.exec(
                select(ChatUsageLog)
                .where(ChatUsageLog.api_key_name == "Streaming RAG Bot")
                .order_by(ChatUsageLog.created_at.desc())
            ).first()
            assert log_rec is not None
            assert log_rec.is_stream is True
            assert log_rec.rag_triggered is True
            assert log_rec.total_tokens == 210
            assert "リモートワーク 手当" in log_rec.get_search_queries()
            assert log_rec.get_cited_documents()[0]["filename"] == "在宅勤務規程.pdf"


def test_chat_streaming_no_tool_used_for_greetings():
    """
    ストリーミング (stream=True) でナレッジ不要な場合:
    Turn 1 の回答がそのまま SSE ストリームとして返却され、Retrieve API も Turn 2 も呼ばれないことの検証。
    """
    raw_key = setup_knowledge_api_key("Streaming Greeting Bot")
    client = TestClient(app)

    mock_turn1_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [{"text": "お疲れ様です！何かお手伝いできることはありますか？"}],
            }
        },
        "usage": {"inputTokens": 25, "outputTokens": 15, "totalTokens": 40},
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
                "messages": [{"role": "user", "content": "お疲れ様です"}],
                "stream": True,
            },
        )

        assert resp.status_code == 200
        assert not mock_retrieve.called
        assert mock_runtime.converse.call_count == 1
        assert not mock_runtime.converse_stream.called

        sse_events = [
            json.loads(line[6:])
            for line in resp.text.splitlines()
            if line.startswith("data: ") and line != "data: [DONE]"
        ]
        full_text = "".join(ev["choices"][0]["delta"].get("content", "") for ev in sse_events)
        assert full_text == "お疲れ様です！何かお手伝いできることはありますか？"


def test_chat_multiple_tool_use_blocks_handled_without_crash():
    """
    Turn 1 で LLM が複数の toolUse ブロックを同時に返した場合、
    すべての toolUseId に対応する toolResult が構築されて Turn 2 に渡され、
    重複チャンクが排除されて正常に回答が生成されることの検証。
    """
    raw_key = setup_knowledge_api_key("Multi Tool Bot")
    client = TestClient(app)

    mock_turn1_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "toolUse": {
                            "toolUseId": "tu_vacation_1",
                            "name": "retrieve_knowledge",
                            "input": {"query": "有給休暇 付与日数"},
                        }
                    },
                    {
                        "toolUse": {
                            "toolUseId": "tu_expense_2",
                            "name": "retrieve_knowledge",
                            "input": {"query": "出張旅費 宿泊上限"},
                        }
                    },
                ],
            }
        },
        "usage": {"inputTokens": 50, "outputTokens": 30, "totalTokens": 80},
        "stopReason": "tool_use",
    }

    mock_turn2_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {"text": "有給休暇は初年度10日 [1]、宿泊上限は1泊12,000円です [2]。"}
                ],
            }
        },
        "usage": {"inputTokens": 150, "outputTokens": 40, "totalTokens": 190},
        "stopReason": "end_turn",
    }

    res_query1 = [
        {
            "content": "年次有給休暇は入社時に10日付与される。",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/vacation.pdf"}},
            "metadata": {"filename": "休暇規程.pdf", "group": "public"},
            "score": 0.85,
        }
    ]
    res_query2 = [
        {
            "content": "国内出張の宿泊費上限は1泊あたり12,000円とする。",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/expense.pdf"}},
            "metadata": {"filename": "旅費規程.pdf", "group": "public"},
            "score": 0.89,
        }
    ]

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        mock_retrieve.side_effect = [res_query1, res_query2]
        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.side_effect = [mock_turn1_resp, mock_turn2_resp]

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={
                "messages": [{"role": "user", "content": "有給休暇の日数と出張の宿泊上限を教えて"}]
            },
        )

        assert resp.status_code == 200
        data = resp.json()

        # retrieve_knowledge が 2 回呼ばれていること
        assert mock_retrieve.call_count == 2

        # Turn 2 の最後の user メッセージに 2 つの toolResult ブロックが含まれていること
        turn2_kwargs = mock_runtime.converse.call_args_list[1][1]
        user_tool_msg = turn2_kwargs["messages"][-1]
        assert len(user_tool_msg["content"]) == 2
        assert user_tool_msg["content"][0]["toolResult"]["toolUseId"] == "tu_vacation_1"
        assert user_tool_msg["content"][1]["toolResult"]["toolUseId"] == "tu_expense_2"

        # citations に 2 件（index 1, 2）が含まれていること
        assert len(data["citations"]) == 2
        assert data["citations"][0]["filename"] == "休暇規程.pdf"
        assert data["citations"][1]["filename"] == "旅費規程.pdf"


def test_chat_deleted_document_ghost_chunk_filtered_out():
    """
    削除されたドキュメントが KB 同期完了前に Retrieve API から返却された場合でも、
    Ghost Chunk Filter により検索結果から即座に除外されることの検証。
    """
    from app.models.knowledge import KnowledgeDocument
    from app.services.knowledge_service import delete_knowledge_document

    raw_key = setup_knowledge_api_key("Ghost Filter Bot")
    client = TestClient(app)

    # ドキュメントを登録して削除を実行
    ghost_doc_id = "doc-ghost-deleted-999"
    with Session(engine) as session:
        doc = KnowledgeDocument(
            id=ghost_doc_id,
            s3_key="public/obsolete_rule.pdf",
            s3_bucket="sample-knowledge-bucket",
            filename="廃止規程.pdf",
            file_size=512,
            content_type="application/pdf",
            group_name="public",
            sync_status="SYNCED",
        )
        session.add(doc)
        session.commit()

        delete_knowledge_document(
            db=session,
            s3_client=MagicMock(),
            bedrock_agent=None,
            kb_id="SAMPLE_KB_ID",
            doc_id=ghost_doc_id,
            trigger_sync=False,
        )

    mock_turn1_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "toolUse": {
                            "toolUseId": "tu_ghost_01",
                            "name": "retrieve_knowledge",
                            "input": {"query": "古い規程"},
                        }
                    }
                ],
            }
        },
        "usage": {"inputTokens": 20, "outputTokens": 10, "totalTokens": 30},
        "stopReason": "tool_use",
    }

    mock_turn2_resp = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [{"text": "現行規程のみに基づいて回答します [1]。"}],
            }
        },
        "usage": {"inputTokens": 50, "outputTokens": 20, "totalTokens": 70},
        "stopReason": "end_turn",
    }

    # Retrieve API が削除済みの廃止規程と有効な現行規程の両方を返したと仮定
    mock_retrieved = [
        {
            "content": "廃止された古いルールです。",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/obsolete_rule.pdf"}},
            "metadata": {"document_id": ghost_doc_id, "filename": "廃止規程.pdf", "group": "public"},
            "score": 0.95,
        },
        {
            "content": "現在有効な新しいルールです。",
            "location": {"s3Location": {"uri": "s3://sample-knowledge-bucket/public/active_rule.pdf"}},
            "metadata": {"filename": "現行規程.pdf", "group": "public"},
            "score": 0.80,
        },
    ]

    with patch("app.api.v1.chat.retrieve_knowledge") as mock_retrieve, \
         patch("app.api.v1.chat.get_bedrock_runtime_client") as mock_get_runtime:

        mock_retrieve.return_value = mock_retrieved
        mock_runtime = MagicMock()
        mock_get_runtime.return_value = mock_runtime
        mock_runtime.converse.side_effect = [mock_turn1_resp, mock_turn2_resp]

        resp = client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {raw_key}"},
            json={"messages": [{"role": "user", "content": "規程を教えて"}]},
        )

        assert resp.status_code == 200
        data = resp.json()

        # 廃止規程.pdf は除外され、現行規程.pdf のみが含まれていること
        assert len(data["citations"]) == 1
        assert data["citations"][0]["filename"] == "現行規程.pdf"
        turn2_kwargs = mock_runtime.converse.call_args_list[1][1]
        tool_text = turn2_kwargs["messages"][-1]["content"][0]["toolResult"]["content"][0]["text"]
        assert "廃止規程.pdf" not in tool_text
        assert "現行規程.pdf" in tool_text


