from datetime import datetime, timedelta, timezone
import json
import logging
from typing import Optional
from fastapi import APIRouter, Cookie, Depends, Form, HTTPException, Request, Response, status
from fastapi.responses import HTMLResponse, RedirectResponse
from sqlmodel import Session, select
from app.core.config import settings
from app.core.database import get_session
from app.core.security import (
    generate_session_id,
    hash_password,
    verify_password,
)
from app.core.templates import templates
from app.models.session import SessionRecord
from app.models.user import User
from app.services.oidc_service import oidc_service

logger = logging.getLogger(__name__)

router = APIRouter()


def get_current_user_optional(
    request: Request,
    db: Session = Depends(get_session),
) -> Optional[User]:
    """Cookie から現在ログイン中のユーザーを取得する（未ログイン時は None）"""
    session_id = request.cookies.get(settings.SESSION_COOKIE_NAME)
    if not session_id:
        return None

    statement = (
        select(SessionRecord)
        .where(SessionRecord.session_id == session_id)
        .where(SessionRecord.expires_at > datetime.now(timezone.utc))
    )
    session_rec = db.exec(statement).first()
    if not session_rec:
        return None

    return db.get(User, session_rec.user_id)


def require_current_user(
    request: Request,
    user: Optional[User] = Depends(get_current_user_optional),
) -> User:
    """ログイン必須の依存関係"""
    if not user:
        if request.headers.get("HX-Request"):
            response = Response(status_code=status.HTTP_401_UNAUTHORIZED)
            response.headers["HX-Redirect"] = "/login"
            raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Unauthorized")
        raise HTTPException(
            status_code=status.HTTP_307_TEMPORARY_REDIRECT,
            headers={"Location": "/login"},
        )
    return user


def require_admin_user(
    user: User = Depends(require_current_user),
) -> User:
    """システム管理者 (admin ロール) 専用の依存関係"""
    if "admin" not in user.get_roles():
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="この操作を行うには管理者権限が必要です",
        )
    return user


def get_user_viewable_groups(user: User) -> Optional[list[str]]:
    """
    ユーザーが閲覧可能なグループ名リストを取得する。
    admin の場合は全グループ閲覧可能なため None を返却する。
    一般ユーザーの場合は ['public'] + 自身所属グループ を返却する。
    """
    if "admin" in user.get_roles():
        return None
    return list(set(["public"] + user.get_groups()))


def can_user_upload_to_group(user: User, group_name: str) -> bool:
    """
    ユーザーが指定グループへアップロード可能かを判定する。
    admin: 全グループおよび public へアップロード可能。
    一般ユーザー: 自身が所属するグループのみアップロード可能（public への登録は管理者のみ）。
    """
    if "admin" in user.get_roles():
        return True
    clean = group_name.strip().lower()
    if clean == "public":
        return False
    return clean in user.get_groups()


def can_user_download_document(user: User, doc_group: str) -> bool:
    """
    ユーザーが指定ドキュメント（のグループ）をダウンロード可能かを判定する。
    admin: 全ドキュメント可能。
    一般ユーザー: public または自身の所属グループのみ可能。
    """
    if "admin" in user.get_roles():
        return True
    clean = doc_group.strip().lower()
    return clean == "public" or clean in user.get_groups()


def create_user_session(
    db: Session,
    user: User,
    response: Response,
    raw_id_token: Optional[str] = None,
) -> str:
    """
    セッションIDを新規生成（セッション固定攻撃対策）し、DBへ保存してCookieを発行する。
    """
    new_session_id = generate_session_id()
    expires_at = datetime.now(timezone.utc) + timedelta(seconds=settings.SESSION_MAX_AGE_SECONDS)

    session_rec = SessionRecord(
        session_id=new_session_id,
        user_id=user.id,
        raw_id_token=raw_id_token,
        expires_at=expires_at,
    )
    db.add(session_rec)
    db.commit()

    response.set_cookie(
        key=settings.SESSION_COOKIE_NAME,
        value=new_session_id,
        max_age=settings.SESSION_MAX_AGE_SECONDS,
        httponly=True,
        samesite=settings.SESSION_COOKIE_SAMESITE,
        secure=settings.SESSION_COOKIE_SECURE,
        path="/",
    )
    return new_session_id


@router.get("/login", response_class=HTMLResponse)
async def login_page(
    request: Request,
    user: Optional[User] = Depends(get_current_user_optional),
):
    if user:
        return RedirectResponse(url="/", status_code=status.HTTP_302_FOUND)

    return templates.TemplateResponse(
        request=request,
        name="login.html",
        context={
            "oidc_enabled": oidc_service.is_configured(),
            "error": None,
        },
    )


@router.post("/login", response_class=HTMLResponse)
async def login_submit(
    request: Request,
    response: Response,
    username: str = Form(...),
    password: str = Form(...),
    db: Session = Depends(get_session),
):
    user = db.exec(select(User).where(User.username == username)).first()

    if not user or not user.password_hash or not verify_password(password, user.password_hash):
        return templates.TemplateResponse(
            request=request,
            name="login.html",
            context={
                "oidc_enabled": oidc_service.is_configured(),
                "error": "ユーザー名またはパスワードが無効です。",
            },
            status_code=status.HTTP_401_UNAUTHORIZED,
        )

    redirect_resp = RedirectResponse(url="/", status_code=status.HTTP_302_FOUND)
    create_user_session(db, user, redirect_resp)
    return redirect_resp


