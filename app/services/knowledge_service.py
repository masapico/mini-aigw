import json
import logging
from typing import Any, Dict, List, Optional, Set, Tuple
import boto3
from botocore.config import Config
from sqlmodel import Session, select
from app.core.config import settings
from app.models.knowledge import KnowledgeDocument

logger = logging.getLogger(__name__)

# Boto3 クライアントのコネクションプール再利用キャッシュ
_CLIENT_CACHE: Dict[Tuple[str, str, Optional[str]], Any] = {}

# KB同期完了前に検索ヒットするGhost Chunkを即座に除外するための削除済みレジストリ
_DELETED_DOC_IDS: Set[str] = set()
_DELETED_S3_KEYS: Set[str] = set()


def _build_boto_config() -> Config:
    return Config(
        retries={
            "max_attempts": settings.AWS_MAX_RETRY_ATTEMPTS,
            "mode": "adaptive",
        },
        connect_timeout=settings.AWS_CONNECT_TIMEOUT,
        read_timeout=settings.AWS_READ_TIMEOUT,
        max_pool_connections=settings.AWS_MAX_POOL_CONNECTIONS,
    )


def get_s3_client() -> Any:
    """S3 クライアントを取得する（コネクションプール再利用）"""
    cache_key = ("s3", settings.AWS_REGION, settings.AWS_ENDPOINT_URL)
    if cache_key not in _CLIENT_CACHE:
        kwargs: Dict[str, Any] = {
            "region_name": settings.AWS_REGION,
            "config": _build_boto_config(),
        }
        if settings.AWS_ENDPOINT_URL:
            kwargs["endpoint_url"] = settings.AWS_ENDPOINT_URL
        _CLIENT_CACHE[cache_key] = boto3.client("s3", **kwargs)
    return _CLIENT_CACHE[cache_key]


def get_bedrock_agent_client() -> Any:
    """Bedrock Agent クライアントを取得する（コネクションプール再利用）"""
    cache_key = ("bedrock-agent", settings.AWS_REGION, settings.AWS_ENDPOINT_URL)
    if cache_key not in _CLIENT_CACHE:
        kwargs: Dict[str, Any] = {
            "region_name": settings.AWS_REGION,
            "config": _build_boto_config(),
        }
        if settings.AWS_ENDPOINT_URL:
            kwargs["endpoint_url"] = settings.AWS_ENDPOINT_URL
        _CLIENT_CACHE[cache_key] = boto3.client("bedrock-agent", **kwargs)
    return _CLIENT_CACHE[cache_key]


def upload_document_with_metadata(
    s3_client: Any,
    bucket: str,
    prefix: str,
    filename: str,
    content: bytes,
    group: str,
    document_id: Optional[str] = None,
    content_type: str = "application/octet-stream",
    tags: Optional[list[str]] = None,
) -> Dict[str, str]:
    """
    S3 にファイル本体と同一ディレクトリに .metadata.json サイドカーをアップロードする。
    Returns:
        {"s3_key": obj_key, "meta_key": meta_key}
    """
    prefix_clean = prefix.strip("/")
    obj_key = f"{prefix_clean}/{filename}" if prefix_clean else filename
    meta_key = f"{obj_key}.metadata.json"

    # 再アップロード時は削除済みレジストリから解除
    _DELETED_S3_KEYS.discard(obj_key)
    if document_id:
        _DELETED_DOC_IDS.discard(document_id)

    metadata_content: Dict[str, Any] = {
        "metadataAttributes": {
            "group": group,
            "filename": filename,
            "source_prefix": f"{prefix_clean}/" if prefix_clean else "",
        }
    }
    if document_id:
        metadata_content["metadataAttributes"]["document_id"] = document_id

    if tags:
        clean_tags = [t.strip() for t in tags if t.strip()]
        if clean_tags:
            metadata_content["metadataAttributes"]["tags"] = clean_tags
            metadata_content["metadataAttributes"]["tag_string"] = ", ".join(clean_tags)

    # 1. ファイル本体アップロード
    s3_client.put_object(
        Bucket=bucket,
        Key=obj_key,
        Body=content,
        ContentType=content_type,
    )

    # 2. サイドカーメタデータ JSON アップロード
    s3_client.put_object(
        Bucket=bucket,
        Key=meta_key,
        Body=json.dumps(metadata_content, ensure_ascii=False, indent=2).encode("utf-8"),
        ContentType="application/json",
    )

    logger.info("Uploaded document %s and metadata %s to bucket %s", obj_key, meta_key, bucket)
    return {"s3_key": obj_key, "meta_key": meta_key}


