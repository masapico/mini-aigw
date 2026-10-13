import os
from typing import Generator
from sqlalchemy import event
from sqlmodel import Session, SQLModel, create_engine
from app.core.config import settings

# SQLiteデータベースファイルの格納ディレクトリを確保
os.makedirs(settings.DATA_DIR, exist_ok=True)

engine = create_engine(
    settings.DATABASE_URL,
    connect_args={"check_same_thread": False, "timeout": 15}
)


@event.listens_for(engine, "connect")
def set_sqlite_pragma(dbapi_connection, connection_record):
    cursor = dbapi_connection.cursor()
    cursor.execute("PRAGMA journal_mode=WAL")
    cursor.execute("PRAGMA synchronous=NORMAL")
    cursor.execute("PRAGMA foreign_keys=ON")
    cursor.close()


def init_db() -> None:
    """データベースの全テーブルを作成する"""
    SQLModel.metadata.create_all(engine)
    # 既存の api_keys テーブルに raw_key カラムが存在しない場合は追加
    try:
        with engine.connect() as conn:
            result = conn.exec_driver_sql("PRAGMA table_info(api_keys)").fetchall()
            columns = [row[1] for row in result]
            if columns and "raw_key" not in columns:
                conn.exec_driver_sql("ALTER TABLE api_keys ADD COLUMN raw_key VARCHAR")
                conn.commit()
    except Exception:
        pass


def get_session() -> Generator[Session, None, None]:
    with Session(engine) as session:
        yield session