@router.get("/auth/oidc/login")
async def oidc_login_redirect():
    """mini-id OIDC 認可エンドポイントへ PKCE 付きでリダイレクト"""
    if not oidc_service.is_configured():
        raise HTTPException(status_code=400, detail="OIDC is not configured")

    auth_url, state, nonce, code_verifier = oidc_service.create_authorization_request()

    resp = RedirectResponse(url=auth_url, status_code=status.HTTP_302_FOUND)
    # state, nonce, code_verifier を短時間有効な Cookie で一時保持
    cookie_args = {
        "httponly": True,
        "samesite": "lax",
        "secure": settings.SESSION_COOKIE_SECURE,
        "max_age": 600,
        "path": "/",
    }
    resp.set_cookie("oidc_state", state, **cookie_args)
    resp.set_cookie("oidc_nonce", nonce, **cookie_args)
    resp.set_cookie("oidc_verifier", code_verifier, **cookie_args)
    return resp


@router.get("/auth/oidc/callback")
async def oidc_callback(
    request: Request,
    code: Optional[str] = None,
    state: Optional[str] = None,
    error: Optional[str] = None,
    db: Session = Depends(get_session),
):
    if error:
        return templates.TemplateResponse(
            request=request,
            name="login.html",
            context={
                "oidc_enabled": oidc_service.is_configured(),
                "error": f"OIDC 認証エラー: {error}",
            },
            status_code=status.HTTP_400_BAD_REQUEST,
        )

    saved_state = request.cookies.get("oidc_state")
    saved_verifier = request.cookies.get("oidc_verifier")
    saved_nonce = request.cookies.get("oidc_nonce")

    if not state or not saved_state or state != saved_state:
        raise HTTPException(status_code=400, detail="Invalid OIDC state (CSRF detected)")

    if not code or not saved_verifier:
        raise HTTPException(status_code=400, detail="Missing code or verifier")

    try:
        token_data = await oidc_service.exchange_code(code, saved_verifier)
    except Exception as e:
        logger.error("Token exchange failed: %s", e)
        raise HTTPException(status_code=400, detail="Failed to exchange authorization code")

    raw_id_token = token_data.get("id_token")
    # sub, username, groups の抽出
    user_info = token_data.get("userinfo") or {}
    if not user_info and raw_id_token:
        # ペイロードの簡易デコード (Authlib 等の検証済みクレーム)
        try:
            import base64
            payload_part = raw_id_token.split(".")[1]
            padded = payload_part + "=" * (-len(payload_part) % 4)
            user_info = json.loads(base64.urlsafe_b64decode(padded).decode("utf-8"))
        except Exception:
            user_info = {}

    sub = user_info.get("sub")
    if not sub:
        raise HTTPException(status_code=400, detail="ID token missing 'sub' claim")

    username = user_info.get("preferred_username") or user_info.get("username") or f"oidc_{sub[:8]}"
    email = user_info.get("email")
    display_name = user_info.get("name") or username
    groups = user_info.get("groups") or ["user"]
    if isinstance(groups, str):
        groups = [groups]

    # sub (UUID) を主識別キーとして検索
    user = db.exec(select(User).where(User.oidc_sub == sub)).first()
    if not user:
        user = User(
            auth_type="oidc",
            oidc_sub=sub,
            username=username,
            email=email,
            display_name=display_name,
            roles=json.dumps(groups),
        )
        db.add(user)
        db.commit()
        db.refresh(user)
    else:
        # ロール/属性の同期
        user.display_name = display_name
        user.email = email
        user.roles = json.dumps(groups)
        user.updated_at = datetime.now(timezone.utc)
        db.add(user)
        db.commit()
        db.refresh(user)

    redirect_resp = RedirectResponse(url="/", status_code=status.HTTP_302_FOUND)
    create_user_session(db, user, redirect_resp, raw_id_token=raw_id_token)

    # 一時 Cookie の削除
    redirect_resp.delete_cookie("oidc_state", path="/")
    redirect_resp.delete_cookie("oidc_nonce", path="/")
    redirect_resp.delete_cookie("oidc_verifier", path="/")
    return redirect_resp


@router.get("/logout")
@router.post("/logout")
async def logout(
    request: Request,
    db: Session = Depends(get_session),
):
    session_id = request.cookies.get(settings.SESSION_COOKIE_NAME)
    raw_id_token = None
    if session_id:
        session_rec = db.get(SessionRecord, session_id)
        if session_rec:
            raw_id_token = session_rec.raw_id_token
            db.delete(session_rec)
            db.commit()

    # RP-Initiated Logout URL 構築
    post_logout_url = str(request.base_url) + "login"
    logout_url = "/login"
    if raw_id_token and oidc_service.is_configured():
        rp_url = oidc_service.build_end_session_url(
            id_token_hint=raw_id_token,
            post_logout_redirect_uri=post_logout_url,
        )
        if rp_url:
            logout_url = rp_url

    resp = RedirectResponse(url=logout_url, status_code=status.HTTP_302_FOUND)
    resp.delete_cookie(settings.SESSION_COOKIE_NAME, path="/")
    return resp
