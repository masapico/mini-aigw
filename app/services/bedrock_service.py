import base64
import json
import logging
import re
import time
import uuid
from typing import Any, AsyncGenerator, Dict, List, Optional, Tuple
import boto3
from app.core.config import settings
from app.services.markitdown_service import extract_content_from_base64

logger = logging.getLogger(__name__)


def _format_content_block_for_debug(block: Dict[str, Any]) -> str:
    """デバッグ出力用にコンテンツブロックを整形する。添付ファイルはファイル名のみ、画像は要約表示する。"""
    if "image" in block:
        img_info = block["image"]
        img_fmt = img_info.get("format", "unknown")
        raw_bytes = img_info.get("source", {}).get("bytes", b"")
        byte_len = len(raw_bytes) if isinstance(raw_bytes, (bytes, bytearray)) else 0
        return f"[Attached Image: format={img_fmt}, size={byte_len} bytes]"

    if "text" in block:
        text = block.get("text", "")
        # markitdown 展開された添付ドキュメントブロックを [Attached Document: <filename>] に置換
        formatted_text = re.sub(
            r"--- Attached Document: (.+?) ---\n.*?\n--- End of Document ---",
            r"[Attached Document: \1]",
            text,
            flags=re.DOTALL,
        )
        return formatted_text

    if "toolUse" in block:
        tu = block["toolUse"]
        return f"[Tool Use: name={tu.get('name')}, id={tu.get('toolUseId')}, input={tu.get('input')}]"

    if "toolResult" in block:
        tr = block["toolResult"]
        return f"[Tool Result: id={tr.get('toolUseId')}, status={tr.get('status')}, content={tr.get('content')}]"

    return str(block)


def _log_ai_request(
    model_id: str,
    system_prompts: List[Dict[str, Any]],
    messages: List[Dict[str, Any]],
    inference_config: Dict[str, Any],
    tool_config: Optional[Dict[str, Any]] = None,
) -> None:
    """Bedrock AI へのリクエスト内容をコンソール出力する"""
    lines = [
        "",
        "=" * 40 + " [DEBUG] AI Request " + "=" * 40,
        f"Model: {model_id}",
    ]
    if inference_config:
        lines.append(f"Inference Config: {json.dumps(inference_config, ensure_ascii=False)}")
    if tool_config:
        lines.append(f"Tool Config: {json.dumps(tool_config, ensure_ascii=False)}")

    if system_prompts:
        lines.append("--- System Prompt ---")
        for s in system_prompts:
            lines.append(s.get("text", ""))

    lines.append("--- Messages ---")
    for msg in messages:
        role = msg.get("role", "unknown")
        content_blocks = msg.get("content", [])
        if isinstance(content_blocks, list):
            content_strs = [_format_content_block_for_debug(b) for b in content_blocks]
            content_display = "\n".join(content_strs)
        else:
            content_display = str(content_blocks)
        lines.append(f"[{role}]:\n{content_display}")

    lines.append("=" * 96)
    logger.info("\n".join(lines))


def _log_ai_response(
    model_id: str,
    response_text: str,
    usage: Optional[Dict[str, Any]] = None,
    stop_reason: Optional[str] = None,
    is_stream: bool = False,
) -> None:
    """Bedrock AI からの回答内容をコンソール出力する"""
    stream_suffix = " (Stream Completed)" if is_stream else ""
    lines = [
        "",
        "=" * 40 + f" [DEBUG] AI Response{stream_suffix} " + "=" * (40 - len(stream_suffix)),
        f"Model: {model_id}",
    ]
    if stop_reason:
        lines.append(f"Stop Reason: {stop_reason}")
    if usage:
        lines.append(
            f"Usage Tokens: Input={usage.get('inputTokens', 0)}, Output={usage.get('outputTokens', 0)}, Total={usage.get('totalTokens', 0)}"
        )
    lines.append("--- Content ---")
    lines.append(response_text)
    lines.append("=" * 96)
    logger.info("\n".join(lines))


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

                # Bedrock Converse 形式のツールブロック (toolUse, toolResult) はそのまま保持
                if "toolUse" in part or "toolResult" in part:
                    content_blocks.append(part)
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


