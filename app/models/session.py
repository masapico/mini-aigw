import uuid
from datetime import datetime, timezone
from typing import Optional
from sqlmodel import Field, SQLModel


class SessionRecord(SQLModel, table=True):
    __tablename__ = "sessions"

    session_id: str = Field(default_factory=lambda: str(uuid.uuid4()), primary_key=True)
    user_id: str = Field(foreign_key="users.id", index=True)
    raw_id_token: Optional[str] = Field(default=None)  # OIDC RP-Initiated Logout用
    expires_at: datetime = Field(index=True)
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))
