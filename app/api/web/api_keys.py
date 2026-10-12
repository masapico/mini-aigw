import json
import logging
from typing import List, Optional
from fastapi import APIRouter, Depends, Form, HTTPException, Request, Response, status
from fastapi.responses import HTMLResponse
from fastapi.templating import Jinja2Templates
from sqlmodel import Session, select
from app.api.web.auth import require_current_user
from app.core.database import get_session
from app.core.security import generate_api_key
from app.models.api_key import ApiKey
from app.models.group import Group
from app.models.user import User

logger = logging.getLogger(__name__)

router = APIRouter()
templates = Jinja2Templates(directory="app/templates")


@router.get("/api-keys", response_class=HTMLResponse)
async def api_keys_list(
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    keys = db.exec(select(ApiKey).order_by(ApiKey.created_at.desc())).all()
    db_groups = [g.name for g in db.exec(select(Group)).all()]
    user_groups = user.get_groups()
    groups = sorted(list(set(["public"] + user_groups + db_groups)))

    return templates.TemplateResponse(
        request=request,
        name="api_keys.html",
        context={
            "user": user,
            "keys": keys,
            "available_groups": groups,
            "new_raw_key": None,
        },
    )


@router.post("/api-keys/create", response_class=HTMLResponse)
async def api_key_create(
    request: Request,
    name: str = Form(...),
    allowed_groups: List[str] = Form(...),
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    raw_key, key_hash, prefix = generate_api_key()

    clean_groups = [g.strip() for g in allowed_groups if g.strip()]
    if not clean_groups:
        clean_groups = ["public"]

    api_key_obj = ApiKey(
        user_id=user.id,
        name=name.strip(),
        key_hash=key_hash,
        prefix=prefix,
        allowed_groups=json.dumps(clean_groups),
    )
    db.add(api_key_obj)
    db.commit()
    db.refresh(api_key_obj)

    keys = db.exec(select(ApiKey).order_by(ApiKey.created_at.desc())).all()

    return templates.TemplateResponse(
        request=request,
        name="partials/api_keys_section.html",
        context={
            "user": user,
            "keys": keys,
            "new_raw_key": raw_key,
            "new_key_name": name,
        },
    )


@router.delete("/api-keys/{key_id}")
async def api_key_delete(
    key_id: str,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    api_key = db.get(ApiKey, key_id)
    if not api_key:
        raise HTTPException(status_code=404, detail="API Key not found")

    db.delete(api_key)
    db.commit()
    return Response(content="", status_code=status.HTTP_200_OK)
