from fastapi import APIRouter, Depends, Request
from fastapi.responses import HTMLResponse
from fastapi.templating import Jinja2Templates
from sqlmodel import Session, func, select
from app.api.web.auth import get_user_viewable_groups, require_current_user
from app.core.config import settings
from app.core.database import get_session
from app.models.api_key import ApiKey
from app.models.knowledge import KnowledgeDocument
from app.models.user import User

router = APIRouter()
templates = Jinja2Templates(directory="app/templates")


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
        },
    )