def filter_knowledge_results_by_score(
    results: List[Dict[str, Any]],
    threshold: float,
) -> List[Dict[str, Any]]:
    """
    Bedrock Knowledge Base の検索結果から、類似度スコア (score) が threshold 以上のチャンクのみを抽出する。
    """
    return [r for r in results if float(r.get("score", 0.0)) >= float(threshold)]


def build_inference_config(
    temperature: Optional[float] = None,
    top_p: Optional[float] = None,
    max_tokens: Optional[int] = None,
) -> Dict[str, Any]:
    """
    OpenAI 互換パラメータを Bedrock Converse API の inferenceConfig にサニタイズ・マッピングする。
    - maxTokens: 1 〜 MAX_ALLOWED_TOKENS にクランプ。未指定時は DEFAULT_MAX_TOKENS を適用。
    - temperature: 0.0 〜 1.0 にクランプ。未指定時は DEFAULT_TEMPERATURE を適用。
    - topP: 0.0 〜 1.0 にクランプ。
    - 排他制御:
      Claude 等のモデルは temperature と topP の両方変更を嫌うため:
      - 両方指定され、片方が 1.0 (OpenAIデフォルト) の場合は非デフォルト側を採用
      - 両方とも 1.0 未満で指定された場合は temperature を優先
    """
    config: Dict[str, Any] = {}

    # 1. maxTokens の解決とクランプ
    effective_max_tokens = max_tokens if max_tokens is not None else settings.DEFAULT_MAX_TOKENS
    if effective_max_tokens is not None:
        capped_tokens = min(max(int(effective_max_tokens), 1), settings.MAX_ALLOWED_TOKENS)
        config["maxTokens"] = capped_tokens

    # 2. temperature と top_p のサニタイズ
    raw_temp_specified = temperature is not None
    raw_top_p_specified = top_p is not None

    sanitized_temp: Optional[float] = None
    if raw_temp_specified:
        sanitized_temp = round(max(0.0, min(float(temperature), 1.0)), 4)
    elif settings.DEFAULT_TEMPERATURE is not None:
        sanitized_temp = round(max(0.0, min(float(settings.DEFAULT_TEMPERATURE), 1.0)), 4)

    sanitized_top_p: Optional[float] = None
    if raw_top_p_specified:
        sanitized_top_p = round(max(0.0, min(float(top_p), 1.0)), 4)

    # 3. 排他制御 (Bedrock / Claude 等の仕様互換)
    if sanitized_temp is not None and sanitized_top_p is not None:
        temp_is_default = (temperature == 1.0)
        top_p_is_default = (top_p == 1.0)

        if top_p_is_default and not temp_is_default:
            config["temperature"] = sanitized_temp
        elif temp_is_default and not top_p_is_default:
            config["topP"] = sanitized_top_p
        elif temp_is_default and top_p_is_default:
            config["temperature"] = sanitized_temp
        else:
            # 両方ともデフォルト以外 (例: temp=1.8, top_p=0.9 や temp=0.7, top_p=0.8)
            logger.warning(
                "Both temperature (%s) and top_p (%s) explicitly specified. Prioritizing temperature for Converse API compatibility.",
                temperature,
                top_p,
            )
            config["temperature"] = sanitized_temp
    elif sanitized_temp is not None:
        config["temperature"] = sanitized_temp
    elif sanitized_top_p is not None:
        config["topP"] = sanitized_top_p

    return config


