import logging
from typing import Any, Dict, List, Optional, Tuple
from fastapi import APIRouter, Depends, Header, HTTPException, Request, status
from fastapi.responses import JSONResponse, StreamingResponse
from pydantic import BaseModel, ConfigDict, Field
from sqlmodel import Session, select
from app.core.config import settings
from app.core.database import get_session
from app.core.security import hash_api_key
from app.models.api_key import ApiKey
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
)
from app.services.knowledge_service import resolve_document_and_download_url

logger = logging.getLogger(__name__)

router = APIRouter()


class ChatCompletionRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    model: Optional[str] = None
    messages: List[Dict[str, Any]]
    temperature: Optional[float] = None
    max_tokens: Optional[int] = Field(default=None, alias="max_completion_tokens")
    stream: bool = False


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


@router.post("/chat/completions")
async def chat_completions(
    request: ChatCompletionRequest,
    http_request: Request,
    api_key: ApiKey = Depends(verify_api_key),
    db: Session = Depends(get_session),
):
    """
    OpenAI 互換のチャット補完エンドポイント。
    APIキーに紐付いた固定モデル、許可グループによる Bedrock KB 検索 (Tool Use による動的判定)、
    回答根拠提示・mini-aigw 経由の資料ダウンロードリンク付与、およびシステムプロンプトを適用して Bedrock へ中継する。
    """
    model_id = api_key.model_id or request.model or settings.DEFAULT_MODEL_ID
    allowed_groups = api_key.get_allowed_groups()
    has_knowledge_access = bool(allowed_groups and settings.BEDROCK_KB_ID)

    client = get_bedrock_runtime_client()

    # ストリーミング呼び出し時は現行の直接配信を維持
    if request.stream:
        effective_messages = prepare_chat_messages(
            messages=request.messages,
            api_key_system_prompt=api_key.system_prompt,
        )
        return StreamingResponse(
            call_converse_stream(
                client=client,
                model_id=model_id,
                messages=effective_messages,
                temperature=request.temperature,
                max_tokens=request.max_tokens,
            ),
            media_type="text/event-stream",
            headers={
                "Cache-Control": "no-cache",
                "Connection": "keep-alive",
                "X-Accel-Buffering": "no",
            },
        )

    # 1. ナレッジグループ許可がない、または KB が設定されていない場合（LLM 単体呼び出し）
    if not has_knowledge_access:
        effective_messages = prepare_chat_messages(
            messages=request.messages,
            api_key_system_prompt=api_key.system_prompt,
        )
        try:
            result = call_converse(
                client=client,
                model_id=model_id,
                messages=effective_messages,
                temperature=request.temperature,
                max_tokens=request.max_tokens,
            )
            result.pop("stop_reason", None)
            result.pop("output_message", None)
            return JSONResponse(content=result)
        except Exception as e:
            logger.error("Bedrock converse execution error: %s", e)
            raise HTTPException(
                status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
                detail={"error": {"message": str(e), "type": "api_error"}},
            )

    # 2. ナレッジアクセス権限がある場合: Tool Use による動的分岐処理
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
        turn1_result = call_converse(
            client=client,
            model_id=model_id,
            messages=effective_messages,
            temperature=request.temperature,
            max_tokens=request.max_tokens,
            tool_config=tool_config,
        )
    except Exception as e:
        logger.error("Bedrock converse Turn 1 error: %s", e)
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail={"error": {"message": str(e), "type": "api_error"}},
        )

    stop_reason = turn1_result.get("stop_reason", "end_turn")
    output_message = turn1_result.get("output_message", {})

    # 分岐 A: ナレッジ不要な場合（挨拶、翻訳、要約、コード生成等）
    # LLMはツールを使わず stopReason: "end_turn" で直接回答（LLM 1回で完了）
    if stop_reason != "tool_use":
        turn1_result.pop("stop_reason", None)
        turn1_result.pop("output_message", None)
        return JSONResponse(content=turn1_result)

    # 分岐 B: ナレッジが必要な場合 (stopReason: "tool_use")
    tool_use_block = None
    for block in output_message.get("content", []):
        if "toolUse" in block:
            tool_use_block = block["toolUse"]
            break

    if not tool_use_block:
        turn1_result.pop("stop_reason", None)
        turn1_result.pop("output_message", None)
        return JSONResponse(content=turn1_result)

    tool_use_id = tool_use_block.get("toolUseId", "")
    query = tool_use_block.get("input", {}).get("query", "").strip()
    if not query:
        query = extract_last_user_query(request.messages) or ""

    # Bedrock KB Retrieve API を実行し、スコア閾値による足切りを実施
    citations: List[Dict[str, Any]] = []
    filtered_results: List[Dict[str, Any]] = []
    retrieval_error = None

    try:
        agent_client = get_bedrock_agent_runtime_client()
        raw_results = retrieve_knowledge(
            agent_client=agent_client,
            kb_id=settings.BEDROCK_KB_ID,
            query=query,
            allowed_groups=allowed_groups,
            number_of_results=settings.RAG_NUMBER_OF_RESULTS,
        )
        # スコア閾値による足切り (Score Filtering)
        filtered_results = filter_knowledge_results_by_score(
            raw_results,
            threshold=settings.RAG_SCORE_THRESHOLD,
        )
    except Exception as e:
        retrieval_error = str(e)
        logger.warning("Bedrock KB retrieval failed in chat tool use: %s", e)

    # toolResult コンテンツの組み立て
    if retrieval_error:
        tool_result_text = (
            f"ナレッジベースの検索中にエラーが発生しました ({retrieval_error})。"
            "ナレッジが一時的に参照できない旨を案内し、一般的な知識で回答してください。"
        )
    elif not filtered_results:
        # スコア閾値未満または検索ヒット0件
        tool_result_text = format_rag_empty_tool_result(
            query=query,
            threshold=settings.RAG_SCORE_THRESHOLD,
        )
    else:
        base_url = str(http_request.base_url)
        context_chunks: List[str] = []

        for i, res in enumerate(filtered_results, start=1):
            content = res.get("content", "")
            metadata = res.get("metadata", {})
            location = res.get("location", {})
            s3_uri = location.get("s3Location", {}).get("uri", "")
            filename = metadata.get("filename") or (s3_uri.split("/")[-1] if s3_uri else f"資料{i}")
            doc_group = metadata.get("group", "unknown")
            score = float(res.get("score", 0.0))

            doc_id, download_url = resolve_document_and_download_url(
                db=db,
                base_url=base_url,
                s3_uri=s3_uri,
                metadata=metadata,
            )

            url_desc = f"（ダウンロードURL: {download_url}）" if download_url else ""
            context_chunks.append(
                f"[{i}] ファイル名: {filename} (グループ: {doc_group}, 類似度スコア: {score:.2f}) {url_desc}\n{content}"
            )
            citations.append({
                "index": i,
                "filename": filename,
                "group": doc_group,
                "document_id": doc_id,
                "download_url": download_url,
                "s3_uri": s3_uri,
                "score": score,
            })

        tool_result_text = format_rag_grounding_tool_result(context_chunks)

    # Turn 2: 会話履歴に assistant (toolUse) と user (toolResult) を追加して最終回答を得る
    turn2_messages = list(effective_messages)
    turn2_messages.append(output_message)
    turn2_messages.append({
        "role": "user",
        "content": [
            {
                "toolResult": {
                    "toolUseId": tool_use_id,
                    "content": [{"text": tool_result_text}],
                    "status": "success",
                }
            }
        ],
    })

    try:
        turn2_result = call_converse(
            client=client,
            model_id=model_id,
            messages=turn2_messages,
            temperature=request.temperature,
            max_tokens=request.max_tokens,
            tool_config=None,  # 2ターン目は最終回答を生成させるためツール指定なし
        )
    except Exception as e:
        logger.error("Bedrock converse Turn 2 error: %s", e)
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail={"error": {"message": str(e), "type": "api_error"}},
        )

    # トークン使用量の合算 (Turn 1 + Turn 2)
    turn1_usage = turn1_result.get("usage", {})
    turn2_usage = turn2_result.get("usage", {})
    total_prompt_tokens = turn1_usage.get("prompt_tokens", 0) + turn2_usage.get("prompt_tokens", 0)
    total_completion_tokens = turn1_usage.get("completion_tokens", 0) + turn2_usage.get("completion_tokens", 0)

    turn2_result["usage"] = {
        "prompt_tokens": total_prompt_tokens,
        "completion_tokens": total_completion_tokens,
        "total_tokens": total_prompt_tokens + total_completion_tokens,
    }

    if citations:
        turn2_result["citations"] = citations

    turn2_result.pop("stop_reason", None)
    turn2_result.pop("output_message", None)
    return JSONResponse(content=turn2_result)