def get_document_stream(
    s3_client: Any,
    bucket: str,
    key: str,
) -> Dict[str, Any]:
    """
    S3 から原本オブジェクトを取得する。
    Returns:
        {"body": StreamingBody, "content_type": str, "content_length": int}
    """
    response = s3_client.get_object(Bucket=bucket, Key=key)
    return {
        "body": response["Body"],
        "content_type": response.get("ContentType", "application/octet-stream"),
        "content_length": response.get("ContentLength", 0),
    }


def trigger_ingestion_job(
    bedrock_agent: Any,
    kb_id: str,
    data_source_id: str,
) -> Optional[str]:
    """Bedrock Knowledge Base の Ingestion Job をトリガーする"""
    try:
        response = bedrock_agent.start_ingestion_job(
            knowledgeBaseId=kb_id,
            dataSourceId=data_source_id,
        )
        job_id = response.get("ingestionJob", {}).get("ingestionJobId")
        logger.info("Started ingestion job %s for KB %s", job_id, kb_id)
        return job_id
    except Exception as e:
        logger.error("Failed to start ingestion job: %s", e)
        return None


def get_latest_ingestion_job(
    bedrock_agent: Any,
    kb_id: str,
    data_source_id: str,
) -> Optional[Dict[str, Any]]:
    """Bedrock Knowledge Base の最新 Ingestion Job 情報を取得する"""
    if not kb_id or not data_source_id:
        return None
    try:
        response = bedrock_agent.list_ingestion_jobs(
            knowledgeBaseId=kb_id,
            dataSourceId=data_source_id,
            maxResults=1,
            sortBy={"attribute": "STARTED_AT", "order": "DESCENDING"},
        )
        jobs = response.get("ingestionJobSummaries", [])
        if jobs:
            return jobs[0]
        return None
    except Exception as e:
        logger.warning("Failed to fetch latest ingestion job for KB %s: %s", kb_id, e)
        return None


def delete_knowledge_document(
    db: Session,
    s3_client: Any,
    bedrock_agent: Optional[Any],
    kb_id: str,
    doc_id: str,
    trigger_sync: bool = True,
) -> bool:
    """
    ドキュメント削除フロー（整合性保証）:
    1. DB上のステータスを 'DELETING' に更新・コミット
    2. S3 からファイル本体 & メタデータを削除
    3. オプションで Bedrock KB Ingestion ジョブのトリガー（同期は管理者/バッチに委任可能）
    4. 削除済みレジストリに登録し、DB レコード物理削除
    """
    doc = db.get(KnowledgeDocument, doc_id)
    if not doc:
        return False

    s3_key_to_mark = doc.s3_key

    # 1. ステータス更新
    doc.sync_status = "DELETING"
    db.add(doc)
    db.commit()

    # 2. S3 削除 (本体 & メタデータ)
    try:
        s3_client.delete_object(Bucket=doc.s3_bucket, Key=doc.s3_key)
        s3_client.delete_object(Bucket=doc.s3_bucket, Key=f"{doc.s3_key}.metadata.json")
    except Exception as e:
        logger.error("Error deleting S3 objects for %s: %s", doc.s3_key, e)

    # 3. オプションで KB Ingestion ジョブのトリガー
    if trigger_sync and bedrock_agent and doc.data_source_id and kb_id:
        trigger_ingestion_job(bedrock_agent, kb_id, doc.data_source_id)

    # 4. 完了後レコード削除 & Ghost Chunk 除外レジストリへ登録
    _DELETED_DOC_IDS.add(doc_id)
    if s3_key_to_mark:
        _DELETED_S3_KEYS.add(s3_key_to_mark)

    db.delete(doc)
    db.commit()
    logger.info("Deleted knowledge document %s (%s)", doc_id, doc.filename)
    return True


def _extract_s3_key_from_uri(s3_uri: str) -> Optional[str]:
    """
    Bedrock KB が返す s3Location.uri ('s3://bucket/key' または 'https://bucket.s3.region.amazonaws.com/key')
    から URL デコード済みの S3 オブジェクトキーを抽出する。
    """
    if not s3_uri:
        return None
    import urllib.parse

    if s3_uri.startswith("s3://"):
        parts = s3_uri.split("/")
        if len(parts) >= 4:
            return urllib.parse.unquote("/".join(parts[3:]))
    elif s3_uri.startswith("https://") or s3_uri.startswith("http://"):
        parsed = urllib.parse.urlparse(s3_uri)
        path = parsed.path.lstrip("/")
        if path:
            return urllib.parse.unquote(path)
    return None


