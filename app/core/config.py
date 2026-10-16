import os
from typing import List, Optional
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore"
    )

    PROJECT_NAME: str = "mini-aigw"
    DEBUG: bool = False
    HOST: str = "0.0.0.0"
    PORT: int = 8000
    PUBLIC_BASE_URL: Optional[str] = None  # e.g. "https://aigw.example.com" (未指定時はリクエストの base_url を使用)
    SECRET_KEY: str = "mini-aigw-secret-key-development-mode-change-in-production"

    # SQLite
    DATABASE_URL: str = "sqlite:///data/gateway.db"
    DATA_DIR: str = "data"

    # Session
    SESSION_COOKIE_NAME: str = "aigw_session"
    SESSION_MAX_AGE_SECONDS: int = 60 * 60 * 24 * 7  # 7 days
    SESSION_COOKIE_SECURE: bool = False  # Set to True in HTTPS production
    SESSION_COOKIE_SAMESITE: str = "lax"

    # AWS
    AWS_REGION: str = "ap-northeast-1"
    AWS_ENDPOINT_URL: Optional[str] = None
    AWS_CONNECT_TIMEOUT: int = 10
    AWS_READ_TIMEOUT: int = 120
    AWS_MAX_RETRY_ATTEMPTS: int = 4
    AWS_MAX_POOL_CONNECTIONS: int = 50
    BEDROCK_KB_ID: str = "SAMPLE_KB_ID"
    BEDROCK_DATA_SOURCE_ID: str = "SAMPLE_DS_ID"
    KNOWLEDGE_S3_BUCKET: str = "sample-knowledge-bucket"
    MAX_UPLOAD_FILE_SIZE_MB: int = 50
    DEFAULT_MODEL_ID: str = "openai.gpt-oss-20b-1:0"
    AVAILABLE_MODELS: List[str] = [
        "openai.gpt-oss-20b-1:0",
        "amazon.nova-lite-v1:0",
    ]
    # RAG Configuration
    RAG_SCORE_THRESHOLD: float = 0.40  # 類似度スコア閾値 (0.0〜1.0)
    RAG_NUMBER_OF_RESULTS: int = 5     # Retrieve API で取得する最大件数

    # Inference Parameters & Guardrails
    DEFAULT_TEMPERATURE: Optional[float] = None  # None の場合はモデルデフォルトに委任
    DEFAULT_MAX_TOKENS: int = 4096              # クライアント未指定時のデフォルト最大トークン数
    MAX_ALLOWED_TOKENS: int = 8192              # AIGW が許容する最大出力トークン数 (ハードキャップ)
    # mini-id OIDC
    OIDC_ISSUER: Optional[str] = None  # e.g. "https://auth.internal"
    OIDC_CLIENT_ID: Optional[str] = None
    OIDC_CLIENT_SECRET: Optional[str] = None
    OIDC_REDIRECT_URI: str = "http://localhost:8000/auth/oidc/callback"
    OIDC_SCOPES: str = "openid profile email groups"
    OIDC_END_SESSION_ENDPOINT: Optional[str] = None


settings = Settings()

