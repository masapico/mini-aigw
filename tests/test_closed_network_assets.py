import os
import re
from fastapi.testclient import TestClient
from app.main import app


def test_no_external_cdn_references_in_templates():
    """HTML テンプレート内に外部 CDN 参照が一切含まれていないことを検証する"""
    templates_dir = os.path.join(os.path.dirname(__file__), "..", "app", "templates")
    cdn_patterns = [
        re.compile(r"https?://cdn\.jsdelivr\.net", re.IGNORECASE),
        re.compile(r"https?://cdnjs\.cloudflare\.com", re.IGNORECASE),
        re.compile(r"https?://unpkg\.com", re.IGNORECASE),
        re.compile(r"https?://stackpath\.bootstrapcdn\.com", re.IGNORECASE),
        re.compile(r"https?://code\.jquery\.com", re.IGNORECASE),
    ]

    for root, _, files in os.walk(templates_dir):
        for file in files:
            if file.endswith(".html"):
                file_path = os.path.join(root, file)
                with open(file_path, "r", encoding="utf-8") as f:
                    content = f.read()
                    for pattern in cdn_patterns:
                        match = pattern.search(content)
                        assert not match, f"External CDN reference found in {file_path}: {match.group(0)}"


def test_local_vendor_static_assets_exist_and_served():
    """同梱された静的ファイル (Bootstrap, htmx, app.css) が存在し、200 OK で配信されることを検証する"""
    client = TestClient(app)

    assets_to_test = [
        "/static/vendor/bootstrap/css/bootstrap.min.css",
        "/static/vendor/bootstrap/js/bootstrap.bundle.min.js",
        "/static/vendor/bootstrap-icons/bootstrap-icons.min.css",
        "/static/vendor/bootstrap-icons/fonts/bootstrap-icons.woff2",
        "/static/vendor/htmx/htmx.min.js",
        "/static/css/app.css",
    ]

    for asset_url in assets_to_test:
        response = client.get(asset_url)
        assert response.status_code == 200, f"Failed to serve {asset_url}"
        assert len(response.content) > 0

