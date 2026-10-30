package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/logging"
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

	if h.cfg.ChatMaxBodyMB > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, int64(h.cfg.ChatMaxBodyMB)<<20)
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			log.Printf("[ERROR] Request body too large: limit=%d bytes", maxErr.Limit)
			logging.Error(r.Context(), "chat request body too large",
				"limit_bytes", maxErr.Limit, "api_key_id", apiKey.ID, "api_key_name", apiKey.Name)
			writeJSONError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large",
				fmt.Sprintf("Request body exceeds the limit of %d MB (CHAT_MAX_BODY_MB)", h.cfg.ChatMaxBodyMB))
			return
		}
		log.Printf("[ERROR] Failed to read request body: %v", err)
		logging.Error(r.Context(), "failed to read chat request body", "error", err.Error(), "api_key_id", apiKey.ID)
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "", "Failed to read request body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var req ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		if h.cfg.Debug {
			log.Printf("[DEBUG] [ERROR] Invalid JSON request body: %v\nBody: %s", err, string(bodyBytes))
		}
		logging.Error(r.Context(), "invalid chat request JSON",
			"error", err.Error(), "body_bytes", len(bodyBytes), "api_key_id", apiKey.ID)
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

	meta := &chatLogMeta{
		ModelID:    effectiveModelID,
		Stream:     req.Stream,
		RAG:        ragEnabled,
		APIKeyID:   apiKey.ID,
		APIKeyName: apiKey.Name,
		BodyBytes:  len(bodyBytes),
		StartTime:  startTime,
	}

	// Check if flusher is supported when stream=true
	var flusher http.Flusher
	if req.Stream {
		var ok bool
		flusher, ok = w.(http.Flusher)
		if !ok {
			logChatError(r.Context(), "stream_setup", errors.New("streaming unsupported by response writer"), meta)
			writeJSONError(w, http.StatusInternalServerError, "api_error", "", "Streaming unsupported")
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
		h.handleDirectConverse(r.Context(), w, flusher, apiKey, effectiveModelID, messages, req.Temperature, req.TopP, maxTokens, req.Stream, startTime, meta)
		return
	}

	// RAG Tool Use inference
	h.handleRAGConverse(r.Context(), w, flusher, r, apiKey, effectiveModelID, messages, req.Temperature, req.TopP, maxTokens, req.Stream, allowedGroups, startTime, meta)
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
	meta *chatLogMeta,
) {
	sysBlocks, bedrockMsgs, attachments := h.bedrockService.ConvertOpenAIMessagesToBedrockWithAttachments(ctx, rawMessages)
	meta.Attachments = attachments

	if isStream {
		if h.cfg.Debug {
			log.Printf("[DEBUG] [DIRECT] Starting stream with model=%s...", modelID)
		}
		err := h.bedrockService.CallConverseStream(ctx, w, flusher, modelID, sysBlocks, bedrockMsgs, temperature, topP, maxTokens, nil, nil, nil, func(res services.StreamResult) {
			if h.cfg.Debug {
				log.Printf("\n========== [DEBUG: DIRECT STREAM SUCCESS] ==========\n"+
					"Status  : 200 OK (Streamed)\n"+
					"Model   : %s\n"+
					"Duration: %v\n"+
					"Tokens  : prompt=%d, completion=%d, total=%d\n"+
					"Finish  : %s (stopReason=%s)\n"+
					"Answer  :\n%s\n"+
					"====================================================",
					modelID, time.Since(startTime),
					res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.TotalTokens,
					res.FinishReason, res.StopReason,
					res.Answer,
				)
			}
			status := checkEmptyReply(ctx, meta, replyDiagFromStream("direct_stream", res))
			h.recordUsageLog(apiKey, modelID, true, false, nil, nil, &res.Usage, startTime, status, nil)
		})
		if err != nil {
			log.Printf("[ERROR] ConverseStream error: %v", err)
			logChatError(ctx, "direct_converse_stream", err, meta)
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
		logChatError(ctx, "direct_converse", err, meta)
		writeBedrockError(w, err)
		h.recordUsageLog(apiKey, modelID, false, false, nil, nil, nil, startTime, "error", awsStr(err.Error()))
		return
	}

	if h.cfg.Debug {
		log.Printf("\n========== [DEBUG: DIRECT RESPONSE SUCCESS] ==========\n"+
			"Status  : 200 OK\n"+
			"Model   : %s\n"+
			"Duration: %v\n"+
			"Tokens  : prompt=%d, completion=%d, total=%d\n"+
			"Finish  : %s (stopReason=%s)\n"+
			"Answer  :\n%s\n"+
			"======================================================",
			modelID, time.Since(startTime),
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens,
			resp.FinishReason(), resp.StopReason,
			resp.AnswerText(),
		)
	}

	_ = json.NewEncoder(w).Encode(resp)
	status := checkEmptyReply(ctx, meta, replyDiagFromResponse("direct", resp))
	h.recordUsageLog(apiKey, modelID, false, false, nil, nil, &resp.Usage, startTime, status, nil)
}

// maxRAGToolQueries は Turn 1 で並列に呼ばれた retrieve_knowledge のうち、実際に検索するクエリ数の上限。
const maxRAGToolQueries = 3

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
	meta *chatLogMeta,
) {
	sysBlocks, bedrockMsgs, attachments := h.bedrockService.ConvertOpenAIMessagesToBedrockWithAttachments(ctx, rawMessages)
	meta.Attachments = attachments

	// Turn 1: Knowledge Tool System Prompt + tool definition
	turn1Sys := append(append([]bedrockTypes.SystemContentBlock{}, sysBlocks...), &bedrockTypes.SystemContentBlockMemberText{
		Value: prompts.KnowledgeToolSystemPrompt,
	})
	toolConfig := prompts.GetKnowledgeToolConfig()

	if h.cfg.Debug {
		log.Printf("[DEBUG] [RAG] Turn 1: Calling Converse with model=%s (checking tool invocation)...", modelID)
	}

	turn1Resp, err := h.bedrockService.CallConverse(ctx, modelID, turn1Sys, bedrockMsgs, temperature, topP, maxTokens, toolConfig)
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
		logChatError(ctx, "rag_turn1_converse", err, meta)
		if isStream {
			services.SendSSEError(w, flusher, err.Error())
		} else {
			writeBedrockError(w, err)
		}
		h.recordUsageLog(apiKey, modelID, isStream, false, nil, nil, nil, startTime, "error", awsStr(err.Error()))
		return
	}

	searchQueries, toolInvoked := extractToolQueries(turn1Resp)

	if !toolInvoked {
		// Tool not called: Turn 1 text is the final answer
		if h.cfg.Debug {
			log.Printf("[DEBUG] [RAG] Tool was NOT invoked in Turn 1. Model provided direct answer.")
			log.Printf("\n========== [DEBUG: RAG DIRECT RESPONSE SUCCESS] ==========\n"+
				"Status  : 200 OK (stream=%v)\n"+
				"Model   : %s\n"+
				"Duration: %v\n"+
				"Tokens  : prompt=%d, completion=%d, total=%d\n"+
				"Finish  : %s (stopReason=%s)\n"+
				"Answer  :\n%s\n"+
				"==========================================================",
				isStream, modelID, time.Since(startTime),
				turn1Resp.Usage.PromptTokens, turn1Resp.Usage.CompletionTokens, turn1Resp.Usage.TotalTokens,
				turn1Resp.FinishReason(), turn1Resp.StopReason,
				turn1Resp.AnswerText(),
			)
		}
		if isStream {
			h.bedrockService.StreamPrebuiltTextAsSSE(w, flusher, modelID, turn1Resp, nil, nil)
		} else {
			_ = json.NewEncoder(w).Encode(turn1Resp)
		}
		status := checkEmptyReply(ctx, meta, replyDiagFromResponse("rag_turn1_direct", turn1Resp))
		h.recordUsageLog(apiKey, modelID, isStream, false, nil, nil, &turn1Resp.Usage, startTime, status, nil)
		return
	}

	if len(searchQueries) == 0 {
		if q := extractLastUserQuery(rawMessages); q != "" {
			searchQueries = []string{q}
		}
	}
	// 主クエリ (ゼロヒット記録・検索結果なしの案内に使う)
	searchQuery := ""
	if len(searchQueries) > 0 {
		searchQuery = searchQueries[0]
	}

	// Execute Retrieve API for each query and merge
	var retrievedChunks []services.RetrievalResult
	for _, q := range searchQueries {
		matchedTags, matchedDocIDs := h.knowledgeService.FindMatchingMetadataFromQuery(q)
		if h.cfg.Debug {
			log.Printf("[DEBUG] [RAG] Tool invoked (searchQuery: %q, matchedTags: %v, matchedDocs: %v)", q, matchedTags, matchedDocIDs)
		}
		chunks, err := h.bedrockService.RetrieveKnowledge(
			ctx,
			h.cfg.BedrockKBID,
			q,
			allowedGroups,
			h.cfg.RAGNumberOfResults,
			nil,
			matchedTags,
			matchedDocIDs,
		)
		if err != nil {
			log.Printf("[ERROR] Retrieve knowledge failed: %v", err)
			logging.Warn(ctx, "retrieve knowledge failed", "error", err.Error(), "model", modelID, "api_key_id", meta.APIKeyID)
			continue
		}
		retrievedChunks = services.MergeRetrievalResults(retrievedChunks, chunks, h.cfg.RAGNumberOfResults*len(searchQueries))
	}

	// Filter by score threshold
	filteredChunks := services.FilterKnowledgeResultsByScore(retrievedChunks, h.cfg.RAGScoreThreshold)

	if h.cfg.Debug {
		log.Printf("[DEBUG] [RAG] Retrieved %d chunks from KB (queries: %d, score threshold: %.2f)", len(retrievedChunks), len(searchQueries), h.cfg.RAGScoreThreshold)
		for i, c := range retrievedChunks {
			log.Printf("[DEBUG]   [%d] Score: %.4f | Loc: %v", i+1, c.Score, c.Location)
		}
		log.Printf("[DEBUG] [RAG] Chunks passing threshold: %d", len(filteredChunks))
	}

	baseURL := middleware.ResolveBaseURL(r, h.cfg)

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

	var toolResultText string
	if len(formattedContext) > 0 {
		toolResultText = prompts.FormatRAGGroundingToolResult(formattedContext)
	} else {
		toolResultText = prompts.FormatRAGEmptyToolResult(searchQuery, h.cfg.RAGScoreThreshold)

		// 未解決クエリ (Zero-Hit) の非同期集約記録
		reason := "0件ヒット"
		if len(retrievedChunks) > 0 {
			reason = "スコア不足"
		}
		h.zeroHitService.RecordZeroHitAsync(searchQuery, primaryGroup, maxScore, reason, apiKeyName)
	}

	// Turn 2: 検索結果をテキストの会話として渡し、ツールなしで回答を生成する。
	// ツールを渡すとモデルが再検索 (tool_use) して本文が空になるため、toolConfig は渡さない。
	turn2Sys := append(append([]bedrockTypes.SystemContentBlock{}, sysBlocks...), &bedrockTypes.SystemContentBlockMemberText{
		Value: prompts.KnowledgeAnswerSystemPrompt,
	})
	turn2Messages := services.BuildRAGAnswerMessages(bedrockMsgs, turn1Resp.AnswerText(), toolResultText)

	ragDiag := func(d replyDiag) replyDiag {
		d.ToolInvoked = true
		d.Queries = len(searchQueries)
		d.RetrievedChunks = len(retrievedChunks)
		d.PassedChunks = len(formattedContext)
		return d
	}

	if h.cfg.Debug {
		log.Printf("[DEBUG] [RAG] Starting Turn 2 with model=%s (stream=%v, formattedContext chunks: %d)...", modelID, isStream, len(formattedContext))
	}

	if isStream {
		err := h.bedrockService.CallConverseStream(
			ctx, w, flusher, modelID, turn2Sys, turn2Messages, temperature, topP, maxTokens,
			nil,
			citations, &turn1Resp.Usage,
			func(res services.StreamResult) {
				wasCited := len(res.Citations) > 0
				if !wasCited && len(formattedContext) > 0 {
					// チャンクは存在したがLLMが引用しなかった / ナレッジ記載なしの場合も未解決クエリとして集約！
					h.zeroHitService.RecordZeroHitAsync(searchQuery, primaryGroup, maxScore, "ナレッジ記載なし", apiKeyName)
				}
				if h.cfg.Debug {
					log.Printf("\n========== [DEBUG: RAG STREAM RESPONSE SUCCESS] ==========\n"+
						"Status   : 200 OK (Streamed)\n"+
						"Model    : %s\n"+
						"Duration : %v\n"+
						"Tokens   : prompt=%d, completion=%d, total=%d\n"+
						"Finish   : %s (stopReason=%s)\n"+
						"Citations: %d items (wasCited=%v)\n"+
						"Answer   :\n%s\n"+
						"==========================================================",
						modelID, time.Since(startTime),
						res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.TotalTokens,
						res.FinishReason, res.StopReason,
						len(res.Citations), wasCited,
						res.Answer,
					)
				}
				status := checkEmptyReply(ctx, meta, ragDiag(replyDiagFromStream("rag_turn2_stream", res)))
				h.recordUsageLog(apiKey, modelID, true, true, searchQueries, res.Citations, &res.Usage, startTime, status, nil)
			},
		)
		if err != nil {
			log.Printf("[ERROR] Turn 2 ConverseStream error: %v", err)
			logChatError(ctx, "rag_turn2_converse_stream", err, meta)
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

	combinedUsage := turn1Resp.Usage
	var turn2Resp *services.ChatCompletionResponse
	// 本文が空のまま正常終了 (stop) した場合だけ 1 回再試行する
	for attempt := 1; attempt <= 2; attempt++ {
		turn2Resp, err = h.bedrockService.CallConverse(ctx, modelID, turn2Sys, turn2Messages, temperature, topP, maxTokens, nil)
		if err != nil {
			break
		}
		combinedUsage = addUsage(combinedUsage, turn2Resp.Usage)
		if attempt == 2 || strings.TrimSpace(turn2Resp.AnswerText()) != "" || turn2Resp.FinishReason() != "stop" {
			break
		}
		logging.Warn(ctx, "chat reply empty, retrying",
			"stage", "rag_turn2", "model", modelID, "stop_reason", turn2Resp.StopReason,
			"prompt_tokens", turn2Resp.Usage.PromptTokens, "completion_tokens", turn2Resp.Usage.CompletionTokens,
			"api_key_id", meta.APIKeyID)
	}
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
		logChatError(ctx, "rag_turn2_converse", err, meta)
		writeBedrockError(w, err)
		h.recordUsageLog(apiKey, modelID, false, true, searchQueries, citations, &combinedUsage, startTime, "error", awsStr(err.Error()))
		return
	}

	answerText := turn2Resp.AnswerText()

	actualCitations, wasCited := services.FilterInterfaceCitations(answerText, citations)
	if !wasCited && len(formattedContext) > 0 {
		actualCitations = nil
		h.zeroHitService.RecordZeroHitAsync(searchQuery, primaryGroup, maxScore, "ナレッジ記載なし", apiKeyName)
	}

	turn2Resp.Usage = combinedUsage
	turn2Resp.Citations = actualCitations

	if h.cfg.Debug {
		log.Printf("\n========== [DEBUG: RAG RESPONSE SUCCESS] ==========\n"+
			"Status   : 200 OK\n"+
			"Model    : %s\n"+
			"Duration : %v\n"+
			"Tokens   : prompt=%d, completion=%d, total=%d\n"+
			"Finish   : %s (stopReason=%s)\n"+
			"Citations: %d items (wasCited=%v)\n"+
			"Answer   :\n%s\n"+
			"===================================================",
			modelID, time.Since(startTime),
			combinedUsage.PromptTokens, combinedUsage.CompletionTokens, combinedUsage.TotalTokens,
			turn2Resp.FinishReason(), turn2Resp.StopReason,
			len(actualCitations), wasCited,
			answerText,
		)
	}

	_ = json.NewEncoder(w).Encode(turn2Resp)
	status := checkEmptyReply(ctx, meta, ragDiag(replyDiagFromResponse("rag_turn2", turn2Resp)))
	h.recordUsageLog(apiKey, modelID, false, true, searchQueries, actualCitations, &combinedUsage, startTime, status, nil)
}

