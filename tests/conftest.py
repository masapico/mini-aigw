import os
import pytest

# テスト実行時は専用のテストDBを使用（開発/本番用DBを汚染しない）
TEST_DB_PATH = "data/test_aigw.db"
os.environ["DATABASE_URL"] = f"sqlite:///{TEST_DB_PATH}"

from app.core.config import settings
from app.core.database import engine, init_db


@pytest.fixture(autouse=True)
def setup_test_db():
    """テスト実行時にテスト用DBのテーブルを初期化する"""
    init_db()
    yield

