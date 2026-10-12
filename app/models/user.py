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
    roles: str = Field(default='["user"]')  # JSON 配列 (例: '["admin"]', '["user"]')
    groups: str = Field(default='[]')  # JSON 配列 (例: '["sales", "dev"]')
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))
    updated_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

    def get_roles(self) -> List[str]:
        try:
            return json.loads(self.roles)
        except Exception:
            return ["user"]

    def set_roles(self, roles: List[str]) -> None:
        self.roles = json.dumps(roles)

    def get_groups(self) -> List[str]:
        try:
            val = json.loads(self.groups)
            if isinstance(val, list) and val:
                return val
        except Exception:
            pass
        # 既存互換フォールバック: roles 内に sales, dev 等が含まれていた場合
        roles = self.get_roles()
        return [r for r in roles if r not in ("admin", "user")]

    def set_groups(self, groups: List[str]) -> None:
        self.groups = json.dumps(groups, ensure_ascii=False)
