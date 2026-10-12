import base64
import json
import logging
import time
import uuid
from typing import Any, AsyncGenerator, Dict, List, Optional, Tuple
import boto3
from app.core.config import settings
from app.services.markitdown_service import extract_content_from_base64

logger = logging.getLogger(__name__)


def get_bedrock_runtime_client() -> Any:
    """Bedrock Runtime クライアントを取得する"""
    kwargs: Dict[str, Any] = {"region_name": settings.AWS_REGION}
    if settings.AWS_ENDPOINT_URL:
        kwargs["endpoint_url"] = settings.AWS_ENDPOINT_URL
    return boto3.client("bedrock-runtime", **kwargs)


def get_bedrock_agent_runtime_client() -> Any:
    """Bedrock Agent Runtime クライアントを取得する"""
    kwargs: Dict[str, Any] = {"region_name": settings.AWS_REGION}
    if settings.AWS_ENDPOINT_URL:
        kwargs["endpoint_url"] = settings.AWS_ENDPOINT_URL
    return boto3.client("bedrock-agent-runtime", **kwargs)


def _detect_image_format(header: str) -> Optional[str]:
    """Base64 Data URL または Content-Type から画像フォーマットを判定する"""
    header_lower = header.lower()
    if "image/png" in header_lower or header_lower.endswith(".png"):
        return "png"
    elif "image/jpeg" in header_lower or "image/jpg" in header_lower or header_lower.endswith((".jpg", ".jpeg")):
        return "jpeg"
    elif "image/gif" in header_lower or header_lower.endswith(".gif"):
        return "gif"
    elif "image/webp" in header_lower or header_lower.endswith(".webp"):
        return "webp"
    return None


def convert_openai_messages_to_bedrock(
    messages: List[Dict[str, Any]]
) -> Tuple[List[Dict[str, Any]], List[Dict[str, Any]]]:
    """
    OpenAI 形式の messages を Bedrock Converse API 形式の (system, messages) に変換する。
    添付ファイル (Base64) や画像ブロックの抽出・正規化を同時に行う。
    """
    system_prompts: List[Dict[str, Any]] = []
    bedrock_messages: List[Dict[str, Any]] = []

    for msg in messages:
        role = msg.get("role", "user")
        content = msg.get("content", "")

        if role == "system":
            if isinstance(content, str):
                system_prompts.append({"text": content})
            elif isinstance(content, list):
                for part in content:
                    if isinstance(part, dict) and "text" in part:
                        system_prompts.append({"text": part["text"]})
            continue

        # Bedrock Converse は user または assistant
        converse_role = "user" if role != "assistant" else "assistant"
        content_blocks: List[Dict[str, Any]] = []

        if isinstance(content, str):
            content_blocks.append({"text": content})
        elif isinstance(content, list):
            for part in content:
                if not isinstance(part, dict):
                    continue

                part_type = part.get("type", "text")
                if part_type == "text":
                    content_blocks.append({"text": part.get("text", "")})
                elif part_type == "image_url":
                    # 画像ブロック
                    img_data = part.get("image_url", {})
                    url = img_data.get("url", "")
                    if url.startswith("data:"):
                        header, b64_body = url.split(",", 1) if "," in url else ("", url)
                        fmt = _detect_image_format(header) or "jpeg"
                        try:
                            raw_bytes = base64.b64decode(b64_body)
                            content_blocks.append({
                                "image": {
                                    "format": fmt,
                                    "source": {"bytes": raw_bytes}
                                }
                            })
                        except Exception as e:
                            logger.error("Failed to decode image: %s", e)
                            content_blocks.append({"text": "[Image decode error]"})
                    else:
                        content_blocks.append({"text": f"[Image URL: {url}]"})
                elif part_type in ("file", "document"):
                    # 非画像ドキュメント (markitdown で Markdown 抽出)
                    b64_body = part.get("data") or part.get("content") or ""
                    filename = part.get("name") or part.get("filename") or "document.bin"
                    extracted_text = extract_content_from_base64(b64_body, filename)
                    content_blocks.append({
                        "text": f"--- Attached Document: {filename} ---\n{extracted_text}\n--- End of Document ---"
                    })
                elif "data" in part and ("name" in part or "filename" in part):
                    # 汎用 Base64 ファイル
                    b64_body = part["data"]
                    filename = part.get("name") or part.get("filename") or "file.bin"
                    fmt = _detect_image_format(filename)
                    if fmt:
                        try:
                            raw_bytes = base64.b64decode(b64_body)
                            content_blocks.append({
                                "image": {
                                    "format": fmt,
                                    "source": {"bytes": raw_bytes}
                                }
                            })
                        except Exception:
                            content_blocks.append({"text": f"[Image decode error: {filename}]"})
                    else:
                        extracted_text = extract_content_from_base64(b64_body, filename)
                        content_blocks.append({
                            "text": f"--- Attached Document: {filename} ---\n{extracted_text}\n--- End of Document ---"
                        })

        if not content_blocks:
            content_blocks.append({"text": ""})

        bedrock_messages.append({"role": converse_role, "content": content_blocks})

    return system_prompts, bedrock_messages


