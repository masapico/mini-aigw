import json
import logging
import urllib.parse
import uuid
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional, Tuple
from fastapi import APIRouter, Depends, File, Form, HTTPException, Request, Response, UploadFile, status
from fastapi.responses import HTMLResponse, RedirectResponse, StreamingResponse
from sqlalchemy import update
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
from app.core.templates import templates
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


PAGE_SIZE = 10


def _get_filter_groups(db: Session, user: User) -> List[str]:
    """検索フィルターのグループ選択肢一覧を取得する"""
    if "admin" in user.get_roles():
        groups = db.exec(select(Group).order_by(Group.name.asc())).all()
        names = [g.name for g in groups]
        if "public" not in names:
            names.insert(0, "public")
        return names

    user_groups = user.get_groups()
    names = ["public"]
    for g in user_groups:
        if g not in names:
            names.append(g)
    return names


def _get_filtered_docs_and_uploaders(
    db: Session,
    user: User,
    q: Optional[str] = None,
    group: Optional[str] = None,
    sync_status: Optional[str] = None,
    page: int = 1,
    page_size: int = PAGE_SIZE,
) -> Tuple[List[KnowledgeDocument], Dict[str, str], int, int, int]:
    """ユーザーの権限および検索フィルター・ページネーションに基づいてドキュメント一覧を取得する"""
    import math
    from sqlalchemy import func, or_

    viewable_groups = get_user_viewable_groups(user)
    conditions = []

    # 1. ユーザー閲覧権限
    if viewable_groups is not None:
        conditions.append(KnowledgeDocument.group_name.in_(viewable_groups))

    # 2. グループ絞り込み
    if group and group.strip():
        group_clean = group.strip()
        if viewable_groups is None or group_clean in viewable_groups:
            conditions.append(KnowledgeDocument.group_name == group_clean)
        else:
            # 権限外のグループが指定された場合はヒット0件
            conditions.append(KnowledgeDocument.group_name == "__NO_ACCESS__")

    # 3. 同期状態絞り込み
    if sync_status and sync_status.strip():
        conditions.append(KnowledgeDocument.sync_status == sync_status.strip())

    # 4. キーワード絞り込み（ファイル名 または タグ）
    if q and q.strip():
        q_clean = q.strip()
        conditions.append(
            or_(
                KnowledgeDocument.filename.ilike(f"%{q_clean}%"),
                KnowledgeDocument.tags.ilike(f"%{q_clean}%"),
            )
        )

    # 総件数カウント
    count_stmt = select(func.count(KnowledgeDocument.id))
    if conditions:
        count_stmt = count_stmt.where(*conditions)
    total_count = db.exec(count_stmt).one()

    total_pages = max(1, math.ceil(total_count / page_size))
    current_page = max(1, min(page, total_pages))
    offset = (current_page - 1) * page_size

    # ドキュメント取得
    stmt = select(KnowledgeDocument)
    if conditions:
        stmt = stmt.where(*conditions)
    stmt = stmt.order_by(KnowledgeDocument.created_at.desc()).offset(offset).limit(page_size)
    docs = db.exec(stmt).all()

    uploader_ids = list(set([d.uploader_id for d in docs if d.uploader_id]))
    uploader_map: Dict[str, str] = {}
    if uploader_ids:
        users = db.exec(select(User).where(User.id.in_(uploader_ids))).all()
        uploader_map = {u.id: (u.display_name or u.username) for u in users}

    return docs, uploader_map, total_count, total_pages, current_page


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


def _sync_completed_documents(db: Session, latest_job: Optional[Dict[str, Any]]) -> None:
    """
    最新の Bedrock Ingestion Job が正常完了 (COMPLETE) している場合、
    ジョブ開始日時以前に登録された未同期ドキュメント (S3_UPLOADED) を
    単一SQLバルククエリで一括して SYNCED に更新する。
    ジョブが FAILED や IN_PROGRESS の場合は更新せず安全に維持する。
    """
    if not latest_job or latest_job.get("status") != "COMPLETE":
        return

    job_time = latest_job.get("startedAt") or latest_job.get("updatedAt")
    if not job_time:
        return

    try:
        if not hasattr(job_time, "tzinfo") or job_time.tzinfo is None:
            utc_job_time = job_time.replace(tzinfo=timezone.utc)
        else:
            utc_job_time = job_time.astimezone(timezone.utc)

        # 1クエリで未同期ドキュメントを一括更新 (500件でも1〜2ms)
        db.exec(
            update(KnowledgeDocument)
            .where(KnowledgeDocument.sync_status == "S3_UPLOADED")
            .where(KnowledgeDocument.created_at <= utc_job_time)
            .values(sync_status="SYNCED")
        )
        db.commit()
    except Exception as e:
        logger.warning("Could not bulk update synced documents: %s", e)


