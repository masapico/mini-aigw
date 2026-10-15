import json
import logging
from typing import List, Optional
from fastapi import APIRouter, Depends, Form, HTTPException, Request, Response, status
from fastapi.responses import HTMLResponse, RedirectResponse
from sqlmodel import Session, select
from app.api.web.auth import require_admin_user, require_current_user
from app.core.database import get_session
from app.core.security import hash_password
from app.core.templates import templates
from app.models.group import Group
from app.models.session import SessionRecord
from app.models.user import User

logger = logging.getLogger(__name__)

router = APIRouter()


@router.get("/users", response_class=HTMLResponse)
async def users_page(
    request: Request,
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    users = db.exec(select(User).order_by(User.created_at.desc())).all()
    groups = db.exec(select(Group).order_by(Group.name.asc())).all()

    return templates.TemplateResponse(
        request=request,
        name="users.html",
        context={
            "user": user,
            "users": users,
            "groups": groups,
            "error": None,
        },
    )


@router.post("/users/create")
async def user_create(
    request: Request,
    username: str = Form(...),
    password: str = Form(...),
    display_name: Optional[str] = Form(None),
    email: Optional[str] = Form(None),
    role: str = Form("user"),
    selected_groups: List[str] = Form(default=[]),
    admin_user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    clean_username = username.strip()
    if not clean_username:
        raise HTTPException(status_code=400, detail="ユーザー名を入力してください")

    existing = db.exec(select(User).where(User.username == clean_username)).first()
    if existing:
        if request.headers.get("HX-Request"):
            return HTMLResponse(
                content="<div class='alert alert-danger py-2 small mb-0'>そのユーザー名は既に使用されています</div>",
                status_code=status.HTTP_400_BAD_REQUEST,
            )
        raise HTTPException(status_code=400, detail="そのユーザー名は既に使用されています")

    clean_role = "admin" if role == "admin" else "user"
    clean_groups = [g.strip() for g in selected_groups if g.strip()]

    new_user = User(
        auth_type="local",
        username=clean_username,
        password_hash=hash_password(password),
        display_name=display_name.strip() if display_name else clean_username,
        email=email.strip() if email else None,
        roles=json.dumps([clean_role]),
        groups=json.dumps(clean_groups, ensure_ascii=False),
    )
    db.add(new_user)
    db.commit()
    db.refresh(new_user)
    logger.info("Created user %s by admin %s", clean_username, admin_user.username)

    if request.headers.get("HX-Request"):
        users = db.exec(select(User).order_by(User.created_at.desc())).all()
        groups = db.exec(select(Group).order_by(Group.name.asc())).all()
        return templates.TemplateResponse(
            request=request,
            name="partials/users_table.html",
            context={"users": users, "groups": groups, "user": admin_user},
        )

    return RedirectResponse(url="/users", status_code=status.HTTP_302_FOUND)


@router.post("/users/{user_id}/edit")
async def user_edit(
    user_id: str,
    request: Request,
    display_name: Optional[str] = Form(None),
    email: Optional[str] = Form(None),
    password: Optional[str] = Form(None),
    role: str = Form("user"),
    selected_groups: List[str] = Form(default=[]),
    admin_user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    target_user = db.get(User, user_id)
    if not target_user:
        raise HTTPException(status_code=404, detail="ユーザーが見つかりません")

    clean_role = "admin" if role == "admin" else "user"
    clean_groups = [g.strip() for g in selected_groups if g.strip()]

    target_user.display_name = display_name.strip() if display_name else target_user.username
    target_user.email = email.strip() if email else None
    target_user.roles = json.dumps([clean_role])
    target_user.groups = json.dumps(clean_groups, ensure_ascii=False)

    if password and password.strip():
        target_user.password_hash = hash_password(password.strip())

    db.add(target_user)
    db.commit()
    db.refresh(target_user)
    logger.info("Updated user %s by admin %s", target_user.username, admin_user.username)

    if request.headers.get("HX-Request"):
        users = db.exec(select(User).order_by(User.created_at.desc())).all()
        groups = db.exec(select(Group).order_by(Group.name.asc())).all()
        return templates.TemplateResponse(
            request=request,
            name="partials/users_table.html",
            context={"users": users, "groups": groups, "user": admin_user},
        )

    return RedirectResponse(url="/users", status_code=status.HTTP_302_FOUND)


@router.delete("/users/{user_id}")
async def user_delete(
    user_id: str,
    admin_user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    if user_id == admin_user.id:
        raise HTTPException(status_code=400, detail="ログイン中の管理者自身を削除することはできません")

    target_user = db.get(User, user_id)
    if not target_user:
        raise HTTPException(status_code=404, detail="ユーザーが見つかりません")

    # 関連セッションレコードの削除
    sessions = db.exec(select(SessionRecord).where(SessionRecord.user_id == user_id)).all()
    for s in sessions:
        db.delete(s)

    db.delete(target_user)
    db.commit()
    logger.info("Deleted user %s by admin %s", target_user.username, admin_user.username)
    return Response(content="", status_code=status.HTTP_200_OK)


@router.post("/groups/create")
async def group_create(
    request: Request,
    name: str = Form(...),
    display_name: Optional[str] = Form(None),
    description: Optional[str] = Form(None),
    admin_user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    clean_name = name.strip().lower()
    if not clean_name:
        raise HTTPException(status_code=400, detail="グループ識別名を入力してください")

    if clean_name == "public":
        raise HTTPException(status_code=400, detail="'public' は共通予約グループ名のため追加できません")

    existing = db.exec(select(Group).where(Group.name == clean_name)).first()
    if existing:
        if request.headers.get("HX-Request"):
            return HTMLResponse(
                content="<div class='alert alert-danger py-2 small mb-0'>そのグループ名は既に使用されています</div>",
                status_code=status.HTTP_400_BAD_REQUEST,
            )
        raise HTTPException(status_code=400, detail="そのグループ名は既に使用されています")

    clean_display = display_name.strip() if display_name else clean_name

    new_group = Group(
        name=clean_name,
        display_name=clean_display,
        description=description.strip() if description else None,
    )
    db.add(new_group)
    db.commit()
    db.refresh(new_group)
    logger.info("Created group %s by admin %s", clean_name, admin_user.username)

    if request.headers.get("HX-Request"):
        groups = db.exec(select(Group).order_by(Group.name.asc())).all()
        return templates.TemplateResponse(
            request=request,
            name="partials/groups_table.html",
            context={"groups": groups, "user": admin_user},
        )

    return RedirectResponse(url="/users", status_code=status.HTTP_302_FOUND)


@router.post("/groups/{group_id}/edit")
async def group_edit(
    group_id: str,
    request: Request,
    display_name: str = Form(...),
    description: Optional[str] = Form(None),
    admin_user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    target_group = db.get(Group, group_id)
    if not target_group:
        raise HTTPException(status_code=404, detail="グループが見つかりません")

    clean_display = display_name.strip()
    if not clean_display:
        if request.headers.get("HX-Request"):
            return HTMLResponse(
                content="<div class='alert alert-danger py-2 small mb-0'>表示名を入力してください</div>",
                status_code=status.HTTP_400_BAD_REQUEST,
            )
        raise HTTPException(status_code=400, detail="表示名を入力してください")

    target_group.display_name = clean_display
    target_group.description = description.strip() if description else None

    db.add(target_group)
    db.commit()
    db.refresh(target_group)
    logger.info("Updated group %s (display: %s) by admin %s", target_group.name, clean_display, admin_user.username)

    if request.headers.get("HX-Request"):
        groups = db.exec(select(Group).order_by(Group.name.asc())).all()
        return templates.TemplateResponse(
            request=request,
            name="partials/groups_table.html",
            context={"groups": groups, "user": admin_user},
        )

    return RedirectResponse(url="/users", status_code=status.HTTP_302_FOUND)


@router.delete("/groups/{group_id}")
async def group_delete(
    group_id: str,
    admin_user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    target_group = db.get(Group, group_id)
    if not target_group:
        raise HTTPException(status_code=404, detail="グループが見つかりません")

    db.delete(target_group)
    db.commit()
    logger.info("Deleted group %s by admin %s", target_group.name, admin_user.username)
    return Response(content="", status_code=status.HTTP_200_OK)

