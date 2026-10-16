from contextlib import asynccontextmanager
import logging
import os
from fastapi import FastAPI
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from sqlmodel import Session, select
from app.api.v1 import chat, knowledge as v1_knowledge, models
from app.api.web import api_keys, auth, dashboard, knowledge, models as web_models, rag_test, users
from app.core.config import settings
from app.core.database import engine, init_db
from app.core.security import hash_password
from app.models.group import Group
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

    with Session(engine) as session:
        # 初期グループが存在しない場合にシード
        existing_groups = session.exec(select(Group)).all()
        if not existing_groups:
            seed_groups = [
                Group(name="group000", display_name="グループ000", description="グループ0のナレッジ"),
                Group(name="group001", display_name="グループ001", description="グループ1のナレッジ"),
            ]
            for g in seed_groups:
                session.add(g)
            session.commit()
            logger.info("Initialized default groups: 'group000', 'group001'")

        # 初期ローカルユーザーが存在しない場合にシード
        existing_user = session.exec(select(User)).first()
        if not existing_user:
            default_admin = User(
                auth_type="local",
                username="admin",
                password_hash=hash_password("Password1234"),
                email="admin@internal.local",
                display_name="System Admin",
                roles='["admin"]',
                groups='["group000", "group001"]',
            )
            session.add(default_admin)
            session.commit()
            logger.info("Initialized default admin user: 'admin' (password: 'Password1234')")

        # 起動時に期限切れセッションをクリーンアップ
        auth.purge_expired_sessions(session)

    logger.info("mini-aigw initialized successfully on %s:%s", settings.HOST, settings.PORT)
    yield


app = FastAPI(
    title=settings.PROJECT_NAME,
    description="AI Gateway for Amazon Bedrock and Bedrock Knowledge Bases",
    version="0.1.0",
    lifespan=lifespan,
)


@app.get("/health", tags=["Health Check"])
async def health_check():
    """ALB / EC2 AutoRecovery 用のヘルスチェックエンドポイント"""
    from fastapi.responses import JSONResponse
    from sqlalchemy import text

    try:
        with Session(engine) as session:
            session.exec(text("SELECT 1"))
        return {"status": "healthy", "database": "ok", "version": app.version}
    except Exception as e:
        logger.error("Health check failed: %s", e)
        return JSONResponse(
            status_code=503,
            content={"status": "unhealthy", "database": "error", "detail": str(e)},
        )


# 静的ファイルの完全ローカル配信 (CDN 依存の排除)
static_dir = os.path.join(os.path.dirname(__file__), "static")
if os.path.exists(static_dir):
    app.mount("/static", StaticFiles(directory=static_dir), name="static")

    @app.get("/favicon.ico", include_in_schema=False)
    async def favicon():
        return FileResponse(os.path.join(static_dir, "favicon.svg"), media_type="image/svg+xml")

# API ルーター (OpenAI 互換)
app.include_router(chat.router, prefix="/v1", tags=["OpenAI Compatible v1"])
app.include_router(models.router, prefix="/v1", tags=["OpenAI Compatible v1"])
app.include_router(v1_knowledge.router, prefix="/v1", tags=["OpenAI Compatible v1"])

# Web UI ルーター
app.include_router(auth.router, tags=["Web Auth"])
app.include_router(dashboard.router, tags=["Web Dashboard"])
app.include_router(knowledge.router, tags=["Web Knowledge"])
app.include_router(rag_test.router, tags=["Web RAG Test"])
app.include_router(api_keys.router, tags=["Web API Keys"])
app.include_router(web_models.router, tags=["Web Models"])
app.include_router(users.router, tags=["Web Users"])


