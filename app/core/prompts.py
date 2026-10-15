"""
システム全体で利用されるプロンプト定義および Bedrock ツール仕様の集約モジュール。
プロンプトの調整・文言修正・ルール変更は本ファイルにて一括管理する。
"""

from typing import Any, Dict, List

# ==============================================================================
# 1. ナレッジ検索ツール連携時のシステムプロンプト (System Prompt)
# ==============================================================================
KNOWLEDGE_TOOL_SYSTEM_PROMPT = (
    "あなたは業務を支援するAIアシスタントです。\n"
    "組織のナレッジベースを検索するツール `retrieve_knowledge` が提供されています。\n\n"
    "【利用ガイドライン】\n"
    "1. 業務規定、業務マニュアル、手続き、組織内のドキュメントなど、組織の固有情報に関する質問や確認が必要な場合は、"
    "必ず `retrieve_knowledge` ツールを使用して検索してください。\n"
    "2. ツール呼び出し時の検索クエリは、会話の文脈や過去の履歴を考慮し、代名詞（それ、あれ等）を解消した具体的な検索キーワードを指定してください。\n"
    "3. 挨拶、日常会話、雑談、外国語の翻訳、一般的なプログラミングコード作成、直前の会話履歴・コンテキストのみで回答可能な要約など、"
    "組織ナレッジの検索が不要な質問には、ツールを呼び出さず直接回答してください。"
)

# ==============================================================================
# 2. Bedrock Converse API 用 Tool 仕様定義 (Tool Specification)
# ==============================================================================
TOOL_RETRIEVE_KNOWLEDGE_SPEC: Dict[str, Any] = {
    "toolSpec": {
        "name": "retrieve_knowledge",
        "description": (
            "組織のナレッジベース（業務規定、就業規則、業務マニュアル、業務手順書、FAQ、各種ガイドライン）から関連情報を検索します。"
            "組織の固有の情報や規定、業務マニュアル、手順書、FAQの確認が必要な質問に対してのみ使用してください。"
            "一般的な挨拶、雑談、翻訳、一般的なコーディング、会話履歴の要約など組織内の情報が不要な場合は絶対に使用しないでください。"
        ),
        "inputSchema": {
            "json": {
                "type": "object",
                "properties": {
                    "query": {
                        "type": "string",
                        "description": "組織ナレッジを検索するための具体的なクエリ文字列。文脈に応じた検索語句（代名詞の具体化を含む）を指定してください。",
                    }
                },
                "required": ["query"],
            }
        },
    }
}


def get_knowledge_tool_config() -> Dict[str, Any]:
    """Bedrock Converse API の toolConfig オブジェクトを生成して返す"""
    return {
        "tools": [TOOL_RETRIEVE_KNOWLEDGE_SPEC],
        "toolChoice": {"auto": {}},
    }


# ==============================================================================
# 3. ナレッジ検索結果の ToolResult 成形プロンプト (Tool Result Formatting)
# ==============================================================================
KNOWLEDGE_GROUNDING_RULES_SUMMARY = (
    "【検索結果の回答・根拠提示ルール（検索ヒット時に自動適用）】\n"
    "1. ナレッジの内容に基づいて回答し、該当箇所に [1], [2] などの引用番号を付記します。\n"
    "2. 回答の末尾に「### 根拠資料 (参照ドキュメント)」セクションを設け、参照したファイル名やダウンロードURLを明記します。\n"
    "3. ナレッジに記載がない事項は憶測で回答せず、「提供されたナレッジに記載がありません」と明記します。"
)


def format_rag_grounding_tool_result(context_chunks: List[str]) -> str:
    """
    検索され、スコア足切りを通過したナレッジチャンク群を
    LLM が根拠として利用できる toolResult テキストに整形する。
    """
    chunks_text = "\n\n".join(context_chunks)
    return (
        "【検索結果（ナレッジ）】\n"
        "以下のナレッジが検索されました。\n\n"
        f"{chunks_text}\n\n"
        "【回答方針・根拠提示ルール】\n"
        "上記で提供されたナレッジの内容に基づいてユーザーの質問に回答してください。\n"
        "1. 回答本文中の該当箇所に [1], [2] などの引用番号を付記してください。\n"
        "2. 回答の末尾に「### 根拠資料 (参照ドキュメント)」セクションを必ず設け、"
        "参照した各ドキュメントの [番号] ファイル名 (グループ名) と、記載されているダウンロードURLを Markdown リンク形式 `[ファイル名](ダウンロードURL)` で明記してください。（URLがない場合はファイル名のみ記載）\n"
        "3. ナレッジに記載がない事項は憶測で回答せず、「提供されたナレッジに記載がありません」と明記してください。"
    )


def format_rag_empty_tool_result(query: str, threshold: float) -> str:
    """
    検索結果が0件、またはスコア閾値未満で全件破棄された場合の toolResult テキストを生成する。
    """
    return (
        f"ナレッジベースを検索しましたが、クエリ「{query}」に関連するドキュメントは見つかりませんでした"
        f"（一致する情報なし、または類似度スコアが閾値 {threshold:.2f} 未満）。\n"
        "一般的な知識で回答できる場合は「ナレッジには該当情報がありませんでした」と前置きした上で一般的な回答を行うか、"
        "ナレッジベースに該当情報がないため回答できない旨を案内してください。末尾の「根拠資料」セクションは不要です。"
    )


# ==============================================================================
# 4. Web 管理画面 RAG テスト用プロンプト (Web UI RAG Test)
# ==============================================================================
RAG_TEST_SYSTEM_INSTRUCTION = (
    "You are an intelligent knowledge assistant. Answer the user question based on the provided retrieved context. "
    "Cite sources using [1], [2], etc. where appropriate. "
    "At the end of your response, provide a '### 根拠資料 (参照ドキュメント)' section listing each cited document with its download URL as a markdown link '[filename](download_url)' if available. "
    "If the context does not contain enough information, state so clearly."
)

