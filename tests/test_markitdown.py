import base64
from app.services.markitdown_service import (
    extract_content_from_base64,
    extract_content_from_bytes,
)


def test_extract_content_from_text_bytes():
    """プレーンテキストおよび Markdown バイト列の抽出検証"""
    sample_text = "# Project Roadmap\n\n- Task 1: Initialize\n- Task 2: Build"
    bytes_data = sample_text.encode("utf-8")

    result = extract_content_from_bytes(bytes_data, "roadmap.md")
    assert "Project Roadmap" in result
    assert "Task 1: Initialize" in result


def test_extract_content_from_csv_bytes():
    """CSV データのテキスト抽出検証"""
    csv_text = "id,name,department\n1,Alice,Sales\n2,Bob,Dev\n"
    bytes_data = csv_text.encode("utf-8")

    result = extract_content_from_bytes(bytes_data, "users.csv")
    assert "Alice" in result
    assert "Sales" in result


def test_extract_content_from_base64():
    """Base64 文字列からの抽出検証"""
    raw_content = "This is a confidential internal memorandum."
    b64_encoded = base64.b64encode(raw_content.encode("utf-8")).decode("utf-8")

    result = extract_content_from_base64(b64_encoded, "memo.txt")
    assert "confidential internal memorandum" in result


def test_extract_content_from_data_url():
    """data: URI スキーム付き Base64 からの抽出検証"""
    raw_content = "Quarterly Sales Report: 120% growth"
    b64_encoded = base64.b64encode(raw_content.encode("utf-8")).decode("utf-8")
    data_url = f"data:text/plain;base64,{b64_encoded}"

    result = extract_content_from_base64(data_url, "sales.txt")
    assert "Quarterly Sales Report" in result

