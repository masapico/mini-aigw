package prompts

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// ==============================================================================
// 1. ナレッジ検索ツール連携時のシステムプロンプト (System Prompt)
// ==============================================================================
const KnowledgeToolSystemPrompt = `あなたは業務を支援するAIアシスタントです。
組織のナレッジベースを検索するツール ` + "`retrieve_knowledge`" + ` が提供されています。

【利用ガイドライン】
1. 業務規定、業務マニュアル、手続き、各種資料など、組織の固有情報や登録ドキュメントに関する質問や確認が必要な場合は、必ず ` + "`retrieve_knowledge`" + ` ツールを使用して検索してください。
2. ツール呼び出し時の検索クエリ (` + "`query`" + `) には、キーワードの羅列や単語単体に分解・短縮せず、ユーザーの入力した自然な質問文・文章形式（例：『統計検定の出題範囲について教えてください』）をそのまま指定してください。ただし、直前の会話履歴の文脈から代名詞（それ、あれ、この資料等）が含まれる場合のみ、代名詞を具体的な名称に置き換えた自然な質問文を指定してください。
3. 挨拶、日常会話、雑談、外国語の翻訳、一般的なプログラミングコード作成、直前の会話履歴・コンテキストのみで回答可能な要約など、組織ナレッジの検索が不要な質問には、ツールを呼び出さず直接回答してください。`

// KnowledgeAnswerSystemPrompt は RAG の回答生成 (Turn 2) 用のシステムプロンプト。
// 検索は完了済みでツールは渡さないため、検索結果に基づいて本文を必ず出力させる。
const KnowledgeAnswerSystemPrompt = `あなたは業務を支援するAIアシスタントです。
組織のナレッジベースの検索は完了しており、その結果が会話の最後のユーザー発言として渡されます。
追加の検索やツール呼び出しはできません。渡された検索結果と会話履歴に基づき、ユーザーの質問への回答本文を必ず出力してください。`

// ==============================================================================
// 2. Bedrock Converse API 用 Tool 仕様定義 (Tool Specification)
// ==============================================================================
func GetKnowledgeToolConfig() *bedrockTypes.ToolConfiguration {
	// JSON Schema definition for query
	schemaMap := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type": "string",
				"description": "ナレッジを検索するための自然な質問文。検索エンジンの仕様上、単語単体やスペース区切りのキーワード羅列ではなく、" +
					"ユーザーの質問文（『〜について』『〜とは？』等の助詞を含む自然な文章）をそのまま指定してください" +
					"（代名詞が含まれる場合は具体名詞に補完した自然な文章にしてください）。",
			},
		},
		"required": []string{"query"},
	}

	doc := document.NewLazyDocument(schemaMap)

	toolSpec := &bedrockTypes.ToolMemberToolSpec{
		Value: bedrockTypes.ToolSpecification{
			Name: aws.String("retrieve_knowledge"),
			Description: aws.String(
				"組織のナレッジベース（業務規定、就業規則、業務マニュアル、業務手順書、FAQ、各種ガイドライン・参考資料）から関連情報を検索します。" +
					"組織の固有の情報や規定、業務マニュアル、手順書、登録資料の確認が必要な質問に対してのみ使用してください。" +
					"一般的な挨拶、雑談、翻訳、一般的なコーディング、会話履歴の要約など組織内の情報が不要な場合は絶対に使用しないでください。",
			),
			InputSchema: &bedrockTypes.ToolInputSchemaMemberJson{
				Value: doc,
			},
		},
	}

	return &bedrockTypes.ToolConfiguration{
		Tools: []bedrockTypes.Tool{toolSpec},
		ToolChoice: &bedrockTypes.ToolChoiceMemberAuto{
			Value: bedrockTypes.AutoToolChoice{},
		},
	}
}

// ==============================================================================
// 3. ナレッジ検索結果の ToolResult 成形プロンプト (Tool Result Formatting)
// ==============================================================================
const KnowledgeGroundingRulesSummary = `【検索結果の回答・根拠提示ルール（検索ヒット時に自動適用）】
1. ナレッジの内容に基づいて回答し、該当箇所に [1], [2] などの引用番号を付記します。
2. 回答の末尾に「### 根拠資料 (参照ドキュメント)」セクションを設け、参照したファイル名やダウンロードURLを明記します。
3. ナレッジに記載がない事項は憶測で回答せず、「提供されたナレッジに記載がありません」と明記します。`

func FormatRAGGroundingToolResult(contextChunks []string) string {
	chunksText := strings.Join(contextChunks, "\n\n")
	return fmt.Sprintf(`【検索結果（ナレッジ）】
以下のナレッジが検索されました。

%s

【回答方針・根拠提示ルール】
上記で提供されたナレッジの内容に基づいてユーザーの質問に回答してください。
1. 回答本文中の該当箇所に [1], [2] などの引用番号を付記してください。
2. 回答の末尾に「### 根拠資料 (参照ドキュメント)」セクションを必ず設け、参照した各ドキュメントの [番号] ファイル名 (グループ名) と、記載されているダウンロードURLを Markdown リンク形式 `+"`[ファイル名](ダウンロードURL)`"+` で明記してください。（URLがない場合はファイル名のみ記載）
3. ナレッジに記載がない事項は憶測で回答せず、「提供されたナレッジに記載がありません」と明記してください。`, chunksText)
}

func FormatRAGEmptyToolResult(query string, threshold float64) string {
	return fmt.Sprintf(
		"ナレッジベースを検索しましたが、クエリ「%s」に関連するドキュメントは見つかりませんでした"+
			"（一致する情報なし、または類似度スコアが閾値 %.2f 未満）。\n"+
			"一般的な知識で回答できる場合は「ナレッジには該当情報がありませんでした」と前置きした上で一般的な回答を行うか、"+
			"ナレッジベースに該当情報がないため回答できない旨を案内してください。末尾の「根拠資料」セクションは不要です。",
		query, threshold,
	)
}

// ==============================================================================
// 4. 自然文フォールバッククエリ生成
// ==============================================================================
var naturalQuerySuffixes = []string{
	"について",
	"とは",
	"ですか",
	"ますか",
	"教えて",
	"ください",
	"とは？",
	"？",
	"?",
	"。",
}

func BuildFallbackNaturalQuery(query string) *string {
	qClean := strings.TrimSpace(query)
	if qClean == "" {
		return nil
	}
	for _, sfx := range naturalQuerySuffixes {
		if strings.HasSuffix(qClean, sfx) {
			return nil
		}
	}
	res := fmt.Sprintf("%sについて", qClean)
	return &res
}

// ==============================================================================
// 5. Web 管理画面 RAG テスト用プロンプト (Web UI RAG Test)
// ==============================================================================
const RAGTestSystemInstruction = `You are an intelligent knowledge assistant. Answer the user question based on the provided retrieved context. ` +
	`Cite sources using [1], [2], etc. where appropriate. ` +
	`At the end of your response, provide a '### 根拠資料 (参照ドキュメント)' section listing each cited document with its download URL as a markdown link '[filename](download_url)' if available. ` +
	`If the context does not contain enough information, state so clearly.`
