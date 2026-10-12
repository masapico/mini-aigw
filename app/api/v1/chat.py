import logging
from typing import Any, Dict, List, Optional
from fastapi import APIRouter, Depends, Header, HTTPException, status
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
    get_bedrock_runtime_client,
)

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


@router.post("/chat/completions")
async def chat_completions(
    request: ChatCompletionRequest,
    api_key: ApiKey = Depends(verify_api_key),
):
    """
    OpenAI 互換のチャット補完エンドポイント。
    Bedrock Converse API へリクエストを中継し、通常または SSE ストリーミングレスポンスを返却する。
    """
    model_id = request.model or settings.DEFAULT_MODEL_ID
    client = get_bedrock_runtime_client()

    if request.stream:
        return StreamingResponse(
            call_converse_stream(
                client=client,
                model_id=model_id,
                messages=request.messages,
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
                messages=request.messages,
                temperature=request.temperature,
                max_tokens=request.max_tokens,
            )
            return JSONResponse(content=result)
        except Exception as e:
            logger.error("Bedrock converse execution error: %s", e)
            raise HTTPException(
                status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
                detail={"error": {"message": str(e), "type": "api_error"}},
            )
