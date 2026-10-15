import json
import logging
from typing import List, Optional
from fastapi import APIRouter, Depends, Form, HTTPException, Request, Response, status
from fastapi.responses import HTMLResponse
from sqlmodel import Session, select
from app.api.web.auth import require_admin_user
from app.core.database import get_session
from app.core.prompts import (
    KNOWLEDGE_GROUNDING_RULES_SUMMARY,
    KNOWLEDGE_TOOL_SYSTEM_PROMPT,
)
from app.core.security import generate_api_key
from app.core.templates import templates
from app.models.api_key import ApiKey
from app.models.group import Group
from app.models.llm_model import LlmModel
from app.models.user import User

logger = logging.getLogger(__name__)

router = APIRouter()


def get_available_groups_and_models(user: User, db: Session):
    db_groups = [g.name for g in db.exec(select(Group)).all()]
    user_groups = user.get_groups()
    groups = sorted(list(set(["public"] + user_groups + db_groups)))
    active_models = db.exec(
        select(LlmModel).where(LlmModel.is_active == True).order_by(LlmModel.created_at.asc())
    ).all()
    return groups, active_models


@router.get("/api-keys", response_class=HTMLResponse)
async def api_keys_list(
    request: Request,
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    keys = db.exec(select(ApiKey).order_by(ApiKey.created_at.desc())).all()
    groups, active_models = get_available_groups_and_models(user, db)

    return templates.TemplateResponse(
        request=request,
        name="api_keys.html",
        context={
            "user": user,
            "keys": keys,
            "available_groups": groups,
            "available_models": active_models,
            "new_raw_key": None,
            "builtin_knowledge_prompt": KNOWLEDGE_TOOL_SYSTEM_PROMPT,
            "builtin_grounding_rules": KNOWLEDGE_GROUNDING_RULES_SUMMARY,
        },
    )


@router.post("/api-keys/create", response_class=HTMLResponse)
async def api_key_create(
    request: Request,
    name: str = Form(...),
    allowed_groups: List[str] = Form(default=[]),
    model_id: Optional[str] = Form(None),
    system_prompt: Optional[str] = Form(None),
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    raw_key, key_hash, prefix = generate_api_key()

    clean_groups = [g.strip() for g in allowed_groups if g.strip()]

    clean_model_id = model_id.strip() if model_id and model_id.strip() else None
    clean_system_prompt = system_prompt.strip() if system_prompt and system_prompt.strip() else None

    api_key_obj = ApiKey(
        user_id=user.id,
        name=name.strip(),
        key_hash=key_hash,
        prefix=prefix,
        raw_key=raw_key,
        allowed_groups=json.dumps(clean_groups),
        model_id=clean_model_id,
        system_prompt=clean_system_prompt,
    )
    db.add(api_key_obj)
    db.commit()
    db.refresh(api_key_obj)

    keys = db.exec(select(ApiKey).order_by(ApiKey.created_at.desc())).all()
    groups, active_models = get_available_groups_and_models(user, db)

    return templates.TemplateResponse(
        request=request,
        name="partials/api_keys_section.html",
        context={
            "user": user,
            "keys": keys,
            "available_groups": groups,
            "available_models": active_models,
            "new_raw_key": raw_key,
            "new_key_name": name,
            "builtin_knowledge_prompt": KNOWLEDGE_TOOL_SYSTEM_PROMPT,
            "builtin_grounding_rules": KNOWLEDGE_GROUNDING_RULES_SUMMARY,
        },
    )


@router.post("/api-keys/{key_id}/edit", response_class=HTMLResponse)
async def api_key_edit(
    request: Request,
    key_id: str,
    name: str = Form(...),
    allowed_groups: List[str] = Form(default=[]),
    model_id: Optional[str] = Form(None),
    system_prompt: Optional[str] = Form(None),
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    api_key = db.get(ApiKey, key_id)
    if not api_key:
        raise HTTPException(status_code=404, detail="API Key not found")

    clean_groups = [g.strip() for g in allowed_groups if g.strip()]
    clean_model_id = model_id.strip() if model_id and model_id.strip() else None
    clean_system_prompt = system_prompt.strip() if system_prompt and system_prompt.strip() else None

    api_key.name = name.strip()
    api_key.allowed_groups = json.dumps(clean_groups)
    api_key.model_id = clean_model_id
    api_key.system_prompt = clean_system_prompt

    db.add(api_key)
    db.commit()
    db.refresh(api_key)

    keys = db.exec(select(ApiKey).order_by(ApiKey.created_at.desc())).all()
    groups, active_models = get_available_groups_and_models(user, db)

    return templates.TemplateResponse(
        request=request,
        name="partials/api_keys_section.html",
        context={
            "user": user,
            "keys": keys,
            "available_groups": groups,
            "available_models": active_models,
            "new_raw_key": None,
            "builtin_knowledge_prompt": KNOWLEDGE_TOOL_SYSTEM_PROMPT,
            "builtin_grounding_rules": KNOWLEDGE_GROUNDING_RULES_SUMMARY,
        },
    )


@router.delete("/api-keys/{key_id}")
async def api_key_delete(
    key_id: str,
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    api_key = db.get(ApiKey, key_id)
    if not api_key:
        raise HTTPException(status_code=404, detail="API Key not found")

    db.delete(api_key)
    db.commit()
    return Response(content="", status_code=status.HTTP_200_OK)
