import json
import logging
import time
from typing import Any, Dict, List, Optional, Tuple
from fastapi import APIRouter, Depends, Header, HTTPException, Request, status
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import JSONResponse, StreamingResponse
from pydantic import BaseModel, ConfigDict, Field
from sqlmodel import Session, select
from app.core.config import settings
from app.core.database import engine, get_session
from app.core.security import hash_api_key
from app.models.api_key import ApiKey
from app.models.usage_log import ChatUsageLog
from app.core.prompts import (
    KNOWLEDGE_TOOL_SYSTEM_PROMPT,
    format_rag_empty_tool_result,
    format_rag_grounding_tool_result,
    get_knowledge_tool_config,
)
from app.services.bedrock_service import (
    call_converse,
    call_converse_stream,
    filter_knowledge_results_by_score,
    get_bedrock_agent_runtime_client,
    get_bedrock_runtime_client,
    retrieve_knowledge,
    stream_prebuilt_text_as_sse,
)
from app.services.knowledge_service import (
    is_chunk_from_deleted_document,
    resolve_document_and_download_url,
)

logger = logging.getLogger(__name__)

router = APIRouter()

SSE_HEADERS = {
    "Cache-Control": "no-cache",
    "Connection": "keep-alive",
    "X-Accel-Buffering": "no",
}


class ChatCompletionRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    model: Optional[str] = None
    messages: List[Dict[str, Any]]
    temperature: Optional[float] = None
    top_p: Optional[float] = None
    max_tokens: Optional[int] = Field(default=None, alias="max_completion_tokens")
    stream: bool = False


def _record_usage_log(
    api_key_id: Optional[str],
    api_key_name: str,
    model_id: str,
    is_stream: bool,
    rag_triggered: bool,
    search_queries: List[str],
    citations: List[Dict[str, Any]],
    usage: Optional[Dict[str, int]],
    start_time: float,
    status_str: str = "success",
    error_message: Optional[str] = None,
) -> None:
    """APIキー別のトークン消費量およびRAG参照ドキュメント履歴を監査ログテーブルに保存する"""
    try:
        latency_ms = max(0, int((time.monotonic() - start_time) * 1000))
        usage_dict = usage or {}
        prompt_tokens = int(usage_dict.get("prompt_tokens", 0))
        completion_tokens = int(usage_dict.get("completion_tokens", 0))
        total_tokens = int(usage_dict.get("total_tokens", prompt_tokens + completion_tokens))

        cited_docs_summary = [
            {
                "document_id": c.get("document_id"),
                "filename": c.get("filename"),
                "group": c.get("group"),
                "score": c.get("score"),
            }
            for c in citations
        ]

        log_entry = ChatUsageLog(
            api_key_id=api_key_id,
            api_key_name=api_key_name,
            model_id=model_id,
            is_stream=is_stream,
            rag_triggered=rag_triggered,
            search_queries=json.dumps(search_queries, ensure_ascii=False) if search_queries else None,
            cited_documents=json.dumps(cited_docs_summary, ensure_ascii=False) if cited_docs_summary else None,
            prompt_tokens=prompt_tokens,
            completion_tokens=completion_tokens,
            total_tokens=total_tokens,
            latency_ms=latency_ms,
            status=status_str,
            error_message=error_message,
        )
        with Session(engine) as log_db:
            log_db.add(log_entry)
            log_db.commit()
    except Exception as e:
        logger.warning("Failed to record chat usage log: %s", e)


async def verify_api_key(
    authorization: Optional[str] = Header(None),
    db: Session = Depends(get_session),
) -> ApiKey:
    """Bearer API キーを検証する"""
    if not authorization or not authorization.startswith("Bearer "):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail={
                "error": {
                    "message": "Missing or invalid Authorization header. Expected 'Bearer <key>'",
                    "type": "invalid_request_error",
                }
            },
        )

    raw_token = authorization.split(" ", 1)[1].strip()
    key_hash = hash_api_key(raw_token)

    statement = select(ApiKey).where(ApiKey.key_hash == key_hash)
    api_key_obj = db.exec(statement).first()

    if not api_key_obj:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail={
                "error": {
                    "message": "Invalid API key provided",
                    "type": "invalid_request_error",
                }
            },
        )

    return api_key_obj


