package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"mini-aigw/internal/services"
)

// JudgeScore は LLM またはルールベースによる採点結果です
type JudgeScore struct {
	Faithfulness    float64 `json:"faithfulness"`     // 1.0 〜 5.0 (コンテキストへの忠実性)
	AnswerRelevance float64 `json:"answer_relevance"` // 1.0 〜 5.0 (質問への適合性)
	Correctness     float64 `json:"correctness"`      // 1.0 〜 5.0 (模範解答との合致度)
	Reasoning       string  `json:"reasoning"`        // 採点理由
}

// JudgePromptTemplate は LLM-as-a-Judge 用のシステムプロンプトです
const JudgeSystemPrompt = `あなたは RAG (検索拡張生成) システムの回答品質を厳格に評価する専門家エバリュエーターです。
与えられた [質問], [検索されたコンテキスト], [モデルの回答], [模範回答 (ある場合)] を精査し、以下の3つの観点を 1.0 〜 5.0 点（0.5点刻み）で採点してください。

【評価観点】
1. faithfulness (忠実度 / 反ハルシネーション)
   - 5.0: 回答中のすべての事実がコンテキストに明記されており、虚偽や推測が一切ない。
   - 3.0: 主張の大部分はコンテキストに基づくが、一部に推測や記載のない表現が含まれる。
   - 1.0: コンテキストにない重大な嘘（ハルシネーション）や矛盾した記述が含まれる。
   ※検索コンテキストが「該当なし」の場合、モデルが正直に「ナレッジに記載がない」旨を伝えていれば 5.0 点とします。

2. answer_relevance (質問への適合性)
   - 5.0: ユーザーの質問に対して直接的かつ明確に回答しており、余計な脱線がない。
   - 3.0: 質問には答えているが、冗長または不完全である。
   - 1.0: 質問に答えていない、または論点が完全にずれている。

3. correctness (模範解答との合致度)
   - 模範回答が存在する場合のみ厳格に評価（存在しない場合は 5.0 とみなす）。
   - 5.0: 模範回答のコア事実と完全に一致している。
   - 3.0: 重要な部分が一部欠落している、または微妙なニュアンスの違いがある。
   - 1.0: 模範回答と矛盾している、またはまったく異なる結論を出している。

【出力形式】
思考過程は出力せず、必ず以下の JSON フォーマットのみを出力してください（Markdown のバッククォート等も不要）:
{"faithfulness": 5.0, "answer_relevance": 5.0, "correctness": 4.5, "reasoning": "簡潔な採点理由"}`

var jsonBlockRegex = regexp.MustCompile(`\{[\s\S]*\}`)

// EvaluateWithLLM は Bedrock LLM を用いて回答の品質を採点します
func EvaluateWithLLM(
	ctx context.Context,
	bedrockSvc *services.BedrockService,
	judgeModelID string,
	query string,
	contextText string,
	modelAnswer string,
	referenceAnswer string,
) (*JudgeScore, error) {
	if bedrockSvc == nil || judgeModelID == "" {
		// LLM Judge なしの場合はルールベースで採点
		return EvaluateRuleBased(query, contextText, modelAnswer, referenceAnswer), nil
	}

	userContent := fmt.Sprintf(`[質問]
%s

[検索されたコンテキスト]
%s

[模範回答]
%s

[モデルの回答]
%s`, query, contextText, referenceAnswer, modelAnswer)

	sysBlocks := []bedrockTypes.SystemContentBlock{
		&bedrockTypes.SystemContentBlockMemberText{
			Value: JudgeSystemPrompt,
		},
	}

	messages := []bedrockTypes.Message{
		{
			Role: bedrockTypes.ConversationRoleUser,
			Content: []bedrockTypes.ContentBlock{
				&bedrockTypes.ContentBlockMemberText{
					Value: userContent,
				},
			},
		},
	}

	temp := 0.0
	maxTokens := 1024
	resp, err := bedrockSvc.CallConverse(ctx, judgeModelID, sysBlocks, messages, &temp, nil, &maxTokens, nil)
	if err != nil {
		return nil, fmt.Errorf("judge LLM call error (%s): %w", judgeModelID, err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("judge LLM returned empty response")
	}

	rawText, _ := resp.Choices[0].Message["content"].(string)
	jsonMatch := jsonBlockRegex.FindString(rawText)
	if jsonMatch == "" {
		return nil, fmt.Errorf("failed to extract JSON from judge output: %s", rawText)
	}

	var score JudgeScore
	if err := json.Unmarshal([]byte(jsonMatch), &score); err != nil {
		return nil, fmt.Errorf("failed to parse judge score JSON: %w (raw: %s)", err, jsonMatch)
	}

	return &score, nil
}

// EvaluateRuleBased は LLM を使わずにルールベースで近似スコアを計算します
func EvaluateRuleBased(query, contextText, modelAnswer, referenceAnswer string) *JudgeScore {
	score := &JudgeScore{
		Faithfulness:    4.0,
		AnswerRelevance: 4.0,
		Correctness:     4.0,
		Reasoning:       "Rule-based estimation (Judge LLM not specified)",
	}

	// 模範回答がある場合の一致判定
	if strings.TrimSpace(referenceAnswer) != "" {
		refWords := strings.Fields(referenceAnswer)
		matchCount := 0
		for _, w := range refWords {
			if len(w) > 3 && strings.Contains(modelAnswer, w) {
				matchCount++
			}
		}
		if len(refWords) > 0 {
			ratio := float64(matchCount) / float64(len(refWords))
			score.Correctness = 2.0 + (ratio * 3.0)
			if score.Correctness > 5.0 {
				score.Correctness = 5.0
			}
		}
	}

	return score
}

