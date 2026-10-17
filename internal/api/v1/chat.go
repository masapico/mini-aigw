package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
	"mini-aigw/internal/prompts"
	"mini-aigw/internal/services"
)

type ChatHandler struct {
	bedrockService   *services.BedrockService
	knowledgeService *services.KnowledgeService
	cfg              *config.Config
	db               *gorm.DB
}

func NewChatHandler(
	bedrockService *services.BedrockService,
	knowledgeService *services.KnowledgeService,
	cfg *config.Config,
	db *gorm.DB,
) *ChatHandler {
	return &ChatHandler{
		bedrockService:   bedrockService,
		knowledgeService: knowledgeService,
		cfg:              cfg,
		db:               db,
	}
}

type ChatCompletionRequest struct {
	Model               *string                  `json:"model"`
	Messages            []map[string]interface{} `json:"messages"`
	Temperature         *float64                 `json:"temperature"`
	TopP                *float64                 `json:"top_p"`
	MaxTokens           *int                     `json:"max_tokens"`
	MaxCompletionTokens *int                     `json:"max_completion_tokens"`
	Stream              bool                     `json:"stream"`
}

func (h *ChatHandler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	apiKey := middleware.GetAPIKeyFromContext(r.Context())
	if apiKey == nil {
		http.Error(w, `{"error":{"message":"Unauthorized","type":"invalid_request_error"}}`, http.StatusUnauthorized)
		return
	}

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": fmt.Sprintf("Invalid JSON request body: %v", err),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// 1. Resolve effective model ID
	effectiveModelID := h.cfg.DefaultModelID
	if apiKey.ModelID != nil && *apiKey.ModelID != "" {
		effectiveModelID = *apiKey.ModelID
	} else if req.Model != nil && *req.Model != "" {
		effectiveModelID = *req.Model
	}

	// 2. Resolve maxTokens
	maxTokens := req.MaxTokens
	if maxTokens == nil {
		maxTokens = req.MaxCompletionTokens
	}

	// 3. System prompt & Custom base prompt injection
	messages := req.Messages
	if apiKey.SystemPrompt != nil && *apiKey.SystemPrompt != "" {
		systemMsg := map[string]interface{}{
			"role":    "system",
			"content": *apiKey.SystemPrompt,
		}
		messages = append([]map[string]interface{}{systemMsg}, messages...)
	}

	allowedGroups := apiKey.GetAllowedGroups()
	ragEnabled := len(allowedGroups) > 0 && h.cfg.BedrockKBID != ""

	// Check if flusher is supported when stream=true
	var flusher http.Flusher
	if req.Stream {
		var ok bool
		flusher, ok = w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}

	if !ragEnabled {
		// Pure LLM inference (No Knowledge Tool)
		h.handleDirectConverse(r.Context(), w, flusher, apiKey, effectiveModelID, messages, req.Temperature, req.TopP, maxTokens, req.Stream, startTime)
		return
	}

	// RAG Tool Use inference
	h.handleRAGConverse(r.Context(), w, flusher, r, apiKey, effectiveModelID, messages, req.Temperature, req.TopP, maxTokens, req.Stream, allowedGroups, startTime)
}

func (h *ChatHandler) handleDirectConverse(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	apiKey *models.ApiKey,
	modelID string,
	rawMessages []map[string]interface{},
	temperature *float64,
	topP *float64,
	maxTokens *int,
	isStream bool,
	startTime time.Time,
) {
	sysBlocks, bedrockMsgs := h.bedrockService.ConvertOpenAIMessagesToBedrock(ctx, rawMessages)

	if isStream {
		err := h.bedrockService.CallConverseStream(ctx, w, flusher, modelID, sysBlocks, bedrockMsgs, temperature, topP, maxTokens, nil, nil, func(usage services.ChatCompletionUsage) {
			h.recordUsageLog(apiKey, modelID, true, false, nil, nil, &usage, startTime, "success", nil)
		})
		if err != nil {
			log.Printf("ConverseStream error: %v", err)
			h.recordUsageLog(apiKey, modelID, true, false, nil, nil, nil, startTime, "error", awsStr(err.Error()))
		}
		return
	}

	resp, err := h.bedrockService.CallConverse(ctx, modelID, sysBlocks, bedrockMsgs, temperature, topP, maxTokens, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"message": err.Error(), "type": "api_error"},
		})
		h.recordUsageLog(apiKey, modelID, false, false, nil, nil, nil, startTime, "error", awsStr(err.Error()))
		return
	}

	_ = json.NewEncoder(w).Encode(resp)
	h.recordUsageLog(apiKey, modelID, false, false, nil, nil, &resp.Usage, startTime, "success", nil)
}

