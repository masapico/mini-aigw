import json
import logging
import urllib.parse
import uuid
from typing import Optional
from fastapi import APIRouter, Depends, File, Form, HTTPException, Request, Response, UploadFile, status
from fastapi.responses import HTMLResponse, RedirectResponse, StreamingResponse
from fastapi.templating import Jinja2Templates
from sqlmodel import Session, select
from app.api.web.auth import require_current_user
from app.core.config import settings
from app.core.database import get_session
from app.models.group import Group
from app.models.knowledge import KnowledgeDocument
from app.models.user import User
from app.services.knowledge_service import (
    delete_knowledge_document,
    get_bedrock_agent_client,
    get_document_stream,
    get_s3_client,
    trigger_ingestion_job,
    upload_document_with_metadata,
)

logger = logging.getLogger(__name__)

router = APIRouter()
templates = Jinja2Templates(directory="app/templates")


@router.get("/knowledge", response_class=HTMLResponse)
async def knowledge_list(
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    docs = db.exec(select(KnowledgeDocument).order_by(KnowledgeDocument.created_at.desc())).all()
    groups = db.exec(select(Group).order_by(Group.name.asc())).all()

    return templates.TemplateResponse(
        request=request,
        name="knowledge.html",
        context={
            "user": user,
            "documents": docs,
            "groups": groups,
            "bucket": settings.KNOWLEDGE_S3_BUCKET,
            "kb_id": settings.BEDROCK_KB_ID,
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
    group_clean = group_name.strip().lower() or "public"
    prefix = "public" if group_clean == "public" else f"groups/{group_clean}"
    content = await file.read()
    file_size = len(content)
    filename = file.filename or "uploaded_file"
    content_type = file.content_type or "application/octet-stream"

    # タグのパースと正規化
    tag_list = []
    if tags:
        # JSON 配列形式 または カンマ区切りの両方を処理
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

    # S3 へ本体 & .metadata.json をアップロード
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
        doc.sync_status = "SYNCED"
    except Exception as e:
        logger.error("Failed to upload document to S3: %s", e)
        doc.sync_status = "ERROR"

    db.add(doc)
    db.commit()
    db.refresh(doc)

    # Ingestion ジョブのトリガー
    if doc.sync_status == "SYNCED" and settings.BEDROCK_KB_ID and settings.BEDROCK_DATA_SOURCE_ID:
        bedrock_agent = get_bedrock_agent_client()
        trigger_ingestion_job(
            bedrock_agent=bedrock_agent,
            kb_id=settings.BEDROCK_KB_ID,
            data_source_id=settings.BEDROCK_DATA_SOURCE_ID,
        )

    if request.headers.get("HX-Request"):
        docs = db.exec(select(KnowledgeDocument).order_by(KnowledgeDocument.created_at.desc())).all()
        return templates.TemplateResponse(
            request=request,
            name="partials/knowledge_table.html",
            context={"documents": docs, "user": user},
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
    mini-aigw 内部でファイルを保持する必要はありません。
    """
    doc = db.get(KnowledgeDocument, doc_id)
    if not doc:
        raise HTTPException(status_code=404, detail="Document not found")

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
    )

    if not success:
        raise HTTPException(status_code=404, detail="Document not found")

    return Response(content="", status_code=status.HTTP_200_OK)
