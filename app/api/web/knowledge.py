import json
import logging
import urllib.parse
import uuid
from typing import Any, Dict, List, Optional, Tuple
from fastapi import APIRouter, Depends, File, Form, HTTPException, Request, Response, UploadFile, status
from fastapi.responses import HTMLResponse, RedirectResponse, StreamingResponse
from fastapi.templating import Jinja2Templates
from sqlmodel import Session, select
from app.api.web.auth import (
    can_user_download_document,
    can_user_upload_to_group,
    get_user_viewable_groups,
    require_admin_user,
    require_current_user,
)
from app.core.config import settings
from app.core.database import get_session
from app.models.group import Group
from app.models.knowledge import KnowledgeDocument
from app.models.user import User
from app.services.knowledge_service import (
    delete_knowledge_document,
    get_bedrock_agent_client,
    get_document_stream,
    get_latest_ingestion_job,
    get_s3_client,
    trigger_ingestion_job,
    upload_document_with_metadata,
)

logger = logging.getLogger(__name__)

router = APIRouter()
templates = Jinja2Templates(directory="app/templates")


def _get_filtered_docs_and_uploaders(db: Session, user: User) -> Tuple[List[KnowledgeDocument], Dict[str, str]]:
    """ユーザーの権限に基づいて閲覧可能なドキュメントと、更新者マップを取得する"""
    viewable_groups = get_user_viewable_groups(user)
    if viewable_groups is None:
        statement = select(KnowledgeDocument).order_by(KnowledgeDocument.created_at.desc())
    else:
        statement = (
            select(KnowledgeDocument)
            .where(KnowledgeDocument.group_name.in_(viewable_groups))
            .order_by(KnowledgeDocument.created_at.desc())
        )
    docs = db.exec(statement).all()

    uploader_ids = list(set([d.uploader_id for d in docs if d.uploader_id]))
    uploader_map: Dict[str, str] = {}
    if uploader_ids:
        users = db.exec(select(User).where(User.id.in_(uploader_ids))).all()
        uploader_map = {u.id: (u.display_name or u.username) for u in users}

    return docs, uploader_map


def _get_available_groups_for_upload(db: Session, user: User) -> List[Group]:
    """ユーザーがアップロード時に選択可能なグループ一覧を取得する"""
    if "admin" in user.get_roles():
        return db.exec(select(Group).order_by(Group.name.asc())).all()

    user_groups = user.get_groups()
    if not user_groups:
        return []
    return db.exec(
        select(Group).where(Group.name.in_(user_groups)).order_by(Group.name.asc())
    ).all()


def _get_latest_kb_job() -> Optional[Dict[str, Any]]:
    """最新の Bedrock Ingestion Job 情報を安全に取得する"""
    if not settings.BEDROCK_KB_ID or not settings.BEDROCK_DATA_SOURCE_ID:
        return None
    try:
        bedrock_agent = get_bedrock_agent_client()
        return get_latest_ingestion_job(
            bedrock_agent=bedrock_agent,
            kb_id=settings.BEDROCK_KB_ID,
            data_source_id=settings.BEDROCK_DATA_SOURCE_ID,
        )
    except Exception as e:
        logger.warning("Could not fetch latest ingestion job: %s", e)
        return None