func (h *ChatHandler) handleRAGConverse(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	r *http.Request,
	apiKey *models.ApiKey,
	modelID string,
	rawMessages []map[string]interface{},
	temperature *float64,
	topP *float64,
	maxTokens *int,
	isStream bool,
	allowedGroups []string,
	startTime time.Time,
) {
	sysBlocks, bedrockMsgs := h.bedrockService.ConvertOpenAIMessagesToBedrock(ctx, rawMessages)

	// Add Knowledge Tool System Prompt
	sysBlocks = append(sysBlocks, &bedrockTypes.SystemContentBlockMemberText{
		Value: prompts.KnowledgeToolSystemPrompt,
	})

	toolConfig := prompts.GetKnowledgeToolConfig()

	// Turn 1: Check if tool is invoked
	turn1Resp, err := h.bedrockService.CallConverse(ctx, modelID, sysBlocks, bedrockMsgs, temperature, topP, maxTokens, toolConfig)
	if err != nil {
		log.Printf("Turn 1 Converse error: %v", err)
		if isStream {
			h.bedrockService.StreamPrebuiltTextAsSSE(w, flusher, modelID, "推論処理中にエラーが発生しました。", "stop", nil, nil, nil)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{"message": err.Error(), "type": "api_error"},
			})
		}
		h.recordUsageLog(apiKey, modelID, isStream, false, nil, nil, nil, startTime, "error", awsStr(err.Error()))
		return
	}

	// Check for tool use calls in Turn 1
	var toolUseCall *bedrockTypes.ToolUseBlock
	var assistantTextBlocks []bedrockTypes.ContentBlock

	if outputMember, ok := turn1Resp.RawOutputMsg.(*bedrockTypes.ConverseOutputMemberMessage); ok {
		for _, block := range outputMember.Value.Content {
			assistantTextBlocks = append(assistantTextBlocks, block)
			if tu, ok := block.(*bedrockTypes.ContentBlockMemberToolUse); ok {
				toolUseCall = &tu.Value
			}
		}
	}

	if toolUseCall == nil {
		// Tool not called: Turn 1 text is the final answer
		responseText := ""
		if len(turn1Resp.Choices) > 0 {
			if content, ok := turn1Resp.Choices[0].Message["content"].(string); ok {
				responseText = content
			}
		}

		if isStream {
			h.bedrockService.StreamPrebuiltTextAsSSE(w, flusher, modelID, responseText, turn1Resp.StopReason, &turn1Resp.Usage, nil, func(usage services.ChatCompletionUsage) {
				h.recordUsageLog(apiKey, modelID, true, false, nil, nil, &usage, startTime, "success", nil)
			})
		} else {
			_ = json.NewEncoder(w).Encode(turn1Resp)
			h.recordUsageLog(apiKey, modelID, false, false, nil, nil, &turn1Resp.Usage, startTime, "success", nil)
		}
		return
	}

	// Tool was called! Extract query from input document
	searchQuery := ""
	var inputMap map[string]interface{}
	if err := toolUseCall.Input.UnmarshalSmithyDocument(&inputMap); err == nil {
		if q, ok := inputMap["query"].(string); ok {
			searchQuery = q
		}
	}
	if searchQuery == "" {
		searchQuery = extractLastUserQuery(rawMessages)
	}

	// Find matched tags or doc IDs in query
	matchedTags, matchedDocIDs := h.knowledgeService.FindMatchingMetadataFromQuery(searchQuery)

	// Execute Retrieve API
	retrievedChunks, err := h.bedrockService.RetrieveKnowledge(
		ctx,
		h.cfg.BedrockKBID,
		searchQuery,
		allowedGroups,
		h.cfg.RAGNumberOfResults,
		nil,
		matchedTags,
		matchedDocIDs,
	)
	if err != nil {
		log.Printf("Retrieve knowledge failed: %v", err)
	}

	// Filter by score threshold
	filteredChunks := services.FilterKnowledgeResultsByScore(retrievedChunks, h.cfg.RAGScoreThreshold)

	baseURL := h.cfg.PublicBaseURL
	if baseURL == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		baseURL = fmt.Sprintf("%s://%s", scheme, r.Host)
	}

	var formattedContext []string
	var citations []interface{}
	chunkIdx := 1

	for _, chunk := range filteredChunks {
		if h.knowledgeService.IsChunkFromDeletedDocument(chunk.Location, chunk.Metadata) {
			continue
		}

		s3URI := ""
		if loc, ok := chunk.Location["s3Location"].(map[string]interface{}); ok {
			s3URI, _ = loc["uri"].(string)
		}
		docID, filename, group, downloadURL := h.knowledgeService.ResolveDocumentAndDownloadURL(s3URI, chunk.Metadata, baseURL)

		formattedContext = append(formattedContext, fmt.Sprintf(
			"[%d] %s (%s):\n%s\nダウンロードURL: %s",
			chunkIdx, filename, group, chunk.Content, downloadURL,
		))

		citations = append(citations, map[string]interface{}{
			"index":        chunkIdx,
			"document_id":  docID,
			"filename":     filename,
			"group":        group,
			"score":        chunk.Score,
			"download_url": downloadURL,
		})
		chunkIdx++
	}

	var toolResultText string
	if len(formattedContext) > 0 {
		toolResultText = prompts.FormatRAGGroundingToolResult(formattedContext)
	} else {
		toolResultText = prompts.FormatRAGEmptyToolResult(searchQuery, h.cfg.RAGScoreThreshold)
	}

	// Turn 2 messages: prior messages + assistant message (with toolUse) + user message (toolResult)
	turn2Messages := append([]bedrockTypes.Message{}, bedrockMsgs...)
	turn2Messages = append(turn2Messages, bedrockTypes.Message{
		Role:    bedrockTypes.ConversationRoleAssistant,
		Content: assistantTextBlocks,
	})
	turn2Messages = append(turn2Messages, bedrockTypes.Message{
		Role: bedrockTypes.ConversationRoleUser,
		Content: []bedrockTypes.ContentBlock{
			&bedrockTypes.ContentBlockMemberToolResult{
				Value: bedrockTypes.ToolResultBlock{
					ToolUseId: toolUseCall.ToolUseId,
					Content: []bedrockTypes.ToolResultContentBlock{
						&bedrockTypes.ToolResultContentBlockMemberText{
							Value: toolResultText,
						},
					},
					Status: bedrockTypes.ToolResultStatusSuccess,
				},
			},
		},
	})

	searchQueries := []string{searchQuery}

	if isStream {
		err := h.bedrockService.CallConverseStream(
			ctx, w, flusher, modelID, sysBlocks, turn2Messages, temperature, topP, maxTokens,
			citations, &turn1Resp.Usage,
			func(combinedUsage services.ChatCompletionUsage) {
				h.recordUsageLog(apiKey, modelID, true, true, searchQueries, citations, &combinedUsage, startTime, "success", nil)
			},
		)
		if err != nil {
			log.Printf("Turn 2 ConverseStream error: %v", err)
			h.recordUsageLog(apiKey, modelID, true, true, searchQueries, citations, &turn1Resp.Usage, startTime, "error", awsStr(err.Error()))
		}
		return
	}

	turn2Resp, err := h.bedrockService.CallConverse(ctx, modelID, sysBlocks, turn2Messages, temperature, topP, maxTokens, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"message": err.Error(), "type": "api_error"},
		})
		h.recordUsageLog(apiKey, modelID, false, true, searchQueries, citations, &turn1Resp.Usage, startTime, "error", awsStr(err.Error()))
		return
	}

	// Combine Turn 1 and Turn 2 usage
	combinedUsage := services.ChatCompletionUsage{
		PromptTokens:     turn1Resp.Usage.PromptTokens + turn2Resp.Usage.PromptTokens,
		CompletionTokens: turn1Resp.Usage.CompletionTokens + turn2Resp.Usage.CompletionTokens,
		TotalTokens:      turn1Resp.Usage.TotalTokens + turn2Resp.Usage.TotalTokens,
	}
	turn2Resp.Usage = combinedUsage
	turn2Resp.Citations = citations

	_ = json.NewEncoder(w).Encode(turn2Resp)
	h.recordUsageLog(apiKey, modelID, false, true, searchQueries, citations, &combinedUsage, startTime, "success", nil)
}

