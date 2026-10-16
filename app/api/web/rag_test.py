import logging
from typing import List, Optional
from fastapi import APIRouter, Depends, Form, HTTPException, Request, status
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import HTMLResponse
from sqlmodel import Session, select
from app.api.web.auth import require_admin_user, require_current_user
from app.core.config import settings
from app.core.database import get_session
from app.core.templates import templates
from app.models.group import Group
from app.models.llm_model import LlmModel
from app.models.user import User
from app.core.prompts import RAG_TEST_SYSTEM_INSTRUCTION
from app.services.bedrock_service import (
    call_converse,
    filter_knowledge_results_by_score,
    get_bedrock_agent_runtime_client,
    get_bedrock_runtime_client,
    retrieve_knowledge,
)
from app.services.knowledge_service import (
    find_matching_metadata_from_query,
    get_available_tags_for_groups,
    is_chunk_from_deleted_document,
    resolve_document_and_download_url,
)

logger = logging.getLogger(__name__)

router = APIRouter()


@router.get("/rag-test", response_class=HTMLResponse)
async def rag_test_page(
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    is_admin = "admin" in user.get_roles()
    user_groups = user.get_groups()

    if is_admin:
        db_groups = [g.name for g in db.exec(select(Group)).all()]
        groups = sorted(list(set(["public"] + user_groups + db_groups)))
        available_tags = get_available_tags_for_groups(db, None)
    else:
        # 一般ユーザーは public と自身が所属するグループのみ選択可能
        groups = sorted(list(set(["public"] + user_groups)))
        available_tags = get_available_tags_for_groups(db, groups)

    db_models = db.exec(select(LlmModel).where(LlmModel.is_active == True).order_by(LlmModel.created_at.asc())).all()
    models_list = [m.id for m in db_models] if db_models else settings.AVAILABLE_MODELS

    return templates.TemplateResponse(
        request=request,
        name="rag_test.html",
        context={
            "user": user,
            "groups": groups,
            "available_tags": available_tags,
            "score_threshold": settings.RAG_SCORE_THRESHOLD,
            "models": models_list,
            "kb_id": settings.BEDROCK_KB_ID,
            "is_admin": is_admin,
        },
    )


@router.post("/rag-test/retrieve", response_class=HTMLResponse)
async def rag_retrieve_test(
    request: Request,
    query: str = Form(...),
    group: str = Form("public"),
    tag: Optional[str] = Form(None),
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    is_admin = "admin" in user.get_roles()
    clean_group = group.strip() if group.strip() else "public"
    clean_tag = tag.strip() if tag and tag.strip() else None

    # 権限チェック: 一般ユーザーは public または自身のグループのみ指定可能
    if not is_admin and clean_group != "public" and clean_group not in user.get_groups():
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail=f"グループ '{clean_group}' のナレッジを検索する権限がありません。",
        )

    agent_client = get_bedrock_agent_runtime_client()
    allowed_groups = [clean_group]

    matched_meta = find_matching_metadata_from_query(
        db=db,
        query=query,
        allowed_groups=allowed_groups,
    )
    extra_kwargs = {}
    if clean_tag:
        extra_kwargs["tags"] = [clean_tag]
    else:
        if matched_meta.get("tags"):
            extra_kwargs["matched_tags"] = matched_meta["tags"]
        if matched_meta.get("document_ids"):
            extra_kwargs["matched_document_ids"] = matched_meta["document_ids"]

    results = []
    error_message = None
    try:
        raw_results = await run_in_threadpool(
            retrieve_knowledge,
            agent_client=agent_client,
            kb_id=settings.BEDROCK_KB_ID,
            query=query,
            allowed_groups=allowed_groups,
            number_of_results=5,
            **extra_kwargs,
        )
        results = [
            r
            for r in raw_results
            if not is_chunk_from_deleted_document(
                db=db,
                s3_uri=r.get("location", {}).get("s3Location", {}).get("uri", ""),
                metadata=r.get("metadata", {}),
            )
        ]
        base_url = str(request.base_url)
        for res in results:
            s3_uri = res.get("location", {}).get("s3Location", {}).get("uri", "")
            metadata = res.get("metadata", {})
            doc_id, download_url = resolve_document_and_download_url(
                db=db,
                base_url=base_url,
                s3_uri=s3_uri,
                metadata=metadata,
            )
            res["document_id"] = doc_id
            res["download_url"] = download_url
    except Exception as e:
        error_message = str(e)
        logger.error("RAG retrieve test error: %s", e)

    return templates.TemplateResponse(
        request=request,
        name="partials/rag_retrieve_results.html",
        context={
            "query": query,
            "group": group,
            "selected_tag": clean_tag,
            "matched_tags": matched_meta.get("tags", []),
            "score_threshold": settings.RAG_SCORE_THRESHOLD,
            "results": results,
            "error": error_message,
        },
    )


@router.post("/rag-test/chat", response_class=HTMLResponse)
async def rag_chat_test(
    request: Request,
    query: str = Form(...),
    model_id: str = Form(settings.DEFAULT_MODEL_ID),
    group: str = Form("public"),
    tag: Optional[str] = Form(None),
    user: User = Depends(require_admin_user),
    db: Session = Depends(get_session),
):
    agent_client = get_bedrock_agent_runtime_client()
    runtime_client = get_bedrock_runtime_client()

    allowed_groups = [group.strip()] if group.strip() else ["public"]
    clean_tag = tag.strip() if tag and tag.strip() else None

    matched_meta = find_matching_metadata_from_query(
        db=db,
        query=query,
        allowed_groups=allowed_groups,
    )
    extra_kwargs = {}
    if clean_tag:
        extra_kwargs["tags"] = [clean_tag]
    else:
        if matched_meta.get("tags"):
            extra_kwargs["matched_tags"] = matched_meta["tags"]
        if matched_meta.get("document_ids"):
            extra_kwargs["matched_document_ids"] = matched_meta["document_ids"]

    filtered_out_count = 0
    max_raw_score = 0.0

    # 1. ナレッジ検索 (Pre-filtering)
    try:
        raw_results = await run_in_threadpool(
            retrieve_knowledge,
            agent_client=agent_client,
            kb_id=settings.BEDROCK_KB_ID,
            query=query,
            allowed_groups=allowed_groups,
            number_of_results=settings.RAG_NUMBER_OF_RESULTS,
            **extra_kwargs,
        )
        active_raw = [
            r
            for r in raw_results
            if not is_chunk_from_deleted_document(
                db=db,
                s3_uri=r.get("location", {}).get("s3Location", {}).get("uri", ""),
                metadata=r.get("metadata", {}),
            )
        ]
        if active_raw:
            max_raw_score = max(float(r.get("score", 0.0)) for r in active_raw)
        # スコア閾値による足切り (Score Filtering)
        results = filter_knowledge_results_by_score(active_raw, threshold=settings.RAG_SCORE_THRESHOLD)
        filtered_out_count = max(0, len(active_raw) - len(results))
    except Exception as e:
        error_message = str(e)
        logger.error("RAG retrieval error during chat test: %s", error_message)
        kb_hint = ""
        if "Incompatible configuration" in error_message:
            kb_hint = "\n\n※ 本システムは Amazon Bedrock のマネージド Knowledge Base（managedSearchConfiguration）専用です。"
        return templates.TemplateResponse(
            request=request,
            name="partials/rag_chat_response.html",
            context={
                "query": query,
                "answer": f"ナレッジ検索中にエラーが発生しました:\n{error_message}{kb_hint}",
                "citations": [],
                "model_id": model_id,
            },
        )

    # 2. コンテキスト注入プロンプト作成
    citations = []
    context_parts = []
    base_url = str(request.base_url)
    for i, res in enumerate(results, start=1):
        content = res.get("content", "")
        metadata = res.get("metadata", {})
        location = res.get("location", {})
        s3_uri = location.get("s3Location", {}).get("uri", "")
        filename = metadata.get("filename") or (s3_uri.split("/")[-1] if s3_uri else f"Doc {i}")
        doc_group = metadata.get("group", "unknown")
        doc_tags = metadata.get("tags") or (metadata.get("tag_string").split(",") if metadata.get("tag_string") else [])

        doc_id, download_url = resolve_document_and_download_url(
            db=db,
            base_url=base_url,
            s3_uri=s3_uri,
            metadata=metadata,
        )

        url_info = f"（ダウンロードURL: {download_url}）" if download_url else ""
        context_parts.append(f"[{i}] File: {filename} (Group: {doc_group}) {url_info}\n{content}")
        citations.append({
            "index": i,
            "filename": filename,
            "group": doc_group,
            "tags": doc_tags,
            "document_id": doc_id,
            "download_url": download_url,
            "s3_uri": s3_uri,
            "score": res.get("score", 0.0),
        })

    context_str = "\n\n".join(context_parts)
    system_instruction = RAG_TEST_SYSTEM_INSTRUCTION

    messages = [
        {"role": "system", "content": system_instruction},
        {
            "role": "user",
            "content": (
                f"Retrieved Knowledge Context:\n{context_str}\n\n"
                f"Question: {query}"
            ) if context_str else query,
        },
    ]

    try:
        response = await run_in_threadpool(
            call_converse,
            client=runtime_client,
            model_id=model_id,
            messages=messages,
            temperature=0.3,
        )
        answer = response.get("choices", [{}])[0].get("message", {}).get("content", "")
    except Exception as e:
        logger.error("Bedrock chat error: %s", e)
        answer = f"Error calling model: {e}"

    return templates.TemplateResponse(
        request=request,
        name="partials/rag_chat_response.html",
        context={
            "query": query,
            "answer": answer,
            "citations": citations,
            "model_id": model_id,
            "filtered_out_count": filtered_out_count,
            "max_raw_score": max_raw_score,
            "score_threshold": settings.RAG_SCORE_THRESHOLD,
        },
    )