// extractToolQueries は Turn 1 応答の retrieve_knowledge 呼び出しから検索クエリを集める (重複除去、上限 maxRAGToolQueries)。
// toolInvoked はツール呼び出しが 1 件以上あったかどうか。query が取れない呼び出しも数える。
func extractToolQueries(resp *services.ChatCompletionResponse) (queries []string, toolInvoked bool) {
	outputMember, ok := resp.RawOutputMsg.(*bedrockTypes.ConverseOutputMemberMessage)
	if !ok {
		return nil, false
	}
	seen := make(map[string]bool)
	for _, block := range outputMember.Value.Content {
		tu, ok := block.(*bedrockTypes.ContentBlockMemberToolUse)
		if !ok {
			continue
		}
		toolInvoked = true
		var inputMap map[string]interface{}
		if tu.Value.Input != nil {
			// document の実装によってはデコードに成功してもエラーを返すため、結果の有無で判定する
			_ = tu.Value.Input.UnmarshalSmithyDocument(&inputMap)
		}
		q, _ := inputMap["query"].(string)
		q = strings.TrimSpace(q)
		if q == "" || seen[q] || len(queries) >= maxRAGToolQueries {
			continue
		}
		seen[q] = true
		queries = append(queries, q)
	}
	return queries, toolInvoked
}

func addUsage(a, b services.ChatCompletionUsage) services.ChatCompletionUsage {
	return services.ChatCompletionUsage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
	}
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
