from fastapi import APIRouter, Depends, Request
from fastapi.responses import HTMLResponse
from sqlmodel import Session, func, select
from app.api.web.auth import get_user_viewable_groups, require_current_user
from app.core.config import settings
from app.core.database import get_session
from app.core.templates import templates
from app.models.api_key import ApiKey
from app.models.knowledge import KnowledgeDocument
from app.models.llm_model import LlmModel
from app.models.user import User

router = APIRouter()


@router.get("/", response_class=HTMLResponse)
async def dashboard_index(
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    is_admin = "admin" in user.get_roles()
    viewable_groups = get_user_viewable_groups(user)

    if viewable_groups is None:
        # 管理者: 全体集計
        doc_count = db.exec(select(func.count(KnowledgeDocument.id))).one()
        key_count = db.exec(select(func.count(ApiKey.id))).one()
        recent_docs = db.exec(
            select(KnowledgeDocument).order_by(KnowledgeDocument.created_at.desc()).limit(5)
        ).all()
    else:
        # 一般ユーザー: 所属グループ + public に絞り込み
        doc_count = db.exec(
            select(func.count(KnowledgeDocument.id)).where(KnowledgeDocument.group_name.in_(viewable_groups))
        ).one()
        key_count = 0
        recent_docs = db.exec(
            select(KnowledgeDocument)
            .where(KnowledgeDocument.group_name.in_(viewable_groups))
            .order_by(KnowledgeDocument.created_at.desc())
            .limit(5)
        ).all()

    # 有効な LLM モデルの集計およびデフォルトモデル名の特定
    active_models = db.exec(
        select(LlmModel).where(LlmModel.is_active == True)
    ).all()
    active_models_count = len(active_models) if active_models else len(settings.AVAILABLE_MODELS)

    default_model_obj = next((m for m in active_models if m.is_default), None)
    if default_model_obj:
        default_model_name = default_model_obj.name
    elif active_models:
        default_model_name = active_models[0].name
    else:
        # DB未登録時のフォールバック (settings.DEFAULT_MODEL_ID)
        parts = settings.DEFAULT_MODEL_ID.split(".")
        default_model_name = parts[1] if len(parts) > 1 else settings.DEFAULT_MODEL_ID

    return templates.TemplateResponse(
        request=request,
        name="dashboard.html",
        context={
            "user": user,
            "is_admin": is_admin,
            "doc_count": doc_count,
            "key_count": key_count,
            "recent_docs": recent_docs,
            "settings": settings,
            "active_models_count": active_models_count,
            "default_model_name": default_model_name,
        },
    )
