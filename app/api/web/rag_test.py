import logging
from typing import List, Optional
from fastapi import APIRouter, Depends, Form, Request
from fastapi.responses import HTMLResponse
from fastapi.templating import Jinja2Templates
from sqlmodel import Session, select
from app.api.web.auth import require_current_user
from app.core.config import settings
from app.core.database import get_session
from app.models.group import Group
from app.models.llm_model import LlmModel
from app.models.user import User
from app.services.bedrock_service import (
    call_converse,
    get_bedrock_agent_runtime_client,
    get_bedrock_runtime_client,
    retrieve_knowledge,
)

logger = logging.getLogger(__name__)

router = APIRouter()
templates = Jinja2Templates(directory="app/templates")


@router.get("/rag-test", response_class=HTMLResponse)
async def rag_test_page(
    request: Request,
    user: User = Depends(require_current_user),
    db: Session = Depends(get_session),
):
    db_groups = [g.name for g in db.exec(select(Group)).all()]
    user_groups = user.get_groups()
    groups = sorted(list(set(["public"] + user_groups + db_groups)))

    db_models = db.exec(select(LlmModel).where(LlmModel.is_active == True).order_by(LlmModel.created_at.asc())).all()
    models_list = [m.id for m in db_models] if db_models else settings.AVAILABLE_MODELS

    return templates.TemplateResponse(
        request=request,
        name="rag_test.html",
        context={
            "user": user,
            "groups": groups,
            "models": models_list,
            "kb_id": settings.BEDROCK_KB_ID,
        },
    )


@router.post("/rag-test/retrieve", response_class=HTMLResponse)
async def rag_retrieve_test(
    request: Request,
    query: str = Form(...),
    group: str = Form("public"),
    user: User = Depends(require_current_user),
):
    agent_client = get_bedrock_agent_runtime_client()
    allowed_groups = [group.strip()] if group.strip() else ["public"]

    results = []
    error_message = None
    try:
        results = retrieve_knowledge(
            agent_client=agent_client,
            kb_id=settings.BEDROCK_KB_ID,
            query=query,
            allowed_groups=allowed_groups,
            number_of_results=5,
        )
    except Exception as e:
        error_message = str(e)
        logger.error("RAG retrieve test error: %s", e)

    return templates.TemplateResponse(
        request=request,
        name="partials/rag_retrieve_results.html",
        context={
            "query": query,
            "group": group,
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
    user: User = Depends(require_current_user),
):
    agent_client = get_bedrock_agent_runtime_client()
    runtime_client = get_bedrock_runtime_client()

    allowed_groups = [group.strip()] if group.strip() else ["public"]

    # 1. ナレッジ検索 (Pre-filtering)
    try:
        results = retrieve_knowledge(
            agent_client=agent_client,
            kb_id=settings.BEDROCK_KB_ID,
            query=query,
            allowed_groups=allowed_groups,
            number_of_results=4,
        )
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
    for i, res in enumerate(results, start=1):
        content = res.get("content", "")
        metadata = res.get("metadata", {})
        location = res.get("location", {})
        s3_uri = location.get("s3Location", {}).get("uri", "")
        filename = metadata.get("filename") or (s3_uri.split("/")[-1] if s3_uri else f"Doc {i}")
        doc_group = metadata.get("group", "unknown")
        doc_tags = metadata.get("tags") or (metadata.get("tag_string").split(",") if metadata.get("tag_string") else [])

        context_parts.append(f"[{i}] File: {filename} (Group: {doc_group})\n{content}")
        citations.append({
            "index": i,
            "filename": filename,
            "group": doc_group,
            "tags": doc_tags,
            "s3_uri": s3_uri,
            "score": res.get("score", 0.0),
        })

    context_str = "\n\n".join(context_parts)
    system_instruction = (
        "You are an intelligent knowledge assistant. Answer the user question based on the provided retrieved context. "
        "Cite sources using [1], [2], etc. where appropriate. If the context does not contain enough information, state so clearly."
    )

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
        response = call_converse(
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
        },
    )