def is_chunk_from_deleted_document(
    db: Session,
    s3_uri: str,
    metadata: Dict[str, Any],
) -> bool:
    """
    検索チャンクが削除済みまたは削除処理中 (DELETING) のドキュメントに由来するかを判定する（Ghost Chunk 防止）。
    """
    doc_id = metadata.get("document_id")
    if doc_id and str(doc_id) in _DELETED_DOC_IDS:
        return True

    key = _extract_s3_key_from_uri(s3_uri)
    if key and key in _DELETED_S3_KEYS:
        return True

    if doc_id:
        doc = db.get(KnowledgeDocument, str(doc_id))
        if doc and doc.sync_status == "DELETING":
            return True

    if key:
        doc = db.exec(select(KnowledgeDocument).where(KnowledgeDocument.s3_key == key)).first()
        if doc and doc.sync_status == "DELETING":
            return True

    return False


def resolve_document_and_download_url(
    db: Session,
    base_url: str,
    s3_uri: str,
    metadata: Dict[str, Any],
) -> Tuple[Optional[str], Optional[str]]:
    """
    Bedrock KB 検索結果 (s3_uri, metadata) からドキュメントIDを特定し、
    mini-aigw 署名付きダウンロードURLを生成して返却する。
    Returns:
        (doc_id, download_url)
    """
    from app.core.security import generate_download_token

    doc_id = metadata.get("document_id")
    if not doc_id and s3_uri:
        key = _extract_s3_key_from_uri(s3_uri)
        if key:
            doc = db.exec(select(KnowledgeDocument).where(KnowledgeDocument.s3_key == key)).first()
            if doc:
                doc_id = doc.id

    if doc_id:
        token = generate_download_token(doc_id)
        effective_base = (settings.PUBLIC_BASE_URL or base_url).rstrip("/")
        download_url = f"{effective_base}/v1/knowledge/download/{doc_id}?token={token}"
        return doc_id, download_url

    return None, None


def find_matching_metadata_from_query(
    db: Optional[Session],
    query: str,
    allowed_groups: List[str],
) -> Dict[str, List[str]]:
    """
    検索クエリ内に、許可グループの登録ドキュメントのタグ (tags) やファイル名 (filename) が
    含まれているかを検査し、.metadata.json の Pre-filtering に利用できる
    {"tags": [...], "document_ids": [...]} を返却する。
    """
    if not db or not query or not query.strip():
        return {"tags": [], "document_ids": []}

    import os
    import re

    q_norm = query.strip().lower()
    # 句読点や典型的な質問末尾（「について」「とは」「を教えて」等）を除去したコア語句も抽出
    q_core = re.sub(
        r"(について教えてください|について教えて|を教えてください|を教えて|について|とは何ですか|とは|って何|ですか|ますか|[？\?！!。、，,\s])+$",
        "",
        q_norm,
    ).strip()

    groups_to_include = list(set(["public"] + [g.strip() for g in allowed_groups if g and g.strip()]))
    stmt = (
        select(KnowledgeDocument)
        .where(KnowledgeDocument.group_name.in_(groups_to_include))
        .where(KnowledgeDocument.sync_status != "DELETING")
    )
    docs = db.exec(stmt).all()

    matched_tags: List[str] = []
    matched_doc_ids: List[str] = []

    for doc in docs:
        if doc.id in _DELETED_DOC_IDS or doc.s3_key in _DELETED_S3_KEYS:
            continue

        doc_matched = False
        # 1. タグ一致判定
        for tag in doc.get_tags():
            tag_clean = tag.strip()
            if not tag_clean:
                continue
            tag_lower = tag_clean.lower()
            if tag_lower in q_norm or (q_core and len(q_core) >= 2 and q_core in tag_lower):
                if tag_clean not in matched_tags:
                    matched_tags.append(tag_clean)
                doc_matched = True

        # 2. ファイル名一致判定
        if doc.filename:
            fname_lower = doc.filename.strip().lower()
            stem_lower = os.path.splitext(fname_lower)[0].strip()
            if fname_lower in q_norm or (len(stem_lower) >= 2 and stem_lower in q_norm):
                doc_matched = True
            elif q_core and len(q_core) >= 2 and q_core in stem_lower:
                doc_matched = True

        if doc_matched and doc.id not in matched_doc_ids:
            matched_doc_ids.append(doc.id)

    return {
        "tags": matched_tags,
        "document_ids": matched_doc_ids,
    }


def get_available_tags_for_groups(
    db: Session,
    allowed_groups: Optional[List[str]] = None,
) -> List[str]:
    """指定されたグループ範囲（None の場合は全グループ）で登録されているユニークなタグ一覧を取得する"""
    stmt = select(KnowledgeDocument).where(KnowledgeDocument.sync_status != "DELETING")
    if allowed_groups is not None:
        groups_to_include = list(set(["public"] + [g.strip() for g in allowed_groups if g and g.strip()]))
        stmt = stmt.where(KnowledgeDocument.group_name.in_(groups_to_include))

    docs = db.exec(stmt).all()
    tag_set: Set[str] = set()
    for d in docs:
        for t in d.get_tags():
            if t.strip():
                tag_set.add(t.strip())
    return sorted(list(tag_set))




