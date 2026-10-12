from unittest.mock import MagicMock
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
    """Bedrock Agent Runtime retrieve API に pre-filtering が正しく渡されることの検証"""
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

    vector_config = call_args["retrievalConfiguration"]["vectorSearchConfiguration"]
    assert vector_config["numberOfResults"] == 3
    assert "filter" in vector_config
    assert "orAll" in vector_config["filter"]

    allowed_filter_groups = [
        item["equals"]["value"] for item in vector_config["filter"]["orAll"]
    ]
    assert "sales" in allowed_filter_groups
    assert "public" in allowed_filter_groups
    assert "dev" not in allowed_filter_groups

