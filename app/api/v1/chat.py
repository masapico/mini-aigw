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
from app.services.bedrock_service import (
    call_converse,
    call_converse_stream,
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
    APIキーに紐付いた固定モデル、許可グループによる Bedrock KB 検索 (RAG)、
    回答根拠提示・mini-aigw 経由の資料ダウンロードリンク付与、およびシステムプロンプトを適用して Bedrock へ中継する。
    """
    model_id = api_key.model_id or request.model or settings.DEFAULT_MODEL_ID
    allowed_groups = api_key.get_allowed_groups()
    user_query = extract_last_user_query(request.messages)

    rag_context_text = None
    citations: List[Dict[str, Any]] = []

    # API キーにナレッジグループが許可されており、ユーザー質問が存在する場合にナレッジ検索を実施
    if allowed_groups and user_query and settings.BEDROCK_KB_ID:
        try:
            agent_client = get_bedrock_agent_runtime_client()
            retrieved_results = retrieve_knowledge(
                agent_client=agent_client,
                kb_id=settings.BEDROCK_KB_ID,
                query=user_query,
                allowed_groups=allowed_groups,
                number_of_results=4,
            )

            if retrieved_results:
                base_url = str(http_request.base_url)
                context_chunks: List[str] = []

                for i, res in enumerate(retrieved_results, start=1):
                    content = res.get("content", "")
                    metadata = res.get("metadata", {})
                    location = res.get("location", {})
                    s3_uri = location.get("s3Location", {}).get("uri", "")
                    filename = metadata.get("filename") or (s3_uri.split("/")[-1] if s3_uri else f"資料{i}")
                    doc_group = metadata.get("group", "unknown")
                    score = res.get("score", 0.0)

                    doc_id, download_url = resolve_document_and_download_url(
                        db=db,
                        base_url=base_url,
                        s3_uri=s3_uri,
                        metadata=metadata,
                    )

                    url_desc = f"（ダウンロードURL: {download_url}）" if download_url else ""
                    context_chunks.append(
                        f"[{i}] ファイル名: {filename} (グループ: {doc_group}) {url_desc}\n{content}"
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

                rag_instruction = (
                    "【回答方針・根拠提示ルール（ナレッジベース連携）】\n"
                    "以下の提供されたナレッジ（参考資料）の内容に基づいて回答してください。\n"
                    "1. 回答本文中の該当箇所に [1], [2] などの引用番号を付記してください。\n"
                    "2. 回答の末尾に「### 根拠資料 (参照ドキュメント)」セクションを必ず設け、"
                    "参照した各ドキュメントの [番号] ファイル名 (グループ名) と、記載されているダウンロードURLを Markdown リンク形式 `[ファイル名](ダウンロードURL)` で明記してください。（URLがない場合はファイル名のみ記載）\n"
                    "3. ナレッジに記載がない事項は憶測で回答せず、「提供されたナレッジに記載がありません」と明記してください。"
                )
                rag_context_text = f"{rag_instruction}\n\n【提供された参考ナレッジ】\n" + "\n\n".join(context_chunks)

        except Exception as e:
            logger.warning("RAG retrieval skipped or failed in chat_completions: %s", e)

    effective_messages = prepare_chat_messages(
        messages=request.messages,
        api_key_system_prompt=api_key.system_prompt,
        rag_context_text=rag_context_text,
    )
    client = get_bedrock_runtime_client()

    if request.stream:
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
    else:
        try:
            result = call_converse(
                client=client,
                model_id=model_id,
                messages=effective_messages,
                temperature=request.temperature,
                max_tokens=request.max_tokens,
            )
            if citations:
                result["citations"] = citations
            return JSONResponse(content=result)
        except Exception as e:
            logger.error("Bedrock converse execution error: %s", e)
            raise HTTPException(
                status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
                detail={"error": {"message": str(e), "type": "api_error"}},
            )

