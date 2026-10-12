from contextlib import asynccontextmanager
import logging
import os
from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles
from sqlmodel import Session, select
from app.api.v1 import chat, models
from app.api.web import api_keys, auth, dashboard, knowledge, rag_test
from app.core.config import settings
from app.core.database import engine, init_db
from app.core.security import hash_password
from app.models.user import User

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger("mini-aigw")


@asynccontextmanager
async def lifespan(app: FastAPI):
    # データベース初期化
    init_db()

    # 初期ローカルユーザーが存在しない場合にシード
    with Session(engine) as session:
        existing_user = session.exec(select(User)).first()
        if not existing_user:
            default_admin = User(
                auth_type="local",
                username="admin",
                password_hash=hash_password("admin123"),
                email="admin@internal.local",
                display_name="System Admin",
                roles='["admin", "sales", "dev"]',
            )
            session.add(default_admin)
            session.commit()
            logger.info("Initialized default admin user: 'admin' (password: 'admin123')")

    logger.info("mini-aigw initialized successfully on %s:%s", settings.HOST, settings.PORT)
    yield


app = FastAPI(
    title=settings.PROJECT_NAME,
    description="AI Gateway for Amazon Bedrock and Bedrock Knowledge Bases",
    version="0.1.0",
    lifespan=lifespan,
)

# 静的ファイルの完全ローカル配信 (CDN 依存の排除)
static_dir = os.path.join(os.path.dirname(__file__), "static")
if os.path.exists(static_dir):
    app.mount("/static", StaticFiles(directory=static_dir), name="static")

# API ルーター (OpenAI 互換)
app.include_router(chat.router, prefix="/v1", tags=["OpenAI Compatible v1"])
app.include_router(models.router, prefix="/v1", tags=["OpenAI Compatible v1"])

# Web UI ルーター
app.include_router(auth.router, tags=["Web Auth"])
app.include_router(dashboard.router, tags=["Web Dashboard"])
app.include_router(knowledge.router, tags=["Web Knowledge"])
app.include_router(rag_test.router, tags=["Web RAG Test"])
app.include_router(api_keys.router, tags=["Web API Keys"])

