import json
import uuid
from datetime import datetime, timezone
from typing import List, Optional
from sqlmodel import Field, SQLModel


class User(SQLModel, table=True):
    __tablename__ = "users"

    id: str = Field(default_factory=lambda: str(uuid.uuid4()), primary_key=True)
    auth_type: str = Field(index=True)  # 'oidc' | 'local'
    oidc_sub: Optional[str] = Field(default=None, unique=True, index=True)  # mini-id sub (UUID)
    username: str = Field(unique=True, index=True)
    password_hash: Optional[str] = Field(default=None)
    email: Optional[str] = Field(default=None)
    display_name: Optional[str] = Field(default=None)
    roles: str = Field(default='["user"]')  # JSON 配列
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))
    updated_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

    def get_roles(self) -> List[str]:
        try:
            return json.loads(self.roles)
        except Exception:
            return ["user"]

    def set_roles(self, roles: List[str]) -> None:
        self.roles = json.dumps(roles)
