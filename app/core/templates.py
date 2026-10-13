from datetime import datetime, timedelta, timezone
from typing import Any, Optional
from fastapi.templating import Jinja2Templates

# 日本標準時 (JST = UTC+9)
JST = timezone(timedelta(hours=9), name="JST")


def to_jst(dt: Any, fmt: str = "%Y-%m-%d %H:%M") -> str:
    """
    UTC または naive な datetime/ISO文字列を JST (UTC+9) に変換してフォーマットする。
    データ保持はUTCのまま、画面表示のみJSTに変換。
    """
    if not dt:
        return "-"
    if isinstance(dt, str):
        try:
            dt = datetime.fromisoformat(dt.replace("Z", "+00:00"))
        except Exception:
            return dt
    if not isinstance(dt, datetime):
        return str(dt)

    # タイムゾーン情報がない naive datetime の場合は UTC と解釈
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)

    jst_dt = dt.astimezone(JST)
    return jst_dt.strftime(fmt)


templates = Jinja2Templates(directory="app/templates")
templates.env.filters["to_jst"] = to_jst

