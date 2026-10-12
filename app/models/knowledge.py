import json
import uuid
from datetime import datetime, timezone
from typing import List, Optional
from sqlmodel import Field, SQLModel


class KnowledgeDocument(SQLModel, table=True):
    __tablename__ = "knowledge_documents"

    id: str = Field(default_factory=lambda: str(uuid.uuid4()), primary_key=True)
    s3_key: str = Field(unique=True, index=True)
    s3_bucket: str
    filename: str
    file_size: int
    content_type: str
    group_name: str = Field(index=True)  # S3 Vectors フィルタ用キー (例: "sales", "dev", "public")
    tags: Optional[str] = Field(default=None)  # JSON 配列 または カンマ区切り
    uploader_id: Optional[str] = Field(default=None, foreign_key="users.id")
    sync_status: str = Field(default="PENDING")  # 'PENDING' | 'SYNCED' | 'DELETING' | 'ERROR'
    data_source_id: Optional[str] = Field(default=None)
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))
    updated_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

    def get_tags(self) -> List[str]:
        if not self.tags:
            return []
        try:
            parsed = json.loads(self.tags)
            if isinstance(parsed, list):
                return [str(t).strip() for t in parsed if str(t).strip()]
        except Exception:
            pass
        return [t.strip() for t in self.tags.split(",") if t.strip()]

    def set_tags(self, tags: List[str]) -> None:
        self.tags = json.dumps([t.strip() for t in tags if t.strip()], ensure_ascii=False)