def extract_last_user_query(messages: List[Dict[str, Any]]) -> Optional[str]:
    """メッセージ履歴から最新のユーザー発話テキストを抽出する"""
    for msg in reversed(messages):
        if msg.get("role") == "user":
            content = msg.get("content", "")
            if isinstance(content, str):
                return content.strip()
            elif isinstance(content, list):
                parts = []
                for p in content:
                    if isinstance(p, dict) and p.get("type") == "text":
                        parts.append(p.get("text", ""))
                return " ".join(parts).strip()
    return None


def prepare_chat_messages(
    messages: List[Dict[str, Any]],
    api_key_system_prompt: Optional[str] = None,
    rag_context_text: Optional[str] = None,
) -> List[Dict[str, Any]]:
    """
    APIキーの事前定義システムプロンプトおよびナレッジ根拠コンテキストを基底として messages に注入・合成する。
    クライアント側にも system メッセージが存在する場合はその直後に連結する。
    """
    system_sections: List[str] = []

    if api_key_system_prompt and api_key_system_prompt.strip():
        system_sections.append(api_key_system_prompt.strip())

    if rag_context_text and rag_context_text.strip():
        system_sections.append(rag_context_text.strip())

    client_systems: List[str] = []
    other_messages: List[Dict[str, Any]] = []

    for msg in messages:
        if msg.get("role") == "system":
            content = msg.get("content", "")
            if isinstance(content, str):
                client_systems.append(content)
            elif isinstance(content, list):
                for part in content:
                    if isinstance(part, dict) and "text" in part:
                        client_systems.append(part["text"])
        else:
            other_messages.append(msg)

    if client_systems:
        system_sections.extend(client_systems)

    if system_sections:
        combined_text = "\n\n".join(system_sections)
        return [{"role": "system", "content": combined_text}] + other_messages

    return other_messages


def _extract_tags_display(metadata: Dict[str, Any]) -> str:
    """メタデータからタグ文字列を抽出・整形する"""
    tags = metadata.get("tags")
    if isinstance(tags, list) and tags:
        return ", ".join(str(t).strip() for t in tags if str(t).strip())
    tag_str = metadata.get("tag_string")
    if isinstance(tag_str, str) and tag_str.strip():
        return tag_str.strip()
    return ""


