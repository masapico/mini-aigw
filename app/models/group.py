import uuid
from datetime import datetime, timezone
from typing import Optional
from sqlmodel import Field, SQLModel


class Group(SQLModel, table=True):
    __tablename__ = "groups"

    id: str = Field(default_factory=lambda: str(uuid.uuid4()), primary_key=True)
    name: str = Field(unique=True, index=True)  # 識別名 (例: "sales", "dev", "marketing")
    display_name: str  # 表示名 (例: "営業部", "開発部", "マーケティング部")
    description: Optional[str] = Field(default=None)
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

