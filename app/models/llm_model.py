from datetime import datetime, timezone
from typing import Optional
from sqlmodel import Field, SQLModel


class LlmModel(SQLModel, table=True):
    __tablename__ = "llm_models"

    id: str = Field(primary_key=True)  # モデルID (例: 'openai.gpt-oss-20b-1:0')
    name: str                          # 表示名 (例: 'GPT-OSS 20B')
    description: Optional[str] = None  # 説明・用途
    provider: str = Field(default="bedrock")  # プロバイダ
    is_active: bool = Field(default=True)     # 有効/無効フラグ
    is_default: bool = Field(default=False)   # デフォルトフラグ
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