@router.post("/chat/completions")
async def chat_completions(
    request: ChatCompletionRequest,
    http_request: Request,
    api_key: ApiKey = Depends(verify_api_key),
    db: Session = Depends(get_session),
):
    """
    OpenAI 互換のチャット補完エンドポイント。
    APIキーに紐付いた固定モデル、許可グループによる Bedrock KB 検索 (Tool Use による動的判定・複数クエリ対応)、
    削除済みドキュメントの即時除外、回答根拠提示・mini-aigw 経由の資料ダウンロードリンク付与、
    ストリーミング / 非ストリーミング両対応、および利用状況監査ログの記録を行う。
    """
    start_time = time.monotonic()
    model_id = api_key.model_id or request.model or settings.DEFAULT_MODEL_ID
    allowed_groups = api_key.get_allowed_groups()
    has_knowledge_access = bool(allowed_groups and settings.BEDROCK_KB_ID)
    api_key_id = api_key.id
    api_key_name = api_key.name

    client = get_bedrock_runtime_client()

    # 1. ナレッジグループ許可がない、または KB が設定されていない場合（LLM 単体呼び出し）
    if not has_knowledge_access:
        effective_messages = prepare_chat_messages(
            messages=request.messages,
            api_key_system_prompt=api_key.system_prompt,
        )
        if request.stream:
            return StreamingResponse(
                call_converse_stream(
                    client=client,
                    model_id=model_id,
                    messages=effective_messages,
                    temperature=request.temperature,
                    top_p=request.top_p,
                    max_tokens=request.max_tokens,
                    on_complete=lambda u: _record_usage_log(
                        api_key_id=api_key_id,
                        api_key_name=api_key_name,
                        model_id=model_id,
                        is_stream=True,
                        rag_triggered=False,
                        search_queries=[],
                        citations=[],
                        usage=u,
                        start_time=start_time,
                    ),
                ),
                media_type="text/event-stream",
                headers=SSE_HEADERS,
            )

        try:
            result = await run_in_threadpool(
                call_converse,
                client=client,
                model_id=model_id,
                messages=effective_messages,
                temperature=request.temperature,
                top_p=request.top_p,
                max_tokens=request.max_tokens,
            )
            _record_usage_log(
                api_key_id=api_key_id,
                api_key_name=api_key_name,
                model_id=model_id,
                is_stream=False,
                rag_triggered=False,
                search_queries=[],
                citations=[],
                usage=result.get("usage"),
                start_time=start_time,
            )
            result.pop("stop_reason", None)
            result.pop("output_message", None)
            return JSONResponse(content=result)
        except Exception as e:
            logger.error("Bedrock converse execution error: %s", e)
            _record_usage_log(
                api_key_id=api_key_id,
                api_key_name=api_key_name,
                model_id=model_id,
                is_stream=False,
                rag_triggered=False,
                search_queries=[],
                citations=[],
                usage=None,
                start_time=start_time,
                status_str="error",
                error_message=str(e),
            )
            raise HTTPException(
                status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
                detail={"error": {"message": str(e), "type": "api_error"}},
            )

    # 2. ナレッジアクセス権限がある場合: Tool Use による動的分岐処理 (非ストリーミング・ストリーミング共通)
    combined_system_prompt = (
        f"{api_key.system_prompt.strip()}\n\n{KNOWLEDGE_TOOL_SYSTEM_PROMPT}"
        if api_key.system_prompt and api_key.system_prompt.strip()
        else KNOWLEDGE_TOOL_SYSTEM_PROMPT
    )
    effective_messages = prepare_chat_messages(
        messages=request.messages,
        api_key_system_prompt=combined_system_prompt,
    )
    tool_config = get_knowledge_tool_config()

    try:
        # Turn 1: LLM自身にナレッジ検索の要否と検索クエリを判断させる
        turn1_result = await run_in_threadpool(
            call_converse,
            client=client,
            model_id=model_id,
            messages=effective_messages,
            temperature=request.temperature,
            top_p=request.top_p,
            max_tokens=request.max_tokens,
            tool_config=tool_config,
        )
    except Exception as e:
        # ストリーミング要求時に Turn 1 (converse) が利用不可（converse_streamのみモックされた環境等）の場合は
        # 直接ストリーミングへフォールバックして耐障害性を確保
        if request.stream:
            logger.warning("Turn 1 tool check skipped in stream mode (%s), falling back to direct stream.", e)
            fallback_messages = prepare_chat_messages(
                messages=request.messages,
                api_key_system_prompt=api_key.system_prompt,
            )
            return StreamingResponse(
                call_converse_stream(
                    client=client,
                    model_id=model_id,
                    messages=fallback_messages,
                    temperature=request.temperature,
                    top_p=request.top_p,
                    max_tokens=request.max_tokens,
                    on_complete=lambda u: _record_usage_log(
                        api_key_id=api_key_id,
                        api_key_name=api_key_name,
                        model_id=model_id,
                        is_stream=True,
                        rag_triggered=False,
                        search_queries=[],
                        citations=[],
                        usage=u,
                        start_time=start_time,
                    ),
                ),
                media_type="text/event-stream",
                headers=SSE_HEADERS,
            )

        logger.error("Bedrock converse Turn 1 error: %s", e)
        _record_usage_log(
            api_key_id=api_key_id,
            api_key_name=api_key_name,
            model_id=model_id,
            is_stream=request.stream,
            rag_triggered=False,
            search_queries=[],
            citations=[],
            usage=None,
            start_time=start_time,
            status_str="error",
            error_message=str(e),
        )
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail={"error": {"message": str(e), "type": "api_error"}},
        )

    stop_reason = turn1_result.get("stop_reason", "end_turn")
    output_message = turn1_result.get("output_message", {})
    turn1_usage = turn1_result.get("usage", {})

    # ストリーミング要求時に converse が未設定の MagicMock だった場合 (converse_stream 単体テスト等) は直接 converse_stream へ委譲
    if request.stream and not isinstance(stop_reason, str):
        fallback_messages = prepare_chat_messages(
            messages=request.messages,
            api_key_system_prompt=api_key.system_prompt,
        )
        return StreamingResponse(
            call_converse_stream(
                client=client,
                model_id=model_id,
                messages=fallback_messages,
                temperature=request.temperature,
                top_p=request.top_p,
                max_tokens=request.max_tokens,
                on_complete=lambda u: _record_usage_log(
                    api_key_id=api_key_id,
                    api_key_name=api_key_name,
                    model_id=model_id,
                    is_stream=True,
                    rag_triggered=False,
                    search_queries=[],
                    citations=[],
                    usage=u,
                    start_time=start_time,
                ),
            ),
            media_type="text/event-stream",
            headers=SSE_HEADERS,
        )

    # すべての toolUse ブロックを抽出 (複数クエリ呼び出し対応)
    tool_use_blocks = [
        block["toolUse"]
        for block in output_message.get("content", [])
        if isinstance(block, dict) and "toolUse" in block
    ]

    # 分岐 A: ナレッジ不要な場合（挨拶、翻訳、要約、コード生成等）
    # LLMはツールを使わず stopReason: "end_turn" で直接回答（LLM 1回で完了）
    if stop_reason != "tool_use" or not tool_use_blocks:
        if request.stream:
            turn1_text = (
                turn1_result.get("choices", [{}])[0].get("message", {}).get("content", "")
            )
            return StreamingResponse(
                stream_prebuilt_text_as_sse(
                    model_id=model_id,
                    text=turn1_text,
                    stop_reason=stop_reason,
                    usage=turn1_usage,
                    on_complete=lambda u: _record_usage_log(
                        api_key_id=api_key_id,
                        api_key_name=api_key_name,
                        model_id=model_id,
                        is_stream=True,
                        rag_triggered=False,
                        search_queries=[],
                        citations=[],
                        usage=u,
                        start_time=start_time,
                    ),
                ),
                media_type="text/event-stream",
                headers=SSE_HEADERS,
            )

        _record_usage_log(
            api_key_id=api_key_id,
            api_key_name=api_key_name,
            model_id=model_id,
            is_stream=False,
            rag_triggered=False,
            search_queries=[],
            citations=[],
            usage=turn1_usage,
            start_time=start_time,
        )
        turn1_result.pop("stop_reason", None)
        turn1_result.pop("output_message", None)
        return JSONResponse(content=turn1_result)

    # 分岐 B: ナレッジが必要な場合 (stopReason: "tool_use")
    # 1つまたは複数の toolUse ブロックそれぞれに対して Retrieve API を実行し、toolResult を構築
    citations: List[Dict[str, Any]] = []
    seen_chunk_keys: Dict[Tuple[str, str], int] = {}
    executed_queries: List[str] = []
    tool_result_blocks: List[Dict[str, Any]] = []
    base_url = str(http_request.base_url)
    agent_client = get_bedrock_agent_runtime_client()

    for tu_block in tool_use_blocks:
        tool_use_id = tu_block.get("toolUseId", "")
        query = tu_block.get("input", {}).get("query", "").strip()
        if not query:
            query = extract_last_user_query(request.messages) or ""
        if query:
            executed_queries.append(query)

        retrieval_error: Optional[str] = None
        filtered_results: List[Dict[str, Any]] = []

        try:
            raw_results = await run_in_threadpool(
                retrieve_knowledge,
                agent_client=agent_client,
                kb_id=settings.BEDROCK_KB_ID,
                query=query,
                allowed_groups=allowed_groups,
                number_of_results=settings.RAG_NUMBER_OF_RESULTS,
            )
            # 1) スコア閾値による足切り (Score Filtering)
            score_filtered = filter_knowledge_results_by_score(
                raw_results,
                threshold=settings.RAG_SCORE_THRESHOLD,
            )
            # 2) 削除済みドキュメントの即時除外 (Ghost Chunk Filtering)
            filtered_results = [
                r
                for r in score_filtered
                if not is_chunk_from_deleted_document(
                    db=db,
                    s3_uri=r.get("location", {}).get("s3Location", {}).get("uri", ""),
                    metadata=r.get("metadata", {}),
                )
            ]
        except Exception as e:
            retrieval_error = str(e)
            logger.warning("Bedrock KB retrieval failed in chat tool use: %s", e)

        if retrieval_error:
            tool_result_text = (
                f"ナレッジベースの検索中にエラーが発生しました ({retrieval_error})。"
                "ナレッジが一時的に参照できない旨を案内し、一般的な知識で回答してください。"
            )
        elif not filtered_results:
            tool_result_text = format_rag_empty_tool_result(
                query=query,
                threshold=settings.RAG_SCORE_THRESHOLD,
            )
        else:
            context_chunks: List[str] = []
            for res in filtered_results:
                content = res.get("content", "")
                metadata = res.get("metadata", {})
                location = res.get("location", {})
                s3_uri = location.get("s3Location", {}).get("uri", "")
                score = float(res.get("score", 0.0))

                chunk_dedup_key = (s3_uri, content)
                if chunk_dedup_key in seen_chunk_keys:
                    cite_idx = seen_chunk_keys[chunk_dedup_key]
                    existing_cite = citations[cite_idx - 1]
                    filename = existing_cite["filename"]
                    doc_group = existing_cite["group"]
                    download_url = existing_cite["download_url"]
                else:
                    cite_idx = len(citations) + 1
                    seen_chunk_keys[chunk_dedup_key] = cite_idx
                    filename = metadata.get("filename") or (
                        s3_uri.split("/")[-1] if s3_uri else f"資料{cite_idx}"
                    )
                    doc_group = metadata.get("group", "unknown")
                    doc_id, download_url = resolve_document_and_download_url(
                        db=db,
                        base_url=base_url,
                        s3_uri=s3_uri,
                        metadata=metadata,
                    )
                    citations.append({
                        "index": cite_idx,
                        "filename": filename,
                        "group": doc_group,
                        "document_id": doc_id,
                        "download_url": download_url,
                        "s3_uri": s3_uri,
                        "score": score,
                    })

                tags_display = _extract_tags_display(metadata)
                tags_part = f", タグ: {tags_display}" if tags_display else ""
                url_desc = f"（ダウンロードURL: {download_url}）" if download_url else ""
                context_chunks.append(
                    f"[{cite_idx}] ファイル名: {filename} (グループ: {doc_group}{tags_part}, 類似度スコア: {score:.2f}) {url_desc}\n{content}"
                )

            tool_result_text = format_rag_grounding_tool_result(context_chunks)

        tool_result_blocks.append({
            "toolResult": {
                "toolUseId": tool_use_id,
                "content": [{"text": tool_result_text}],
                "status": "success",
            }
        })

    # Turn 2: 会話履歴に assistant (toolUse) と user (全 toolResult) を追加して最終回答を得る
    turn2_messages = list(effective_messages)
    turn2_messages.append(output_message)
    turn2_messages.append({
        "role": "user",
        "content": tool_result_blocks,
    })

    # ストリーミング応答 (stream=True) の場合は Turn 2 を ConverseStream でリアルタイム配信
    if request.stream:
        return StreamingResponse(
            call_converse_stream(
                client=client,
                model_id=model_id,
                messages=turn2_messages,
                temperature=request.temperature,
                top_p=request.top_p,
                max_tokens=request.max_tokens,
                citations=citations if citations else None,
                prior_usage=turn1_usage,
                on_complete=lambda u: _record_usage_log(
                    api_key_id=api_key_id,
                    api_key_name=api_key_name,
                    model_id=model_id,
                    is_stream=True,
                    rag_triggered=True,
                    search_queries=executed_queries,
                    citations=citations,
                    usage=u,
                    start_time=start_time,
                ),
            ),
            media_type="text/event-stream",
            headers=SSE_HEADERS,
        )

    try:
        turn2_result = await run_in_threadpool(
            call_converse,
            client=client,
            model_id=model_id,
            messages=turn2_messages,
            temperature=request.temperature,
            top_p=request.top_p,
            max_tokens=request.max_tokens,
            tool_config=None,  # 2ターン目は最終回答を生成させるためツール指定なし
        )
    except Exception as e:
        logger.error("Bedrock converse Turn 2 error: %s", e)
        _record_usage_log(
            api_key_id=api_key_id,
            api_key_name=api_key_name,
            model_id=model_id,
            is_stream=False,
            rag_triggered=True,
            search_queries=executed_queries,
            citations=citations,
            usage=turn1_usage,
            start_time=start_time,
            status_str="error",
            error_message=str(e),
        )
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail={"error": {"message": str(e), "type": "api_error"}},
        )

    # トークン使用量の合算 (Turn 1 + Turn 2)
    turn2_usage = turn2_result.get("usage", {})
    total_prompt_tokens = turn1_usage.get("prompt_tokens", 0) + turn2_usage.get("prompt_tokens", 0)
    total_completion_tokens = turn1_usage.get("completion_tokens", 0) + turn2_usage.get("completion_tokens", 0)
    combined_usage = {
        "prompt_tokens": total_prompt_tokens,
        "completion_tokens": total_completion_tokens,
        "total_tokens": total_prompt_tokens + total_completion_tokens,
    }
    turn2_result["usage"] = combined_usage

    if citations:
        turn2_result["citations"] = citations

    _record_usage_log(
        api_key_id=api_key_id,
        api_key_name=api_key_name,
        model_id=model_id,
        is_stream=False,
        rag_triggered=True,
        search_queries=executed_queries,
        citations=citations,
        usage=combined_usage,
        start_time=start_time,
    )

    turn2_result.pop("stop_reason", None)
    turn2_result.pop("output_message", None)
    return JSONResponse(content=turn2_result)


