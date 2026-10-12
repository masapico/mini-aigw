import base64
import io
import logging
from markitdown import MarkItDown

logger = logging.getLogger(__name__)

md_converter = MarkItDown()


def extract_content_from_bytes(file_bytes: bytes, filename: str) -> str:
    """
    バイト列から markitdown を用いて Markdown テキストを抽出する。
    デコード失敗時やプレーンテキスト時はフォールバックする。
    """
    try:
        file_io = io.BytesIO(file_bytes)
        file_io.name = filename
        result = md_converter.convert_stream(file_io)
        if result and hasattr(result, "text_content") and result.text_content:
            return result.text_content
    except Exception as e:
        logger.warning("markitdown extraction error for %s: %s. Attempting fallback.", filename, e)

    # フォールバック: UTF-8 テキストとしてデコード試行
    try:
        return file_bytes.decode("utf-8")
    except UnicodeDecodeError:
        return f"[Binary file: {filename} ({len(file_bytes)} bytes)]"


def extract_content_from_base64(b64_string: str, filename: str) -> str:
    """
    Base64 文字列（データURLスキーム含む）からバイナリを復元し、
    markitdown で Markdown テキストを抽出する。
    """
    # data:*:*;base64, プレフィックスの除去
    if "," in b64_string and ";base64" in b64_string:
        b64_string = b64_string.split(",", 1)[1]

    file_bytes = base64.b64decode(b64_string)
    return extract_content_from_bytes(file_bytes, filename)