def call_converse(
    client: Any,
    model_id: str,
    messages: List[Dict[str, Any]],
    temperature: Optional[float] = None,
    max_tokens: Optional[int] = None,
) -> Dict[str, Any]:
    """
    Bedrock Converse API を非ストリーミングで呼び出し、OpenAI 互換の辞書形式に変換して返却する。
    """
    system_prompts, converse_messages = convert_openai_messages_to_bedrock(messages)

    inference_config: Dict[str, Any] = {}
    if temperature is not None:
        inference_config["temperature"] = float(temperature)
    if max_tokens is not None:
        inference_config["maxTokens"] = int(max_tokens)

    kwargs: Dict[str, Any] = {
        "modelId": model_id,
        "messages": converse_messages,
    }
    if system_prompts:
        kwargs["system"] = system_prompts
    if inference_config:
        kwargs["inferenceConfig"] = inference_config

    response = client.converse(**kwargs)

    # Bedrock レスポンスの抽出
    output_message = response.get("output", {}).get("message", {})
    content_list = output_message.get("content", [])
    response_text = "".join([c.get("text", "") for c in content_list if "text" in c])

    usage = response.get("usage", {})
    stop_reason = response.get("stopReason", "end_turn")
    finish_reason = "stop" if stop_reason == "end_turn" else stop_reason

    completion_id = f"chatcmpl-{uuid.uuid4().hex[:12]}"
    created_at = int(time.time())

    return {
        "id": completion_id,
        "object": "chat.completion",
        "created": created_at,
        "model": model_id,
        "choices": [
            {
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": response_text,
                },
                "finish_reason": finish_reason,
            }
        ],
        "usage": {
            "prompt_tokens": usage.get("inputTokens", 0),
            "completion_tokens": usage.get("outputTokens", 0),
            "total_tokens": usage.get("totalTokens", 0),
        },
    }


async def call_converse_stream(
    client: Any,
    model_id: str,
    messages: List[Dict[str, Any]],
    temperature: Optional[float] = None,
    max_tokens: Optional[int] = None,
) -> AsyncGenerator[str, None]:
    """
    Bedrock ConverseStream を呼び出し、OpenAI 互換の SSE チャンク文字列を順次 yield する。
    """
    system_prompts, converse_messages = convert_openai_messages_to_bedrock(messages)

    inference_config: Dict[str, Any] = {}
    if temperature is not None:
        inference_config["temperature"] = float(temperature)
    if max_tokens is not None:
        inference_config["maxTokens"] = int(max_tokens)

    kwargs: Dict[str, Any] = {
        "modelId": model_id,
        "messages": converse_messages,
    }
    if system_prompts:
        kwargs["system"] = system_prompts
    if inference_config:
        kwargs["inferenceConfig"] = inference_config

    response = client.converse_stream(**kwargs)
    stream = response.get("stream")

    completion_id = f"chatcmpl-{uuid.uuid4().hex[:12]}"
    created_at = int(time.time())

    # 初回ロール通知チャンク
    first_chunk = {
        "id": completion_id,
        "object": "chat.completion.chunk",
        "created": created_at,
        "model": model_id,
        "choices": [
            {
                "index": 0,
                "delta": {"role": "assistant", "content": ""},
                "finish_reason": None,
            }
        ],
    }
    yield f"data: {json.dumps(first_chunk, ensure_ascii=False)}\n\n"

    for event in stream:
        if "contentBlockDelta" in event:
            delta_text = event["contentBlockDelta"].get("delta", {}).get("text", "")
            if delta_text:
                chunk = {
                    "id": completion_id,
                    "object": "chat.completion.chunk",
                    "created": created_at,
                    "model": model_id,
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"content": delta_text},
                            "finish_reason": None,
                        }
                    ],
                }
                yield f"data: {json.dumps(chunk, ensure_ascii=False)}\n\n"
        elif "messageStop" in event:
            stop_reason = event["messageStop"].get("stopReason", "end_turn")
            finish_reason = "stop" if stop_reason == "end_turn" else stop_reason
            end_chunk = {
                "id": completion_id,
                "object": "chat.completion.chunk",
                "created": created_at,
                "model": model_id,
                "choices": [
                    {
                        "index": 0,
                        "delta": {},
                        "finish_reason": finish_reason,
                    }
                ],
            }
            yield f"data: {json.dumps(end_chunk, ensure_ascii=False)}\n\n"

    yield "data: [DONE]\n\n"


def build_retrieval_filter(allowed_groups: List[str]) -> Optional[Dict[str, Any]]:
    """
    S3 Vectors 用の Pre-filtering 条件オブジェクトを生成する。
    public グループと、ユーザーの許可グループを含める。
    """
    groups_to_include = list(set(["public"] + [g for g in allowed_groups if g]))
    if len(groups_to_include) == 1:
        return {"equals": {"key": "group", "value": groups_to_include[0]}}

    or_list = [{"equals": {"key": "group", "value": grp}} for grp in groups_to_include]
    return {"orAll": or_list}


def retrieve_knowledge(
    agent_client: Any,
    kb_id: str,
    query: str,
    allowed_groups: List[str],
    number_of_results: int = 5,
) -> List[Dict[str, Any]]:
    """
    Bedrock Knowledge Base (S3 Vectors) から pre-filtering を適用してナレッジを検索する。
    """
    retrieval_filter = build_retrieval_filter(allowed_groups)
    vector_search_config: Dict[str, Any] = {
        "numberOfResults": number_of_results,
    }
    if retrieval_filter:
        vector_search_config["filter"] = retrieval_filter

    kwargs: Dict[str, Any] = {
        "knowledgeBaseId": kb_id,
        "retrievalQuery": {"text": query},
        "retrievalConfiguration": {
            "vectorSearchConfiguration": vector_search_config
        },
    }

    try:
        response = agent_client.retrieve(**kwargs)
        results = response.get("retrievalResults", [])
        formatted = []
        for res in results:
            content = res.get("content", {}).get("text", "")
            location = res.get("location", {})
            metadata = res.get("metadata", {})
            score = res.get("score", 0.0)
            formatted.append({
                "content": content,
                "location": location,
                "metadata": metadata,
                "score": score,
            })
        return formatted
    except Exception as e:
        logger.error("Error retrieving knowledge from Bedrock KB: %s", e)
        return []

