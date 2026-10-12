import json
from unittest.mock import MagicMock
from sqlmodel import Session, create_engine, SQLModel
from app.models.knowledge import KnowledgeDocument
from app.services.knowledge_service import (
    delete_knowledge_document,
    upload_document_with_metadata,
)


def test_upload_document_with_sidecar_metadata():
    """ドキュメントと .metadata.json サイドカーが同一プレフィックスにアップロードされることの検証"""
    mock_s3 = MagicMock()
    bucket = "test-bucket"
    prefix = "groups/sales"
    filename = "proposal.docx"
    content = b"Dummy docx content"
    group = "sales"

    result = upload_document_with_metadata(
        s3_client=mock_s3,
        bucket=bucket,
        prefix=prefix,
        filename=filename,
        content=content,
        group=group,
        document_id="doc-12345",
    )

    assert result["s3_key"] == "groups/sales/proposal.docx"
    assert result["meta_key"] == "groups/sales/proposal.docx.metadata.json"
    assert mock_s3.put_object.call_count == 2

    # 1. 本体呼び出しの検証
    call_args_body = mock_s3.put_object.call_args_list[0][1]
    assert call_args_body["Bucket"] == bucket
    assert call_args_body["Key"] == "groups/sales/proposal.docx"
    assert call_args_body["Body"] == content

    # 2. メタデータ呼び出しの検証
    call_args_meta = mock_s3.put_object.call_args_list[1][1]
    assert call_args_meta["Bucket"] == bucket
    assert call_args_meta["Key"] == "groups/sales/proposal.docx.metadata.json"
    assert call_args_meta["ContentType"] == "application/json"

    meta_json = json.loads(call_args_meta["Body"].decode("utf-8"))
    assert "metadataAttributes" in meta_json
    assert meta_json["metadataAttributes"]["group"] == "sales"
    assert meta_json["metadataAttributes"]["filename"] == "proposal.docx"
    assert meta_json["metadataAttributes"]["document_id"] == "doc-12345"


def test_upload_document_with_tags_metadata():
    """ドキュメントにタグが付与された際、.metadata.json に tags と tag_string が記録されることの検証"""
    mock_s3 = MagicMock()
    result = upload_document_with_metadata(
        s3_client=mock_s3,
        bucket="test-bucket",
        prefix="groups/dev",
        filename="api_spec.md",
        content=b"# API Spec",
        group="dev",
        document_id="doc-99999",
        tags=["2024", "設計書", "重要"],
    )

    assert mock_s3.put_object.call_count == 2
    call_args_meta = mock_s3.put_object.call_args_list[1][1]
    meta_json = json.loads(call_args_meta["Body"].decode("utf-8"))
    attrs = meta_json["metadataAttributes"]
    assert attrs["group"] == "dev"
    assert attrs["tags"] == ["2024", "設計書", "重要"]
    assert "2024, 設計書, 重要" == attrs["tag_string"]


def test_get_document_stream():
    """S3 から原本ストリームとメタ情報が取得できることの検証"""
    from app.services.knowledge_service import get_document_stream
    mock_s3 = MagicMock()
    mock_s3.get_object.return_value = {
        "Body": b"binary content",
        "ContentType": "application/pdf",
        "ContentLength": 14,
    }

    res = get_document_stream(mock_s3, "test-bucket", "public/test.pdf")
    assert res["body"] == b"binary content"
    assert res["content_type"] == "application/pdf"
    assert res["content_length"] == 14


def test_delete_knowledge_document_consistency_flow():
    """
    削除処理の整合性保証フロー:
    ステータス更新 (DELETING) -> S3 削除 -> KB Ingestion トリガー -> DB 物理削除
    """
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)

    with Session(engine) as db:
        doc = KnowledgeDocument(
            id="doc-test-del",
            s3_key="groups/sales/plan.pdf",
            s3_bucket="test-bucket",
            filename="plan.pdf",
            file_size=1024,
            content_type="application/pdf",
            group_name="sales",
            sync_status="SYNCED",
            data_source_id="ds-sample-999",
        )
        db.add(doc)
        db.commit()

        mock_s3 = MagicMock()
        mock_bedrock_agent = MagicMock()
        kb_id = "kb-sample-111"

        success = delete_knowledge_document(
            db=db,
            s3_client=mock_s3,
            bedrock_agent=mock_bedrock_agent,
            kb_id=kb_id,
            doc_id="doc-test-del",
        )

        assert success is True

        # S3 削除確認 (本体 & メタデータ)
        assert mock_s3.delete_object.call_count == 2
        deleted_keys = [c[1]["Key"] for c in mock_s3.delete_object.call_args_list]
        assert "groups/sales/plan.pdf" in deleted_keys
        assert "groups/sales/plan.pdf.metadata.json" in deleted_keys

        # Ingestion ジョブのトリガー確認
        mock_bedrock_agent.start_ingestion_job.assert_called_once_with(
            knowledgeBaseId=kb_id,
            dataSourceId="ds-sample-999",
        )

        # DB から削除されていることを確認
        assert db.get(KnowledgeDocument, "doc-test-del") is None

