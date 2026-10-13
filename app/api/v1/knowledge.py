import logging
import urllib.parse
from typing import Optional
from fastapi import APIRouter, Depends, Header, HTTPException, Query, Request, status
from fastapi.responses import StreamingResponse
from sqlmodel import Session, select
from app.api.web.auth import can_user_download_document, get_current_user_optional
from app.core.config import settings
from app.core.database import get_session
from app.core.security import hash_api_key, verify_download_token
from app.models.api_key import ApiKey
from app.models.knowledge import KnowledgeDocument
from app.services.knowledge_service import get_document_stream, get_s3_client

logger = logging.getLogger(__name__)

router = APIRouter()


@router.get("/knowledge/download/{doc_id}")
async def download_knowledge_file(
    doc_id: str,
    request: Request,
    token: Optional[str] = Query(None, description="HMAC署名付きダウンロードトークン"),
    api_key_query: Optional[str] = Query(None, alias="api_key", description="APIキー (クエリパラメータ)"),
    authorization: Optional[str] = Header(None, description="Bearer APIキー (ヘッダー)"),
    db: Session = Depends(get_session),
):
    """
    mini-aigw 経由で S3 からナレッジ原本ドキュメントを安全にストリーミング配信する。
    クライアントは AWS S3 に直接アクセスすることなく、mini-aigw を通じてダウンロードする。

    認証・アクセス認可 (以下のいずれかを満たすこと):
    1. 有効な HMAC 署名付きダウンロードトークン (?token=...)
    2. ドキュメントの所属グループに対する閲覧権限を持つ API キー (ヘッダー Bearer または ?api_key=...)
    3. ドキュメントの閲覧権限を持つログインユーザーの Web セッション Cookie
    """
    doc = db.get(KnowledgeDocument, doc_id)
    if not doc:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Document not found")

    is_authorized = False

    # 1. 署名付き URL トークンによる検証
    if token:
        if verify_download_token(doc_id, token):
            is_authorized = True
        else:
            raise HTTPException(
                status_code=status.HTTP_403_FORBIDDEN,
                detail="ダウンロードトークンが無効または有効期限切れです",
            )

    # 2. API キーによる検証 (トークン未指定時)
    if not is_authorized:
        raw_key = None
        if authorization and authorization.startswith("Bearer "):
            raw_key = authorization.split(" ", 1)[1].strip()
        elif api_key_query:
            raw_key = api_key_query.strip()

        if raw_key:
            key_hash = hash_api_key(raw_key)
            api_key_obj = db.exec(select(ApiKey).where(ApiKey.key_hash == key_hash)).first()
            if not api_key_obj:
                raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Invalid API key")

            allowed_groups = api_key_obj.get_allowed_groups()
            if doc.group_name in allowed_groups or "public" in allowed_groups and doc.group_name == "public":
                is_authorized = True
            else:
                raise HTTPException(
                    status_code=status.HTTP_403_FORBIDDEN,
                    detail=f"APIキーにグループ '{doc.group_name}' のドキュメントへのアクセス権限がありません",
                )

    # 3. Web セッション Cookie による検証
    if not is_authorized:
        user = get_current_user_optional(request, db)
        if user and can_user_download_document(user, doc.group_name):
            is_authorized = True

    if not is_authorized:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="ダウンロード権限がありません。有効なトークンまたは API キーが必要です",
        )

    # S3 からストリーミング取得
    s3_client = get_s3_client()
    try:
        doc_stream = get_document_stream(s3_client, bucket=doc.s3_bucket, key=doc.s3_key)
    except Exception as e:
        logger.error("Failed to fetch document %s from S3: %s", doc.s3_key, e)
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"S3から原本ファイルを取得できませんでした: {e}",
        )

    encoded_filename = urllib.parse.quote(doc.filename)
    headers = {
        "Content-Disposition": f"attachment; filename*=UTF-8''{encoded_filename}",
    }
    if doc_stream.get("content_length"):
        headers["Content-Length"] = str(doc_stream["content_length"])

    return StreamingResponse(
        doc_stream["body"],
        media_type=doc_stream.get("content_type", doc.content_type),
        headers=headers,
    )
