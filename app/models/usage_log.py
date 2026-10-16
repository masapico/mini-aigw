import json
import uuid
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional
from sqlmodel import Field, SQLModel


class ChatUsageLog(SQLModel, table=True):
    __tablename__ = "chat_usage_logs"

    id: str = Field(default_factory=lambda: str(uuid.uuid4()), primary_key=True)
    api_key_id: Optional[str] = Field(default=None, foreign_key="api_keys.id", index=True)
    api_key_name: str = Field(default="unknown")
    model_id: str = Field(index=True)
    is_stream: bool = Field(default=False)
    rag_triggered: bool = Field(default=False, index=True)
    search_queries: Optional[str] = Field(default=None)      # JSON 配列: 実行された検索クエリ群
    cited_documents: Optional[str] = Field(default=None)     # JSON 配列: 参照されたドキュメント情報 ([{"id": ..., "filename": ..., "group": ...}])
    prompt_tokens: int = Field(default=0)
    completion_tokens: int = Field(default=0)
    total_tokens: int = Field(default=0)
    latency_ms: int = Field(default=0)
    status: str = Field(default="success")                   # "success" | "error"
    error_message: Optional[str] = Field(default=None)
    created_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc), index=True)

    def get_search_queries(self) -> List[str]:
        if not self.search_queries:
            return []
        try:
            parsed = json.loads(self.search_queries)
            if isinstance(parsed, list):
                return [str(q) for q in parsed if q]
        except Exception:
            pass
        return []

    def get_cited_documents(self) -> List[Dict[str, Any]]:
        if not self.cited_documents:
            return []
        try:
            parsed = json.loads(self.cited_documents)
            if isinstance(parsed, list):
                return [d for d in parsed if isinstance(d, dict)]
        except Exception:
            pass
        return []

