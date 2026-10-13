import logging
from typing import Optional
from fastapi import APIRouter, Depends, Form, HTTPException, Request, Response, status
from fastapi.responses import HTMLResponse
from fastapi.templating import Jinja2Templates
from sqlmodel import Session, select
from app.api.web.auth import require_admin_user
from app.core.database import get_session
from app.models.llm_model import LlmModel
from app.models.user import User

logger = logging.getLogger(__name__)

router = APIRouter()
templates = Jinja2Templates(directory="app/templates")


@router.get("/models", response_class=HTMLResponse)
async def models_list(
    request: Request,
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    """LLMモデル一覧・管理画面を表示する"""
    models = db.exec(select(LlmModel).order_by(LlmModel.created_at.asc())).all()
    return templates.TemplateResponse(
        request=request,
        name="models.html",
        context={
            "user": user,
            "models": models,
            "message": None,
        },
    )


@router.post("/models/create", response_class=HTMLResponse)
async def model_create(
    request: Request,
    id: str = Form(...),
    name: str = Form(...),
    description: Optional[str] = Form(None),
    provider: str = Form("bedrock"),
    is_active: bool = Form(True),
    is_default: bool = Form(False),
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    """新しいLLMモデルを登録する"""
    model_id = id.strip()
    model_name = name.strip()

    existing = db.get(LlmModel, model_id)
    if existing:
        models = db.exec(select(LlmModel).order_by(LlmModel.created_at.asc())).all()
        return templates.TemplateResponse(
            request=request,
            name="partials/models_section.html",
            context={
                "user": user,
                "models": models,
                "error": f"モデルID「{model_id}」は既に登録されています。",
            },
        )

    # もしデフォルトに設定する場合、他のモデルのデフォルトを解除
    if is_default:
        all_models = db.exec(select(LlmModel)).all()
        for m in all_models:
            m.is_default = False
            db.add(m)

    new_model = LlmModel(
        id=model_id,
        name=model_name,
        description=description.strip() if description else None,
        provider=provider.strip() or "bedrock",
        is_active=is_active,
        is_default=is_default,
    )
    db.add(new_model)
    db.commit()

    models = db.exec(select(LlmModel).order_by(LlmModel.created_at.asc())).all()
    return templates.TemplateResponse(
        request=request,
        name="partials/models_section.html",
        context={
            "user": user,
            "models": models,
            "success": f"モデル「{model_name}」を追加しました。",
        },
    )


@router.post("/models/{model_id:path}/toggle", response_class=HTMLResponse)
async def model_toggle_active(
    request: Request,
    model_id: str,
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    """モデルの有効/無効状態を切り替える"""
    model = db.get(LlmModel, model_id)
    if not model:
        raise HTTPException(status_code=404, detail="モデルが見つかりません")

    model.is_active = not model.is_active
    db.add(model)
    db.commit()

    models = db.exec(select(LlmModel).order_by(LlmModel.created_at.asc())).all()
    return templates.TemplateResponse(
        request=request,
        name="partials/models_section.html",
        context={"user": user, "models": models},
    )


@router.post("/models/{model_id:path}/default", response_class=HTMLResponse)
async def model_set_default(
    request: Request,
    model_id: str,
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    """デフォルトモデルに設定する"""
    target_model = db.get(LlmModel, model_id)
    if not target_model:
        raise HTTPException(status_code=404, detail="モデルが見つかりません")

    all_models = db.exec(select(LlmModel)).all()
    for m in all_models:
        m.is_default = (m.id == model_id)
        db.add(m)
    db.commit()

    models = db.exec(select(LlmModel).order_by(LlmModel.created_at.asc())).all()
    return templates.TemplateResponse(
        request=request,
        name="partials/models_section.html",
        context={"user": user, "models": models},
    )


@router.delete("/models/{model_id:path}")
async def model_delete(
    model_id: str,
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    """モデルを削除する"""
    model = db.get(LlmModel, model_id)
    if not model:
        raise HTTPException(status_code=404, detail="モデルが見つかりません")

    db.delete(model)
    db.commit()
    return Response(content="", status_code=status.HTTP_200_OK)

