import json
import uuid
from datetime import datetime, timezone
from typing import List, Optional
from sqlmodel import Field, SQLModel


class ApiKey(SQLModel, table=True):
    __tablename__ = "api_keys"

    id: str = Field(default_factory=lambda: str(uuid.uuid4()), primary_key=True)
    user_id: Optional[str] = Field(default=None, foreign_key="users.id", index=True)
    name: str
    key_hash: str = Field(unique=True, index=True)  # SHA-256
    prefix: str  # 表示用 (例: 'sk-live-ab12...')
    allowed_groups: str = Field(default='["public"]')  # JSON 配列 (例: '["public", "sales"]')
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

    def get_allowed_groups(self) -> List[str]:
        try:
            return json.loads(self.allowed_groups)
        except Exception:
            return ["public"]

    def set_allowed_groups(self, groups: List[str]) -> None:
        self.allowed_groups = json.dumps(groups)
