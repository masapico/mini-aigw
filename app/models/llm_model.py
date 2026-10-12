from datetime import datetime, timezone
from typing import Optional
from sqlmodel import Field, SQLModel


class LlmModel(SQLModel, table=True):
    __tablename__ = "llm_models"

    id: str = Field(primary_key=True)  # モデルID (例: 'anthropic.claude-3-5-sonnet-20241022-v2:0')
    name: str                          # 表示名 (例: 'Claude 3.5 Sonnet v2')
    description: Optional[str] = None  # 説明・用途
    provider: str = Field(default="bedrock")  # プロバイダ
    is_active: bool = Field(default=True)     # 有効/無効フラグ
    is_default: bool = Field(default=False)   # デフォルトフラグ
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

