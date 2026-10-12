import time
from typing import Any, Dict
from fastapi import APIRouter, Depends
from sqlmodel import Session, select
from app.core.config import settings
from app.core.database import get_session
from app.models.llm_model import LlmModel

router = APIRouter()


@router.get("/models")
async def list_models(db: Session = Depends(get_session)) -> Dict[str, Any]:
    """
    OpenAI 互換の利用可能モデル一覧を返却する。
    DBからアクティブなモデルを取得し、未登録の場合は設定のフォールバックを使用する。
    """
    now = int(time.time())
    db_models = db.exec(select(LlmModel).where(LlmModel.is_active == True)).all()

    if db_models:
        data = [
            {
                "id": m.id,
                "object": "model",
                "created": int(m.created_at.timestamp()) if m.created_at else now,
                "owned_by": m.provider,
            }
            for m in db_models
        ]
    else:
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