@router.get("/knowledge", response_class=HTMLResponse)
async def knowledge_list(
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    docs, uploader_map = _get_filtered_docs_and_uploaders(db, user)
    groups = _get_available_groups_for_upload(db, user)
    latest_job = _get_latest_kb_job()

    return templates.TemplateResponse(
        request=request,
        name="knowledge.html",
        context={
            "user": user,
            "documents": docs,
            "groups": groups,
            "uploader_map": uploader_map,
            "latest_sync_job": latest_job,
            "bucket": settings.KNOWLEDGE_S3_BUCKET,
            "kb_id": settings.BEDROCK_KB_ID,
            "data_source_id": settings.BEDROCK_DATA_SOURCE_ID,
            "is_admin": "admin" in user.get_roles(),
        },
    )


@router.post("/knowledge/upload")
async def knowledge_upload(
    request: Request,
    file: UploadFile = File(...),
    group_name: str = Form(...),
    tags: Optional[str] = Form(None),
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    group_clean = group_name.strip().lower()

    # 権限チェック: 所属外グループや一般ユーザーの public への登録を遮断
    if not can_user_upload_to_group(user, group_clean):
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail=f"グループ '{group_clean}' へのドキュメント登録権限がありません。（一般ユーザーは自身が所属するグループのみ登録可能です）",
        )

    prefix = "public" if group_clean == "public" else f"groups/{group_clean}"
    content = await file.read()
    file_size = len(content)
    filename = file.filename or "uploaded_file"
    content_type = file.content_type or "application/octet-stream"

    # タグのパースと正規化
    tag_list = []
    if tags:
        try:
            parsed = json.loads(tags)
            if isinstance(parsed, list):
                tag_list = [str(t).strip() for t in parsed if str(t).strip()]
        except Exception:
            pass
        if not tag_list:
            tag_list = [t.strip() for t in tags.split(",") if t.strip()]

    doc_id = str(uuid.uuid4())
    s3_key = f"{prefix}/{filename}"

    # 重複確認
    existing = db.exec(select(KnowledgeDocument).where(KnowledgeDocument.s3_key == s3_key)).first()
    if existing:
        doc = existing
        doc.file_size = file_size
        doc.content_type = content_type
        doc.group_name = group_clean
        doc.uploader_id = user.id
        doc.set_tags(tag_list)
        doc.sync_status = "PENDING"
    else:
        doc = KnowledgeDocument(
            id=doc_id,
            s3_key=s3_key,
            s3_bucket=settings.KNOWLEDGE_S3_BUCKET,
            filename=filename,
            file_size=file_size,
            content_type=content_type,
            group_name=group_clean,
            uploader_id=user.id,
            sync_status="PENDING",
            data_source_id=settings.BEDROCK_DATA_SOURCE_ID,
        )
        doc.set_tags(tag_list)
        db.add(doc)

    db.commit()
    db.refresh(doc)

    # S3 へ本体 & .metadata.json をアップロード（KB同期は管理者手動またはバッチへ委任）
    s3_client = get_s3_client()
    try:
        upload_document_with_metadata(
            s3_client=s3_client,
            bucket=settings.KNOWLEDGE_S3_BUCKET,
            prefix=prefix,
            filename=filename,
            content=content,
            group=group_clean,
            document_id=doc.id,
            content_type=content_type,
            tags=tag_list,
        )
        # S3 保管完了（KB同期待ちステータス）
        doc.sync_status = "S3_UPLOADED"
    except Exception as e:
        logger.error("Failed to upload document to S3: %s", e)
        doc.sync_status = "ERROR"

    db.add(doc)
    db.commit()
    db.refresh(doc)

    if request.headers.get("HX-Request"):
        docs, uploader_map = _get_filtered_docs_and_uploaders(db, user)
        return templates.TemplateResponse(
            request=request,
            name="partials/knowledge_table.html",
            context={
                "documents": docs,
                "user": user,
                "uploader_map": uploader_map,
            },
        )

    return RedirectResponse(url="/knowledge", status_code=status.HTTP_302_FOUND)


@router.post("/knowledge/sync")
async def knowledge_sync(
    request: Request,
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    """
    管理者専用: Bedrock Knowledge Base の Ingestion Job (データソース同期) を手動実行する。
    """
    if not settings.BEDROCK_KB_ID or not settings.BEDROCK_DATA_SOURCE_ID:
        raise HTTPException(status_code=400, detail="BEDROCK_KB_ID または BEDROCK_DATA_SOURCE_ID が未設定です")

    bedrock_agent = get_bedrock_agent_client()
    job_id = trigger_ingestion_job(
        bedrock_agent=bedrock_agent,
        kb_id=settings.BEDROCK_KB_ID,
        data_source_id=settings.BEDROCK_DATA_SOURCE_ID,
    )

    if not job_id:
        raise HTTPException(
            status_code=status.HTTP_409_CONFLICT,
            detail="KB同期ジョブの開始に失敗しました。すでに別の同期ジョブが実行中である可能性があります。",
        )

    latest_job = get_latest_ingestion_job(
        bedrock_agent=bedrock_agent,
        kb_id=settings.BEDROCK_KB_ID,
        data_source_id=settings.BEDROCK_DATA_SOURCE_ID,
    )

    if request.headers.get("HX-Request"):
        return templates.TemplateResponse(
            request=request,
            name="partials/knowledge_sync_status.html",
            context={
                "latest_sync_job": latest_job,
                "is_admin": True,
                "just_started_job_id": job_id,
            },
        )

    return RedirectResponse(url="/knowledge", status_code=status.HTTP_302_FOUND)


@router.get("/knowledge/{doc_id}/download")
async def knowledge_download(
    doc_id: str,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    """
    S3 からファイル原本を直接ストリーミング取得して返却する。
    閲覧可能グループに対する権限チェックを実施。
    """
    doc = db.get(KnowledgeDocument, doc_id)
    if not doc:
        raise HTTPException(status_code=404, detail="Document not found")

    if not can_user_download_document(user, doc.group_name):
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="このドキュメントをダウンロードする権限がありません。",
        )

    s3_client = get_s3_client()
    try:
        doc_stream = get_document_stream(s3_client, bucket=doc.s3_bucket, key=doc.s3_key)
    except Exception as e:
        logger.error("Failed to fetch document %s from S3: %s", doc.s3_key, e)
        raise HTTPException(status_code=404, detail=f"S3から原本を取得できませんでした: {e}")

    # 日本語ファイル名のエンコード対応
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


@router.delete("/knowledge/{doc_id}")
async def knowledge_delete(
    doc_id: str,
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    doc = db.get(KnowledgeDocument, doc_id)
    if not doc:
        raise HTTPException(status_code=404, detail="Document not found")

    # 権限チェック: 管理者、または自身がアップロードしたファイルのみ削除可能
    is_admin = "admin" in user.get_roles()
    if not is_admin and doc.uploader_id != user.id:
        raise HTTPException(status_code=403, detail="他者が登録したドキュメントは削除できません")

    s3_client = get_s3_client()
    bedrock_agent = get_bedrock_agent_client()

    success = delete_knowledge_document(
        db=db,
        s3_client=s3_client,
        bedrock_agent=bedrock_agent,
        kb_id=settings.BEDROCK_KB_ID,
        doc_id=doc_id,
        trigger_sync=False,  # 同期は手動またはバッチへ委任
    )

    if not success:
        raise HTTPException(status_code=404, detail="Document not found")

    return Response(content="", status_code=status.HTTP_200_OK)
