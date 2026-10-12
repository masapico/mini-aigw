import logging
import uuid
from typing import Optional
from fastapi import APIRouter, Depends, File, Form, HTTPException, Request, Response, UploadFile, status
from fastapi.responses import HTMLResponse, RedirectResponse
from fastapi.templating import Jinja2Templates
from sqlmodel import Session, select
from app.api.web.auth import require_current_user
from app.core.config import settings
from app.core.database import get_session
from app.models.knowledge import KnowledgeDocument
from app.models.user import User
from app.services.knowledge_service import (
    delete_knowledge_document,
    get_bedrock_agent_client,
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
    return templates.TemplateResponse(
        request=request,
        name="knowledge.html",
        context={
            "user": user,
            "documents": docs,
            "bucket": settings.KNOWLEDGE_S3_BUCKET,
            "kb_id": settings.BEDROCK_KB_ID,
        },
    )


@router.post("/knowledge/upload")
async def knowledge_upload(
    request: Request,
    file: UploadFile = File(...),
    group_name: str = Form(...),
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    group_clean = group_name.strip().lower() or "public"
    prefix = "public" if group_clean == "public" else f"groups/{group_clean}"
    content = await file.read()
    file_size = len(content)
    filename = file.filename or "uploaded_file"
    content_type = file.content_type or "application/octet-stream"

    doc_id = str(uuid.uuid4())
    s3_key = f"{prefix}/{filename}"

    # 重複確認
    existing = db.exec(select(KnowledgeDocument).where(KnowledgeDocument.s3_key == s3_key)).first()
    if existing:
        doc = existing
        doc.file_size = file_size
        doc.content_type = content_type
        doc.group_name = group_clean
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
            context={"documents": docs},
        )

    return RedirectResponse(url="/knowledge", status_code=status.HTTP_302_FOUND)


@router.delete("/knowledge/{doc_id}")
async def knowledge_delete(
    doc_id: str,
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
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
