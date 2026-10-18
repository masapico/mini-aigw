package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"mini-aigw/internal/config"
	"mini-aigw/internal/prompts"
)

// RAGPipeline は RAG 実行の共通ロジックをカプセル化します
type RAGPipeline struct {
	bedrockService   *BedrockService
	knowledgeService *KnowledgeService
	cfg              *config.Config
}

func NewRAGPipeline(bedrock *BedrockService, knowledge *KnowledgeService, cfg *config.Config) *RAGPipeline {
	return &RAGPipeline{
		bedrockService:   bedrock,
		knowledgeService: knowledge,
		cfg:              cfg,
	}
}

// RetrieveResultData は検索・整形後のコンテキスト情報を保持します
type RetrieveResultData struct {
	Query             string
	RawResults        []RetrievalResult
	FilteredResults   []RetrievalResult
	FormattedContexts []string
	DocIDs            []string
	Filenames         []string
	Groups            []string
}

// RetrieveAndFormat はクエリに対して Bedrock KB を検索し、スコアフィルタおよびコンテキスト成形を行います
func (p *RAGPipeline) RetrieveAndFormat(
	ctx context.Context,
	query string,
	group string,
	topK int,
	threshold float64,
	baseURL string,
) (*RetrieveResultData, error) {
	if topK <= 0 {
		topK = p.cfg.RAGNumberOfResults
	}
	if threshold <= 0 {
		threshold = p.cfg.RAGScoreThreshold
	}

	allowedGroups := []string{"public"}
	if strings.TrimSpace(group) != "" && strings.TrimSpace(group) != "public" {
		allowedGroups = append(allowedGroups, strings.TrimSpace(group))
	}

	matchedTags, matchedDocIDs := p.knowledgeService.FindMatchingMetadataFromQuery(query)

	rawResults, err := p.bedrockService.RetrieveKnowledge(
		ctx,
		p.cfg.BedrockKBID,
		query,
		allowedGroups,
		topK,
		nil,
		matchedTags,
		matchedDocIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("bedrock retrieve error: %w", err)
	}

	filteredResults := FilterKnowledgeResultsByScore(rawResults, threshold)

	var formattedContext []string
	var docIDs []string
	var filenames []string
	var groups []string

	chunkIdx := 1
	for _, chunk := range filteredResults {
		if p.knowledgeService.IsChunkFromDeletedDocument(chunk.Location, chunk.Metadata) {
			continue
		}

		s3URI := ""
		if loc, ok := chunk.Location["s3Location"].(map[string]interface{}); ok {
			s3URI, _ = loc["uri"].(string)
		}
		docID, filename, chunkGroup, downloadURL := p.knowledgeService.ResolveDocumentAndDownloadURL(s3URI, chunk.Metadata, baseURL)

		formattedContext = append(formattedContext, fmt.Sprintf(
			"[%d] %s (%s):\n%s\nダウンロードURL: %s",
			chunkIdx, filename, chunkGroup, chunk.Content, downloadURL,
		))

		docIDs = append(docIDs, docID)
		filenames = append(filenames, filename)
		groups = append(groups, chunkGroup)
		chunkIdx++
	}

	return &RetrieveResultData{
		Query:             query,
		RawResults:        rawResults,
		FilteredResults:   filteredResults,
		FormattedContexts: formattedContext,
		DocIDs:            docIDs,
		Filenames:         filenames,
		Groups:            groups,
	}, nil
}

// GenerateAnswer は成形されたコンテキストとユーザーの質問を用いて LLM に推論を実行させます
func (p *RAGPipeline) GenerateAnswer(
	ctx context.Context,
	modelID string,
	query string,
	formattedContexts []string,
	temperature *float64,
	maxTokens *int,
) (answer string, latencyMs int64, inputTokens int, outputTokens int, err error) {
	var toolResultText string
	if len(formattedContexts) > 0 {
		toolResultText = prompts.FormatRAGGroundingToolResult(formattedContexts)
	} else {
		toolResultText = prompts.FormatRAGEmptyToolResult(query, p.cfg.RAGScoreThreshold)
	}

	sysBlocks := []bedrockTypes.SystemContentBlock{
		&bedrockTypes.SystemContentBlockMemberText{
			Value: prompts.KnowledgeToolSystemPrompt,
		},
	}

	messages := []bedrockTypes.Message{
		{
			Role: bedrockTypes.ConversationRoleUser,
			Content: []bedrockTypes.ContentBlock{
				&bedrockTypes.ContentBlockMemberText{
					Value: query,
				},
			},
		},
		{
			Role: bedrockTypes.ConversationRoleAssistant,
			Content: []bedrockTypes.ContentBlock{
				&bedrockTypes.ContentBlockMemberText{
					Value: "ナレッジベースを検索して回答します。",
				},
			},
		},
		{
			Role: bedrockTypes.ConversationRoleUser,
			Content: []bedrockTypes.ContentBlock{
				&bedrockTypes.ContentBlockMemberText{
					Value: toolResultText,
				},
			},
		},
	}

	if maxTokens == nil {
		defaultMax := p.cfg.DefaultMaxTokens
		maxTokens = &defaultMax
	}

	start := time.Now()
	resp, err := p.bedrockService.CallConverse(ctx, modelID, sysBlocks, messages, temperature, nil, maxTokens, nil)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return "", latency, 0, 0, err
	}

	answerText := ""
	if len(resp.Choices) > 0 {
		if content, ok := resp.Choices[0].Message["content"].(string); ok {
			answerText = content
		}
	}

	return answerText, latency, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, nil
}

