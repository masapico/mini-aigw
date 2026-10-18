package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	zeroHitService   *services.ZeroHitService
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
		zeroHitService:   services.NewZeroHitService(db),
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
		if h.cfg.Debug {
			log.Printf("[DEBUG] [AUTH] Unauthorized chat request: missing or invalid API key")
		}
		http.Error(w, `{"error":{"message":"Unauthorized","type":"invalid_request_error"}}`, http.StatusUnauthorized)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		if h.cfg.Debug {
			log.Printf("[DEBUG] [ERROR] Failed to read request body: %v", err)
		}
		http.Error(w, `{"error":{"message":"Failed to read request body","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var req ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		if h.cfg.Debug {
			log.Printf("[DEBUG] [ERROR] Invalid JSON request body: %v\nBody: %s", err, string(bodyBytes))
		}
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

	if h.cfg.Debug {
		var prettyReq bytes.Buffer
		_ = json.Indent(&prettyReq, bodyBytes, "", "  ")
		log.Printf("\n========== [DEBUG: CHAT REQUEST START] ==========\n"+
			"Endpoint        : %s %s\n"+
			"API Key         : %s (ID: %s, Groups: %v)\n"+
			"Effective Model : %s (Requested: %v)\n"+
			"Stream          : %v | RAG Enabled: %v\n"+
			"MaxTokens       : %v | Temp: %v | TopP: %v\n"+
			"Request Payload :\n%s\n"+
			"=================================================",
			r.Method, r.URL.Path,
			apiKey.Name, apiKey.ID, allowedGroups,
			effectiveModelID, req.Model,
			req.Stream, ragEnabled,
			maxTokens, req.Temperature, req.TopP,
			prettyReq.String(),
		)
	}

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
		if h.cfg.Debug {
			log.Printf("[DEBUG] [DIRECT] Starting stream with model=%s...", modelID)
		}
		err := h.bedrockService.CallConverseStream(ctx, w, flusher, modelID, sysBlocks, bedrockMsgs, temperature, topP, maxTokens, nil, nil, nil, func(usage services.ChatCompletionUsage, _ []interface{}, fullAnswer string) {
			if h.cfg.Debug {
				log.Printf("\n========== [DEBUG: DIRECT STREAM SUCCESS] ==========\n"+
					"Status  : 200 OK (Streamed)\n"+
					"Model   : %s\n"+
					"Duration: %v\n"+
					"Tokens  : prompt=%d, completion=%d, total=%d\n"+
					"Answer  :\n%s\n"+
					"====================================================",
					modelID, time.Since(startTime),
					usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
					fullAnswer,
				)
			}
			h.recordUsageLog(apiKey, modelID, true, false, nil, nil, &usage, startTime, "success", nil)
		})
		if err != nil {
			log.Printf("[ERROR] ConverseStream error: %v", err)
			if h.cfg.Debug {
				log.Printf("\n========== [DEBUG: DIRECT STREAM ERROR] ==========\n"+
					"Error   : %v\n"+
					"Duration: %v\n"+
					"==================================================",
					err, time.Since(startTime),
				)
			}
			h.recordUsageLog(apiKey, modelID, true, false, nil, nil, nil, startTime, "error", awsStr(err.Error()))
		}
		return
	}

	if h.cfg.Debug {
		log.Printf("[DEBUG] [DIRECT] Calling Converse with model=%s...", modelID)
	}
	resp, err := h.bedrockService.CallConverse(ctx, modelID, sysBlocks, bedrockMsgs, temperature, topP, maxTokens, nil)
	if err != nil {
		log.Printf("[ERROR] Direct Converse error: %v", err)
		if h.cfg.Debug {
			log.Printf("\n========== [DEBUG: DIRECT ERROR] ==========\n"+
				"Error   : %v\n"+
				"Duration: %v\n"+
				"===========================================",
				err, time.Since(startTime),
			)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"message": err.Error(), "type": "api_error"},
		})
		h.recordUsageLog(apiKey, modelID, false, false, nil, nil, nil, startTime, "error", awsStr(err.Error()))
		return
	}

	answerText := ""
	if len(resp.Choices) > 0 {
		if content, ok := resp.Choices[0].Message["content"].(string); ok {
			answerText = content
		}
	}
	if h.cfg.Debug {
		log.Printf("\n========== [DEBUG: DIRECT RESPONSE SUCCESS] ==========\n"+
			"Status  : 200 OK\n"+
			"Model   : %s\n"+
			"Duration: %v\n"+
			"Tokens  : prompt=%d, completion=%d, total=%d\n"+
			"Answer  :\n%s\n"+
			"======================================================",
			modelID, time.Since(startTime),
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens,
			answerText,
		)
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

	if h.cfg.Debug {
		log.Printf("[DEBUG] [RAG] Turn 1: Calling Converse with model=%s (checking tool invocation)...", modelID)
	}

	// Turn 1: Check if tool is invoked
	turn1Resp, err := h.bedrockService.CallConverse(ctx, modelID, sysBlocks, bedrockMsgs, temperature, topP, maxTokens, toolConfig)
	if err != nil {
		log.Printf("[ERROR] Turn 1 Converse error: %v", err)
		if h.cfg.Debug {
			log.Printf("\n========== [DEBUG: RAG TURN 1 ERROR] ==========\n"+
				"Error   : %v\n"+
				"Duration: %v\n"+
				"===============================================",
				err, time.Since(startTime),
			)
		}
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
		if h.cfg.Debug {
			log.Printf("[DEBUG] [RAG] Tool was NOT invoked in Turn 1. Model provided direct answer.")
		}
		responseText := ""
		if len(turn1Resp.Choices) > 0 {
			if content, ok := turn1Resp.Choices[0].Message["content"].(string); ok {
				responseText = content
			}
		}

		if isStream {
			h.bedrockService.StreamPrebuiltTextAsSSE(w, flusher, modelID, responseText, turn1Resp.StopReason, &turn1Resp.Usage, nil, func(usage services.ChatCompletionUsage) {
				if h.cfg.Debug {
					log.Printf("\n========== [DEBUG: RAG DIRECT STREAM SUCCESS] ==========\n"+
						"Status  : 200 OK (Streamed)\n"+
						"Model   : %s\n"+
						"Duration: %v\n"+
						"Tokens  : prompt=%d, completion=%d, total=%d\n"+
						"Answer  :\n%s\n"+
						"========================================================",
						modelID, time.Since(startTime),
						usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
						responseText,
					)
				}
				h.recordUsageLog(apiKey, modelID, true, false, nil, nil, &usage, startTime, "success", nil)
			})
		} else {
			if h.cfg.Debug {
				log.Printf("\n========== [DEBUG: RAG DIRECT RESPONSE SUCCESS] ==========\n"+
					"Status  : 200 OK\n"+
					"Model   : %s\n"+
					"Duration: %v\n"+
					"Tokens  : prompt=%d, completion=%d, total=%d\n"+
					"Answer  :\n%s\n"+
					"==========================================================\n",
					modelID, time.Since(startTime),
					turn1Resp.Usage.PromptTokens, turn1Resp.Usage.CompletionTokens, turn1Resp.Usage.TotalTokens,
					responseText,
				)
			}
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

	if h.cfg.Debug {
		log.Printf("[DEBUG] [RAG] Tool invoked: %s (searchQuery: %q, matchedTags: %v, matchedDocs: %v)", *toolUseCall.Name, searchQuery, matchedTags, matchedDocIDs)
	}

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
		log.Printf("[ERROR] Retrieve knowledge failed: %v", err)
	}

	// Filter by score threshold
	filteredChunks := services.FilterKnowledgeResultsByScore(retrievedChunks, h.cfg.RAGScoreThreshold)

	if h.cfg.Debug {
		log.Printf("[DEBUG] [RAG] Retrieved %d chunks from KB (score threshold: %.2f)", len(retrievedChunks), h.cfg.RAGScoreThreshold)
		for i, c := range retrievedChunks {
			log.Printf("[DEBUG]   [%d] Score: %.4f | Loc: %v", i+1, c.Score, c.Location)
		}
		log.Printf("[DEBUG] [RAG] Chunks passing threshold: %d", len(filteredChunks))
	}

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

		// 未解決クエリ (Zero-Hit) の非同期集約記録
		maxScore := 0.0
		for _, c := range retrievedChunks {
			if c.Score > maxScore {
				maxScore = c.Score
			}
		}
		reason := "0件ヒット"
		if len(retrievedChunks) > 0 {
			reason = "スコア不足"
		}
		primaryGroup := "public"
		for _, g := range allowedGroups {
			if g != "public" {
				primaryGroup = g
				break
			}
		}
		apiKeyName := "unknown"
		if apiKey != nil {
			apiKeyName = apiKey.Name
		}
		h.zeroHitService.RecordZeroHitAsync(searchQuery, primaryGroup, maxScore, reason, apiKeyName)
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

	primaryGroup := "public"
	for _, g := range allowedGroups {
		if g != "public" {
			primaryGroup = g
			break
		}
	}
	apiKeyName := "unknown"
	if apiKey != nil {
		apiKeyName = apiKey.Name
	}

	maxScore := 0.0
	for _, c := range retrievedChunks {
		if c.Score > maxScore {
			maxScore = c.Score
		}
	}

	if h.cfg.Debug {
		log.Printf("[DEBUG] [RAG] Starting Turn 2 with model=%s (stream=%v, formattedContext chunks: %d)...", modelID, isStream, len(formattedContext))
	}

	if isStream {
		err := h.bedrockService.CallConverseStream(
			ctx, w, flusher, modelID, sysBlocks, turn2Messages, temperature, topP, maxTokens,
			toolConfig,
			citations, &turn1Resp.Usage,
			func(combinedUsage services.ChatCompletionUsage, actualCitations []interface{}, answerText string) {
				wasCited := len(actualCitations) > 0
				if !wasCited && len(formattedContext) > 0 {
					// チャンクは存在したがLLMが引用しなかった / ナレッジ記載なしの場合も未解決クエリとして集約！
					reason := "ナレッジ記載なし"
					h.zeroHitService.RecordZeroHitAsync(searchQuery, primaryGroup, maxScore, reason, apiKeyName)
				}
				if h.cfg.Debug {
					log.Printf("\n========== [DEBUG: RAG STREAM RESPONSE SUCCESS] ==========\n"+
						"Status   : 200 OK (Streamed)\n"+
						"Model    : %s\n"+
						"Duration : %v\n"+
						"Tokens   : prompt=%d, completion=%d, total=%d\n"+
						"Citations: %d items (wasCited=%v)\n"+
						"Answer   :\n%s\n"+
						"==========================================================",
						modelID, time.Since(startTime),
						combinedUsage.PromptTokens, combinedUsage.CompletionTokens, combinedUsage.TotalTokens,
						len(actualCitations), wasCited,
						answerText,
					)
				}
				h.recordUsageLog(apiKey, modelID, true, true, searchQueries, actualCitations, &combinedUsage, startTime, "success", nil)
			},
		)
		if err != nil {
			log.Printf("[ERROR] Turn 2 ConverseStream error: %v", err)
			if h.cfg.Debug {
				log.Printf("\n========== [DEBUG: RAG TURN 2 STREAM ERROR] ==========\n"+
					"Error   : %v\n"+
					"Duration: %v\n"+
					"======================================================",
					err, time.Since(startTime),
				)
			}
			h.recordUsageLog(apiKey, modelID, true, true, searchQueries, citations, &turn1Resp.Usage, startTime, "error", awsStr(err.Error()))
		}
		return
	}

	turn2Resp, err := h.bedrockService.CallConverse(ctx, modelID, sysBlocks, turn2Messages, temperature, topP, maxTokens, toolConfig)
	if err != nil {
		log.Printf("[ERROR] Turn 2 Converse error: %v", err)
		if h.cfg.Debug {
			log.Printf("\n========== [DEBUG: RAG TURN 2 ERROR] ==========\n"+
				"Error   : %v\n"+
				"Duration: %v\n"+
				"===============================================",
				err, time.Since(startTime),
			)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"message": err.Error(), "type": "api_error"},
		})
		h.recordUsageLog(apiKey, modelID, false, true, searchQueries, citations, &turn1Resp.Usage, startTime, "error", awsStr(err.Error()))
		return
	}

	answerText := ""
	if len(turn2Resp.Choices) > 0 {
		if content, ok := turn2Resp.Choices[0].Message["content"].(string); ok {
			answerText = content
		}
	}

	actualCitations, wasCited := services.FilterInterfaceCitations(answerText, citations)
	if !wasCited && len(formattedContext) > 0 {
		actualCitations = nil
		reason := "ナレッジ記載なし"
		h.zeroHitService.RecordZeroHitAsync(searchQuery, primaryGroup, maxScore, reason, apiKeyName)
	}

	// Combine Turn 1 and Turn 2 usage
	combinedUsage := services.ChatCompletionUsage{
		PromptTokens:     turn1Resp.Usage.PromptTokens + turn2Resp.Usage.PromptTokens,
		CompletionTokens: turn1Resp.Usage.CompletionTokens + turn2Resp.Usage.CompletionTokens,
		TotalTokens:      turn1Resp.Usage.TotalTokens + turn2Resp.Usage.TotalTokens,
	}
	turn2Resp.Usage = combinedUsage
	turn2Resp.Citations = actualCitations

	if h.cfg.Debug {
		log.Printf("\n========== [DEBUG: RAG RESPONSE SUCCESS] ==========\n"+
			"Status   : 200 OK\n"+
			"Model    : %s\n"+
			"Duration : %v\n"+
			"Tokens   : prompt=%d, completion=%d, total=%d\n"+
			"Citations: %d items (wasCited=%v)\n"+
			"Answer   :\n%s\n"+
			"===================================================",
			modelID, time.Since(startTime),
			combinedUsage.PromptTokens, combinedUsage.CompletionTokens, combinedUsage.TotalTokens,
			len(actualCitations), wasCited,
			answerText,
		)
	}

	_ = json.NewEncoder(w).Encode(turn2Resp)
	h.recordUsageLog(apiKey, modelID, false, true, searchQueries, actualCitations, &combinedUsage, startTime, "success", nil)
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

		// Calculate estimated cost in USD based on model pricing (per 1M tokens)
		var estimatedCost float64
		var modelRecord models.LLMModel
		if err := h.db.Select("input_price_per_1m", "output_price_per_1m").First(&modelRecord, "id = ?", modelID).Error; err == nil {
			inCost := (float64(promptTokens) * modelRecord.InputPricePer1M) / 1000000.0
			outCost := (float64(completionTokens) * modelRecord.OutputPricePer1M) / 1000000.0
			estimatedCost = inCost + outCost
		}

		apiKeyName := "unknown"
		var apiKeyID *string
		if apiKey != nil {
			apiKeyName = apiKey.Name
			apiKeyID = &apiKey.ID

			// Update last_used_at timestamp on the API key
			now := time.Now()
			_ = h.db.Model(&models.ApiKey{}).Where("id = ?", apiKey.ID).Update("last_used_at", now)
		}

		ttftMs := latencyMs
		if isStream && latencyMs > 100 {
			// Approximation for stream response if not explicitly measured: ~35% of total stream duration
			ttftMs = latencyMs / 3
		}

		logEntry := models.ChatUsageLog{
			ApiKeyID:           apiKeyID,
			ApiKeyName:         apiKeyName,
			ModelID:            modelID,
			IsStream:           isStream,
			RAGTriggered:       ragTriggered,
			SearchQueries:      queriesStr,
			CitedDocuments:     citationsStr,
			PromptTokens:       promptTokens,
			CompletionTokens:   completionTokens,
			TotalTokens:        totalTokens,
			EstimatedCost:      estimatedCost,
			LatencyMs:          latencyMs,
			TimeToFirstTokenMs: ttftMs,
			Status:             status,
			ErrorMessage:       errMessage,
			CreatedAt:          time.Now(),
		}

		if err := h.db.Create(&logEntry).Error; err != nil {
			log.Printf("Failed to record usage log: %v", err)
		}
	}()
}

func awsStr(s string) *string {
	return &s
}
