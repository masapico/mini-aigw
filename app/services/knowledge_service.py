import json
import logging
from typing import Any, Dict, Optional
import boto3
from sqlmodel import Session
from app.core.config import settings
from app.models.knowledge import KnowledgeDocument

logger = logging.getLogger(__name__)


def get_s3_client() -> Any:
    """S3 クライアントを取得する"""
    kwargs: Dict[str, Any] = {"region_name": settings.AWS_REGION}
    if settings.AWS_ENDPOINT_URL:
        kwargs["endpoint_url"] = settings.AWS_ENDPOINT_URL
    return boto3.client("s3", **kwargs)


def get_bedrock_agent_client() -> Any:
    """Bedrock Agent クライアントを取得する"""
    kwargs: Dict[str, Any] = {"region_name": settings.AWS_REGION}
    if settings.AWS_ENDPOINT_URL:
        kwargs["endpoint_url"] = settings.AWS_ENDPOINT_URL
    return boto3.client("bedrock-agent", **kwargs)


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


def delete_knowledge_document(
    db: Session,
    s3_client: Any,
    bedrock_agent: Any,
    kb_id: str,
    doc_id: str,
) -> bool:
    """
    ドキュメント削除フロー（整合性保証）:
    1. DB上のステータスを 'DELETING' に更新・コミット
    2. S3 からファイル本体 & メタデータを削除
    3. Bedrock KB Ingestion ジョブのトリガー
    4. DB レコード物理削除
    """
    doc = db.get(KnowledgeDocument, doc_id)
    if not doc:
        return False

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

    # 3. KB Ingestion ジョブのトリガー
    if doc.data_source_id and kb_id:
        trigger_ingestion_job(bedrock_agent, kb_id, doc.data_source_id)

    # 4. 完了後レコード削除
    db.delete(doc)
    db.commit()
    logger.info("Deleted knowledge document %s (%s)", doc_id, doc.filename)
    return True