@router.get("/knowledge", response_class=HTMLResponse)
async def knowledge_list(
    request: Request,
    q: Optional[str] = None,
    group: Optional[str] = None,
    status: Optional[str] = None,
    page: int = 1,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    latest_job = _get_latest_kb_job()
    _sync_completed_documents(db, latest_job)

    docs, uploader_map, total_count, total_pages, current_page = _get_filtered_docs_and_uploaders(
        db=db, user=user, q=q, group=group, sync_status=status, page=page
    )
    upload_groups = _get_available_groups_for_upload(db, user)
    filter_groups = _get_filter_groups(db, user)

    return templates.TemplateResponse(
        request=request,
        name="knowledge.html",
        context={
            "user": user,
            "documents": docs,
            "groups": upload_groups,
            "filter_groups": filter_groups,
            "uploader_map": uploader_map,
            "latest_sync_job": latest_job,
            "bucket": settings.KNOWLEDGE_S3_BUCKET,
            "kb_id": settings.BEDROCK_KB_ID,
            "data_source_id": settings.BEDROCK_DATA_SOURCE_ID,
            "is_admin": "admin" in user.get_roles(),
            "q": q or "",
            "selected_group": group or "",
            "selected_status": status or "",
            "page": current_page,
            "total_pages": total_pages,
            "total_count": total_count,
            "page_size": PAGE_SIZE,
        },
    )


@router.get("/knowledge/table", response_class=HTMLResponse)
async def knowledge_table_partial(
    request: Request,
    q: Optional[str] = None,
    group: Optional[str] = None,
    status: Optional[str] = None,
    page: int = 1,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    """HTMX 部分更新用: ナレッジ一覧テーブルのみを返却する"""
    latest_job = _get_latest_kb_job()
    _sync_completed_documents(db, latest_job)
    docs, uploader_map, total_count, total_pages, current_page = _get_filtered_docs_and_uploaders(
        db=db, user=user, q=q, group=group, sync_status=status, page=page
    )

    return templates.TemplateResponse(
        request=request,
        name="partials/knowledge_table.html",
        context={
            "documents": docs,
            "user": user,
            "uploader_map": uploader_map,
            "q": q or "",
            "selected_group": group or "",
            "selected_status": status or "",
            "page": current_page,
            "total_pages": total_pages,
            "total_count": total_count,
            "page_size": PAGE_SIZE,
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
    max_bytes = settings.MAX_UPLOAD_FILE_SIZE_MB * 1024 * 1024
    if file_size > max_bytes:
        raise HTTPException(
            status_code=status.HTTP_413_REQUEST_ENTITY_TOO_LARGE,
            detail=f"ファイルサイズが上限 ({settings.MAX_UPLOAD_FILE_SIZE_MB}MB) を超えています。",
        )
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
        now = datetime.now(timezone.utc)
        doc.created_at = now
        doc.updated_at = now
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

    # S3 へ本体 & .metadata.json をアップロード（イベントループをブロックしないようスレッドプール実行）
    from fastapi.concurrency import run_in_threadpool

    s3_client = get_s3_client()
    try:
        await run_in_threadpool(
            upload_document_with_metadata,
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
        docs, uploader_map, total_count, total_pages, current_page = _get_filtered_docs_and_uploaders(
            db=db, user=user, page=1
        )
        return templates.TemplateResponse(
            request=request,
            name="partials/knowledge_table.html",
            context={
                "documents": docs,
                "user": user,
                "uploader_map": uploader_map,
                "q": "",
                "selected_group": "",
                "selected_status": "",
                "page": 1,
                "total_pages": total_pages,
                "total_count": total_count,
                "page_size": PAGE_SIZE,
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
    ジョブの開始が成功した時点ではジョブは STARTING/IN_PROGRESS であり、
    正常完了 (COMPLETE) が検知された際にドキュメントステータスが SYNCED に昇格します。
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


@router.get("/knowledge/sync/status", response_class=HTMLResponse)
async def knowledge_sync_status(
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    """
    HTMX ポーリング / 更新用: 最新の Bedrock Ingestion Job 同期ステータスバーHTMLを返却する。
    同期完了 (COMPLETE) 時はドキュメントテーブルの更新を促すイベントヘッダーを付与する。
    """
    latest_job = _get_latest_kb_job()
    _sync_completed_documents(db, latest_job)
    is_admin = "admin" in user.get_roles()

    response = templates.TemplateResponse(
        request=request,
        name="partials/knowledge_sync_status.html",
        context={
            "latest_sync_job": latest_job,
            "is_admin": is_admin,
            "just_started_job_id": None,
        },
    )
    if latest_job and latest_job.get("status") == "COMPLETE":
        response.headers["HX-Trigger"] = "refreshKnowledgeTable"
    return response


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
        trigger_sync=True,  # 削除時に即時にKB同期を発火
    )

    if not success:
        raise HTTPException(status_code=404, detail="Document not found")

    if request.headers.get("HX-Request"):
        latest_job = _get_latest_kb_job()
        _sync_completed_documents(db, latest_job)
        sync_status_response = templates.TemplateResponse(
            request=request,
            name="partials/knowledge_sync_status.html",
            context={
                "latest_sync_job": latest_job,
                "is_admin": is_admin,
            },
        )
        sync_status_html = sync_status_response.body.decode("utf-8")
        # HTMX の Out-of-Band (OOB) swap で #kb-sync-status-container を更新し、
        # メインターゲットの #doc-row-{doc_id} は空文字（outerHTML置換）で行を削除
        content = f'<div id="kb-sync-status-container" hx-swap-oob="innerHTML">{sync_status_html}</div>'
        return HTMLResponse(
            content=content,
            status_code=status.HTTP_200_OK,
            headers={"HX-Trigger": "refreshKbSyncStatus"},
        )

    return Response(content="", status_code=status.HTTP_200_OK)
