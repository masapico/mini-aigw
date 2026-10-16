from unittest.mock import MagicMock
import pytest
from app.services.bedrock_service import (
    build_retrieval_filter,
    retrieve_knowledge,
)


def test_build_retrieval_filter_group_isolation():
    """所属グループ sales の場合、public と sales のみが含まれ、dev が除外されることの検証"""
    filter_obj = build_retrieval_filter(["sales"])

    assert filter_obj is not None
    assert "orAll" in filter_obj

    allowed_vals = [cond["equals"]["value"] for cond in filter_obj["orAll"] if "equals" in cond]
    assert "public" in allowed_vals
    assert "sales" in allowed_vals
    assert "dev" not in allowed_vals


def test_retrieve_knowledge_applies_pre_filter():
    """Bedrock Agent Runtime retrieve API に managedSearchConfiguration と pre-filtering が正しく渡されることの検証"""
    mock_agent = MagicMock()
    mock_agent.retrieve.return_value = {
        "retrievalResults": [
            {
                "content": {"text": "Sales commission plan for FY26"},
                "location": {"s3Location": {"uri": "s3://test-bucket/groups/sales/plan.docx"}},
                "metadata": {"group": "sales", "filename": "plan.docx"},
                "score": 0.88,
            }
        ]
    }

    results = retrieve_knowledge(
        agent_client=mock_agent,
        kb_id="kb-12345",
        query="営業インセンティブについて",
        allowed_groups=["sales"],
        number_of_results=3,
    )

    assert len(results) == 1
    assert "Sales commission plan" in results[0]["content"]
    assert results[0]["metadata"]["group"] == "sales"

    # API 呼び出し引数の検証
    call_args = mock_agent.retrieve.call_args[1]
    assert call_args["knowledgeBaseId"] == "kb-12345"
    assert call_args["retrievalQuery"] == {"text": "営業インセンティブについて"}

    managed_config = call_args["retrievalConfiguration"]["managedSearchConfiguration"]
    assert managed_config["numberOfResults"] == 3
    assert "filter" in managed_config
    assert "orAll" in managed_config["filter"]

    allowed_filter_groups = [
        item["equals"]["value"] for item in managed_config["filter"]["orAll"]
    ]
    assert "sales" in allowed_filter_groups
    assert "public" in allowed_filter_groups
    assert "dev" not in allowed_filter_groups


def test_retrieve_knowledge_raises_on_error():
    """Bedrock retrieve API 呼び出しエラー時に例外が再送出されることの検証"""
    mock_agent = MagicMock()
    mock_agent.retrieve.side_effect = Exception(
        "Incompatible configuration: vectorSearchConfiguration is not supported for managed knowledge bases."
    )

    with pytest.raises(Exception) as exc_info:
        retrieve_knowledge(
            agent_client=mock_agent,
            kb_id="kb-12345",
            query="テスト",
            allowed_groups=["public"],
        )

    assert "Incompatible configuration" in str(exc_info.value)


def test_build_retrieval_filter_with_tags_and_document_ids():
    """tags (listContains) および document_ids (equals/in) が andAll で正しく合成されることの検証"""
    flt_single_tag = build_retrieval_filter(["public"], tags=["統計検定"])
    assert flt_single_tag == {
        "andAll": [
            {"equals": {"key": "group", "value": "public"}},
            {"listContains": {"key": "tags", "value": "統計検定"}},
        ]
    }

    flt_multi_docs = build_retrieval_filter(["sales"], document_ids=["doc-1", "doc-2"])
    assert "andAll" in flt_multi_docs
    assert flt_multi_docs["andAll"][1] == {
        "in": {"key": "document_id", "value": ["doc-1", "doc-2"]}
    }


def test_retrieve_knowledge_short_query_fallback():
    """
    Managed KB が単語単体クエリ（例: '統計検定'）で 0 件を返した際、
    自動的に『統計検定について』へ補完して再検索することの検証。
    """
    mock_agent = MagicMock()
    # 1回目 ('統計検定') は 0件、2回目 ('統計検定について') は 1件ヒット
    mock_agent.retrieve.side_effect = [
        {"retrievalResults": []},
        {
            "retrievalResults": [
                {
                    "content": {"text": "統計検定2級　出題範囲表"},
                    "location": {"s3Location": {"uri": "s3://mini-aigw/public/stat.pdf"}},
                    "metadata": {"group": "public", "filename": "統計検定2級_出題範囲表.pdf", "tags": ["統計検定"]},
                    "score": 0.42,
                }
            ]
        },
    ]

    results = retrieve_knowledge(
        agent_client=mock_agent,
        kb_id="kb-12345",
        query="統計検定",
        allowed_groups=["public"],
        number_of_results=5,
    )

    assert len(results) == 1
    assert results[0]["metadata"]["filename"] == "統計検定2級_出題範囲表.pdf"
    assert mock_agent.retrieve.call_count == 2
    first_q = mock_agent.retrieve.call_args_list[0][1]["retrievalQuery"]["text"]
    second_q = mock_agent.retrieve.call_args_list[1][1]["retrievalQuery"]["text"]
    assert first_q == "統計検定"
    assert second_q == "統計検定について"