func extractLastUserQuery(messages []map[string]interface{}) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if role, _ := msg["role"].(string); role == "user" {
			if contentStr, ok := msg["content"].(string); ok {
				return contentStr
			}
			if parts, ok := msg["content"].([]interface{}); ok {
				for _, p := range parts {
					if pMap, ok := p.(map[string]interface{}); ok {
						if t, ok := pMap["text"].(string); ok {
							return t
						}
					}
				}
			}
		}
	}
	return ""
}

func (h *ChatHandler) recordUsageLog(
	apiKey *models.ApiKey,
	modelID string,
	isStream bool,
	ragTriggered bool,
	searchQueries []string,
	citations []interface{},
	usage *services.ChatCompletionUsage,
	startTime time.Time,
	status string,
	errMessage *string,
) {
	go func() {
		latencyMs := time.Since(startTime).Milliseconds()
		promptTokens := 0
		completionTokens := 0
		totalTokens := 0
		if usage != nil {
			promptTokens = usage.PromptTokens
			completionTokens = usage.CompletionTokens
			totalTokens = usage.TotalTokens
		}

		var queriesStr *string
		if len(searchQueries) > 0 {
			b, _ := json.Marshal(searchQueries)
			s := string(b)
			queriesStr = &s
		}

		var citationsStr *string
		if len(citations) > 0 {
			b, _ := json.Marshal(citations)
			s := string(b)
			citationsStr = &s
		}

		apiKeyName := "unknown"
		var apiKeyID *string
		if apiKey != nil {
			apiKeyName = apiKey.Name
			apiKeyID = &apiKey.ID
		}

		logEntry := models.ChatUsageLog{
			ApiKeyID:         apiKeyID,
			ApiKeyName:       apiKeyName,
			ModelID:          modelID,
			IsStream:         isStream,
			RAGTriggered:     ragTriggered,
			SearchQueries:    queriesStr,
			CitedDocuments:   citationsStr,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
			LatencyMs:        latencyMs,
			Status:           status,
			ErrorMessage:     errMessage,
			CreatedAt:        time.Now(),
		}

		if err := h.db.Create(&logEntry).Error; err != nil {
			log.Printf("Failed to record usage log: %v", err)
		}
	}()
}

func awsStr(s string) *string {
	return &s
}
