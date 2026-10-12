import time
from typing import Any, Dict
from fastapi import APIRouter
from app.core.config import settings

router = APIRouter()


@router.get("/models")
async def list_models() -> Dict[str, Any]:
    """
    OpenAI 互換の利用可能モデル一覧を返却する。
    """
    now = int(time.time())
    data = [
        {
            "id": model_id,
            "object": "model",
            "created": now,
            "owned_by": "bedrock",
        }
        for model_id in settings.AVAILABLE_MODELS
    ]
    return {
        "object": "list",
        "data": data,
    }