def test_retrieve_knowledge_metadata_hybrid_merge():
    """
    クエリから検知された matched_tags / matched_document_ids が指定された場合、
    .metadata.json フィルタ検索の結果がマージされ、重複チャンクは高スコア側が採用されることの検証。
    """
    mock_agent = MagicMock()
    # 1回目（通常検索）は低スコア1件、2回目（メタデータフィルタ検索）は同一チャンクの高スコア + 新規チャンク
    mock_agent.retrieve.side_effect = [
        {
            "retrievalResults": [
                {
                    "content": {"text": "チャンクA"},
                    "location": {"s3Location": {"uri": "s3://mini-aigw/public/stat.pdf"}},
                    "metadata": {"_chunk_id": "c1", "group": "public", "filename": "stat.pdf"},
                    "score": 0.35,
                }
            ]
        },
        {
            "retrievalResults": [
                {
                    "content": {"text": "チャンクA"},
                    "location": {"s3Location": {"uri": "s3://mini-aigw/public/stat.pdf"}},
                    "metadata": {"_chunk_id": "c1", "group": "public", "filename": "stat.pdf"},
                    "score": 0.48,
                },
                {
                    "content": {"text": "チャンクB（タグ一致でヒット）"},
                    "location": {"s3Location": {"uri": "s3://mini-aigw/public/stat.pdf"}},
                    "metadata": {"_chunk_id": "c2", "group": "public", "filename": "stat.pdf"},
                    "score": 0.41,
                },
            ]
        },
    ]

    results = retrieve_knowledge(
        agent_client=mock_agent,
        kb_id="kb-12345",
        query="参考資料について",
        allowed_groups=["public"],
        number_of_results=5,
        matched_tags=["参考資料"],
        matched_document_ids=["doc-stat-1"],
    )

    assert len(results) == 2
    assert results[0]["score"] == 0.48
    assert results[0]["content"] == "チャンクA"
    assert results[1]["score"] == 0.41
    assert results[1]["content"] == "チャンクB（タグ一致でヒット）"


def test_find_matching_metadata_from_query():
    """DB 内の許可グループドキュメントの tags / filename がクエリから正しく検出されることの検証"""
    from sqlmodel import SQLModel, Session, create_engine
    from app.models.knowledge import KnowledgeDocument
    from app.services.knowledge_service import find_matching_metadata_from_query

    mem_engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(mem_engine)

    with Session(mem_engine) as db:
        doc_pub = KnowledgeDocument(
            id="doc-stat-001",
            s3_key="public/統計検定2級_出題範囲表.pdf",
            s3_bucket="mini-aigw",
            filename="統計検定2級_出題範囲表.pdf",
            file_size=1024,
            content_type="application/pdf",
            group_name="public",
            sync_status="SYNCED",
        )
        doc_pub.set_tags(["統計検定", "出題範囲", "参考資料"])

        doc_dev = KnowledgeDocument(
            id="doc-dev-002",
            s3_key="groups/dev/bpr.pdf",
            s3_bucket="mini-aigw",
            filename="内閣府_助言ポイント.pdf",
            file_size=2048,
            content_type="application/pdf",
            group_name="dev",
            sync_status="SYNCED",
        )
        doc_dev.set_tags(["BPR", "業務改善"])

        db.add(doc_pub)
        db.add(doc_dev)
        db.commit()

        # 1. public のタグ '参考資料' で検索 -> マッチ
        m1 = find_matching_metadata_from_query(db, "参考資料の内容を教えて", allowed_groups=["public"])
        assert "参考資料" in m1["tags"]
        assert "doc-stat-001" in m1["document_ids"]

        # 2. sales ユーザーが dev のタグ 'BPR' で検索 -> 権限外のためマッチしない
        m2 = find_matching_metadata_from_query(db, "BPRについて", allowed_groups=["sales"])
        assert m2["tags"] == []
        assert m2["document_ids"] == []

        # 3. dev ユーザーが 'BPR' で検索 -> マッチ
        m3 = find_matching_metadata_from_query(db, "BPR", allowed_groups=["dev"])
        assert "BPR" in m3["tags"]
        assert "doc-dev-002" in m3["document_ids"]