def call_converse(
    client: Any,
    model_id: str,
    messages: List[Dict[str, Any]],
    temperature: Optional[float] = None,
    top_p: Optional[float] = None,
    max_tokens: Optional[int] = None,
    tool_config: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    """
    Bedrock Converse API を非ストリーミングで呼び出し、OpenAI 互換の辞書形式に変換して返却する。
    tool_config が指定された場合、Tool Use (Function Calling) を有効化する。
    """
    system_prompts, converse_messages = convert_openai_messages_to_bedrock(messages)

    inference_config = build_inference_config(
        temperature=temperature,
        top_p=top_p,
        max_tokens=max_tokens,
    )

    kwargs: Dict[str, Any] = {
        "modelId": model_id,
        "messages": converse_messages,
    }
    if system_prompts:
        kwargs["system"] = system_prompts
    if inference_config:
        kwargs["inferenceConfig"] = inference_config
    if tool_config:
        kwargs["toolConfig"] = tool_config

    if settings.DEBUG:
        _log_ai_request(model_id, system_prompts, converse_messages, inference_config, tool_config)

    response = client.converse(**kwargs)

    # Bedrock レスポンスの抽出
    output_message = response.get("output", {}).get("message", {})
    content_list = output_message.get("content", [])
    response_text = "".join([c.get("text", "") for c in content_list if "text" in c])

    usage = response.get("usage", {})
    stop_reason = response.get("stopReason", "end_turn")
    finish_reason = "stop" if stop_reason == "end_turn" else stop_reason

    if settings.DEBUG:
        _log_ai_response(model_id, response_text, usage, stop_reason, is_stream=False)

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
        "stop_reason": stop_reason,
        "output_message": output_message,
    }


async def call_converse_stream(
    client: Any,
    model_id: str,
    messages: List[Dict[str, Any]],
    temperature: Optional[float] = None,
    top_p: Optional[float] = None,
    max_tokens: Optional[int] = None,
) -> AsyncGenerator[str, None]:
    """
    Bedrock ConverseStream を呼び出し、OpenAI 互換の SSE チャンク文字列を順次 yield する。
    """
    system_prompts, converse_messages = convert_openai_messages_to_bedrock(messages)

    inference_config = build_inference_config(
        temperature=temperature,
        top_p=top_p,
        max_tokens=max_tokens,
    )

    kwargs: Dict[str, Any] = {
        "modelId": model_id,
        "messages": converse_messages,
    }
    if system_prompts:
        kwargs["system"] = system_prompts
    if inference_config:
        kwargs["inferenceConfig"] = inference_config

    if settings.DEBUG:
        _log_ai_request(model_id, system_prompts, converse_messages, inference_config)

    response = client.converse_stream(**kwargs)
    stream = response.get("stream")

    completion_id = f"chatcmpl-{uuid.uuid4().hex[:12]}"
    created_at = int(time.time())
    accumulated_text: List[str] = []
    final_stop_reason: Optional[str] = None

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
                if settings.DEBUG:
                    accumulated_text.append(delta_text)
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
            final_stop_reason = stop_reason
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

    if settings.DEBUG:
        _log_ai_response(
            model_id=model_id,
            response_text="".join(accumulated_text),
            usage=None,
            stop_reason=final_stop_reason,
            is_stream=True,
        )

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
    Bedrock Knowledge Base (Managed) から pre-filtering を適用してナレッジを検索する。
    本システムはマネージド Knowledge Base (managedSearchConfiguration) を標準として使用する。
    """
    retrieval_filter = build_retrieval_filter(allowed_groups)
    managed_search_config: Dict[str, Any] = {
        "numberOfResults": number_of_results,
    }
    if retrieval_filter:
        managed_search_config["filter"] = retrieval_filter

    kwargs: Dict[str, Any] = {
        "knowledgeBaseId": kb_id,
        "retrievalQuery": {"text": query},
        "retrievalConfiguration": {
            "managedSearchConfiguration": managed_search_config
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
        err_msg = str(e)
        if "Incompatible configuration" in err_msg:
            logger.error(
                "Bedrock Knowledge Base configuration incompatibility detected: %s. "
                "This application is configured for managed knowledge bases (managedSearchConfiguration).",
                e,
            )
        else:
            logger.error("Error retrieving knowledge from Bedrock KB: %s", e)
        raise


