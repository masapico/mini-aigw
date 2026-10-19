package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	agentDocument "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/document"
	agentTypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/google/uuid"

	"mini-aigw/internal/config"
	"mini-aigw/internal/prompts"
)

type BedrockService struct {
	awsBundle *AWSClientBundle
	xberg     *XbergService
	cfg       *config.Config
}

func NewBedrockService(awsBundle *AWSClientBundle, xberg *XbergService, cfg *config.Config) *BedrockService {
	return &BedrockService{
		awsBundle: awsBundle,
		xberg:     xberg,
		cfg:       cfg,
	}
}

// ==============================================================================
// 1. OpenAI 互換メッセージ ⇆ Bedrock Converse 変換
// ==============================================================================

func detectImageFormatWithSupport(headerOrName string) (bedrockTypes.ImageFormat, bool) {
	h := strings.ToLower(headerOrName)
	if strings.Contains(h, "image/png") || strings.HasSuffix(h, ".png") {
		return bedrockTypes.ImageFormatPng, true
	}
	if strings.Contains(h, "image/jpeg") || strings.Contains(h, "image/jpg") || strings.HasSuffix(h, ".jpg") || strings.HasSuffix(h, ".jpeg") {
		return bedrockTypes.ImageFormatJpeg, true
	}
	if strings.Contains(h, "image/gif") || strings.HasSuffix(h, ".gif") {
		return bedrockTypes.ImageFormatGif, true
	}
	if strings.Contains(h, "image/webp") || strings.HasSuffix(h, ".webp") {
		return bedrockTypes.ImageFormatWebp, true
	}
	return bedrockTypes.ImageFormatJpeg, false
}

func detectImageFormat(headerOrName string) bedrockTypes.ImageFormat {
	fmtType, _ := detectImageFormatWithSupport(headerOrName)
	return fmtType
}

func inferFilenameFromHeaderOrData(header, filename, b64Data string) string {
	h := strings.ToLower(header)
	if idx := strings.Index(b64Data, ";base64,"); idx != -1 && h == "" {
		h = strings.ToLower(b64Data[:idx])
	}

	ext := ""
	switch {
	case strings.Contains(h, "application/pdf"):
		ext = ".pdf"
	case strings.Contains(h, "wordprocessingml") || strings.Contains(h, "application/msword"):
		ext = ".docx"
	case strings.Contains(h, "spreadsheetml") || strings.Contains(h, "application/vnd.ms-excel"):
		ext = ".xlsx"
	case strings.Contains(h, "presentationml") || strings.Contains(h, "application/vnd.ms-powerpoint"):
		ext = ".pptx"
	case strings.Contains(h, "text/csv"):
		ext = ".csv"
	case strings.Contains(h, "text/markdown"):
		ext = ".md"
	case strings.Contains(h, "text/html"):
		ext = ".html"
	case strings.Contains(h, "text/plain"):
		ext = ".txt"
	case strings.Contains(h, "application/json"):
		ext = ".json"
	}

	// Check raw base64 prefix for %PDF- ("JVBERi0")
	rawB64 := b64Data
	if idx := strings.Index(rawB64, ","); idx != -1 {
		rawB64 = rawB64[idx+1:]
	}
	if ext == "" && strings.HasPrefix(strings.TrimSpace(rawB64), "JVBERi0") {
		ext = ".pdf"
	}

	if filename == "" || filename == "document.bin" || filename == "file.bin" {
		if ext != "" {
			return "document" + ext
		}
		if filename != "" {
			return filename
		}
		return "document.bin"
	}

	if ext != "" && !strings.Contains(filename, ".") {
		return filename + ext
	}
	return filename
}

func extractAttachmentFields(part map[string]interface{}) (b64Body string, filename string, mimeHeader string) {
	b64Body, _ = part["data"].(string)
	if b64Body == "" {
		b64Body, _ = part["content"].(string)
	}
	if b64Body == "" {
		b64Body, _ = part["file_data"].(string)
	}

	filename, _ = part["name"].(string)
	if filename == "" {
		filename, _ = part["filename"].(string)
	}

	mimeHeader, _ = part["mime_type"].(string)
	if mimeHeader == "" {
		mimeHeader, _ = part["media_type"].(string)
	}

	// Support OpenAI nested {"type": "file", "file": {"filename": "...", "file_data": "..."}}
	if fileObj, ok := part["file"].(map[string]interface{}); ok {
		if b64Body == "" {
			b64Body, _ = fileObj["file_data"].(string)
		}
		if b64Body == "" {
			b64Body, _ = fileObj["data"].(string)
		}
		if b64Body == "" {
			b64Body, _ = fileObj["content"].(string)
		}
		if filename == "" {
			filename, _ = fileObj["filename"].(string)
		}
		if filename == "" {
			filename, _ = fileObj["name"].(string)
		}
		if mimeHeader == "" {
			mimeHeader, _ = fileObj["mime_type"].(string)
		}
		if mimeHeader == "" {
			mimeHeader, _ = fileObj["media_type"].(string)
		}
	}

	// Support Anthropic/Claude nested {"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "..."}}
	if srcObj, ok := part["source"].(map[string]interface{}); ok {
		if b64Body == "" {
			b64Body, _ = srcObj["data"].(string)
		}
		if b64Body == "" {
			b64Body, _ = srcObj["content"].(string)
		}
		if filename == "" {
			filename, _ = srcObj["filename"].(string)
		}
		if filename == "" {
			filename, _ = srcObj["name"].(string)
		}
		if mimeHeader == "" {
			mimeHeader, _ = srcObj["media_type"].(string)
		}
		if mimeHeader == "" {
			mimeHeader, _ = srcObj["mime_type"].(string)
		}
	}

	return b64Body, filename, mimeHeader
}

func decodeBase64Flexible(b64 string) ([]byte, error) {
	raw := b64
	if idx := strings.Index(raw, ";base64,"); idx != -1 {
		raw = raw[idx+len(";base64,"):]
	} else if idx := strings.Index(raw, ","); idx != -1 {
		raw = raw[idx+1:]
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(raw)
	}
	return data, err
}

func (s *BedrockService) ConvertOpenAIMessagesToBedrock(
	ctx context.Context,
	rawMessages []map[string]interface{},
) ([]bedrockTypes.SystemContentBlock, []bedrockTypes.Message) {
	var systemPrompts []bedrockTypes.SystemContentBlock
	var bedrockMessages []bedrockTypes.Message

	for _, msg := range rawMessages {
		role, _ := msg["role"].(string)
		rawContent := msg["content"]

		if role == "system" {
			switch c := rawContent.(type) {
			case string:
				if c != "" {
					systemPrompts = append(systemPrompts, &bedrockTypes.SystemContentBlockMemberText{Value: c})
				}
			case []interface{}:
				for _, part := range c {
					if pMap, ok := part.(map[string]interface{}); ok {
						if t, ok := pMap["text"].(string); ok && t != "" {
							systemPrompts = append(systemPrompts, &bedrockTypes.SystemContentBlockMemberText{Value: t})
						}
					}
				}
			}
			continue
		}

		converseRole := bedrockTypes.ConversationRoleUser
		if role == "assistant" {
			converseRole = bedrockTypes.ConversationRoleAssistant
		}

		var contentBlocks []bedrockTypes.ContentBlock

		switch c := rawContent.(type) {
		case string:
			if c != "" {
				contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberText{Value: c})
			}
		case []interface{}:
			for _, item := range c {
				part, ok := item.(map[string]interface{})
				if !ok {
					continue
				}

				partType, _ := part["type"].(string)
				switch partType {
				case "text":
					textVal, _ := part["text"].(string)
					if textVal != "" {
						contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberText{Value: textVal})
					}
				case "image_url":
					imgData, _ := part["image_url"].(map[string]interface{})
					url, _ := imgData["url"].(string)
					if strings.HasPrefix(url, "data:") {
						header := ""
						b64Body := url
						if idx := strings.Index(url, ","); idx != -1 {
							header = url[:idx]
							b64Body = url[idx+1:]
						}
						if fmtType, isImg := detectImageFormatWithSupport(header); isImg {
							rawBytes, err := decodeBase64Flexible(b64Body)
							if err == nil {
								contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberImage{
									Value: bedrockTypes.ImageBlock{
										Format: fmtType,
										Source: &bedrockTypes.ImageSourceMemberBytes{Value: rawBytes},
									},
								})
							} else {
								contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberText{Value: "[Image decode error]"})
							}
						} else {
							// Non-image data URI (e.g., application/pdf) sent via image_url
							hintName, _ := part["name"].(string)
							if hintName == "" {
								hintName, _ = part["filename"].(string)
							}
							filename := inferFilenameFromHeaderOrData(header, hintName, b64Body)
							extracted := s.xberg.ExtractContentFromBase64(ctx, url, filename)
							contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberText{
								Value: fmt.Sprintf("--- Attached Document: %s ---\n%s\n--- End of Document ---", filename, extracted),
							})
						}
					} else {
						contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberText{Value: fmt.Sprintf("[Image URL: %s]", url)})
					}
				case "file", "document", "input_file":
					b64Body, rawFilename, mimeHeader := extractAttachmentFields(part)
					filename := inferFilenameFromHeaderOrData(mimeHeader, rawFilename, b64Body)
					if fmtType, isImg := detectImageFormatWithSupport(mimeHeader + " " + filename); isImg {
						if rawBytes, err := decodeBase64Flexible(b64Body); err == nil {
							contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberImage{
								Value: bedrockTypes.ImageBlock{
									Format: fmtType,
									Source: &bedrockTypes.ImageSourceMemberBytes{Value: rawBytes},
								},
							})
							continue
						}
					}
					extracted := s.xberg.ExtractContentFromBase64(ctx, b64Body, filename)
					contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberText{
						Value: fmt.Sprintf("--- Attached Document: %s ---\n%s\n--- End of Document ---", filename, extracted),
					})
				default:
					if b64Body, rawFilename, mimeHeader := extractAttachmentFields(part); b64Body != "" {
						filename := inferFilenameFromHeaderOrData(mimeHeader, rawFilename, b64Body)
						if fmtType, isImg := detectImageFormatWithSupport(mimeHeader + " " + filename); isImg {
							if rawBytes, err := decodeBase64Flexible(b64Body); err == nil {
								contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberImage{
									Value: bedrockTypes.ImageBlock{
										Format: fmtType,
										Source: &bedrockTypes.ImageSourceMemberBytes{Value: rawBytes},
									},
								})
								continue
							}
						}
						extracted := s.xberg.ExtractContentFromBase64(ctx, b64Body, filename)
						contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberText{
							Value: fmt.Sprintf("--- Attached Document: %s ---\n%s\n--- End of Document ---", filename, extracted),
						})
					}
				}
			}
		}

		if len(contentBlocks) == 0 {
			contentBlocks = append(contentBlocks, &bedrockTypes.ContentBlockMemberText{Value: " "})
		}

		bedrockMessages = append(bedrockMessages, bedrockTypes.Message{
			Role:    converseRole,
			Content: contentBlocks,
		})
	}

	return systemPrompts, bedrockMessages
}

// ==============================================================================
// 2. InferenceConfig 生成
// ==============================================================================

func (s *BedrockService) BuildInferenceConfig(
	temperature *float64,
	topP *float64,
	maxTokens *int,
) *bedrockTypes.InferenceConfiguration {
	config := &bedrockTypes.InferenceConfiguration{}

	effectiveMaxTokens := s.cfg.DefaultMaxTokens
	if maxTokens != nil {
		effectiveMaxTokens = *maxTokens
	}
	if effectiveMaxTokens > s.cfg.MaxAllowedTokens {
		effectiveMaxTokens = s.cfg.MaxAllowedTokens
	}
	if effectiveMaxTokens < 1 {
		effectiveMaxTokens = 1
	}
	config.MaxTokens = aws.Int32(int32(effectiveMaxTokens))

	var sanitizedTemp *float32
	if temperature != nil {
		t := float32(*temperature)
		if t < 0.0 {
			t = 0.0
		} else if t > 1.0 {
			t = 1.0
		}
		sanitizedTemp = &t
	} else if s.cfg.DefaultTemperature != nil {
		t := float32(*s.cfg.DefaultTemperature)
		if t < 0.0 {
			t = 0.0
		} else if t > 1.0 {
			t = 1.0
		}
		sanitizedTemp = &t
	}

	var sanitizedTopP *float32
	if topP != nil {
		p := float32(*topP)
		if p < 0.0 {
			p = 0.0
		} else if p > 1.0 {
			p = 1.0
		}
		sanitizedTopP = &p
	}

	if sanitizedTemp != nil && sanitizedTopP != nil {
		tempIsDefault := (*temperature == 1.0)
		topPIsDefault := (*topP == 1.0)

		if topPIsDefault && !tempIsDefault {
			config.Temperature = sanitizedTemp
		} else if tempIsDefault && !topPIsDefault {
			config.TopP = sanitizedTopP
		} else {
			// Prioritize temperature
			config.Temperature = sanitizedTemp
		}
	} else if sanitizedTemp != nil {
		config.Temperature = sanitizedTemp
	} else if sanitizedTopP != nil {
		config.TopP = sanitizedTopP
	}

	return config
}

// ==============================================================================
// 3. 非ストリーミング Converse 呼び出し
// ==============================================================================

type ChatCompletionChoice struct {
	Index        int                    `json:"index"`
	Message      map[string]interface{} `json:"message"`
	FinishReason string                 `json:"finish_reason"`
}

type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatCompletionResponse struct {
	ID           string                 `json:"id"`
	Object       string                 `json:"object"`
	Created      int64                  `json:"created"`
	Model        string                 `json:"model"`
	Choices      []ChatCompletionChoice `json:"choices"`
	Usage        ChatCompletionUsage    `json:"usage"`
	Citations    []interface{}          `json:"citations,omitempty"`
	StopReason   string                 `json:"stop_reason,omitempty"`
	RawOutputMsg interface{}            `json:"-"`
}

func (s *BedrockService) CallConverse(
	ctx context.Context,
	modelID string,
	systemPrompts []bedrockTypes.SystemContentBlock,
	messages []bedrockTypes.Message,
	temperature *float64,
	topP *float64,
	maxTokens *int,
	toolConfig *bedrockTypes.ToolConfiguration,
) (*ChatCompletionResponse, error) {
	infConfig := s.BuildInferenceConfig(temperature, topP, maxTokens)

	input := &bedrockruntime.ConverseInput{
		ModelId:         aws.String(modelID),
		Messages:        messages,
		InferenceConfig: infConfig,
	}
	if len(systemPrompts) > 0 {
		input.System = systemPrompts
	}
	if toolConfig != nil {
		input.ToolConfig = toolConfig
	}

	res, err := s.awsBundle.BedrockRuntime.Converse(ctx, input)
	if err != nil {
		if s.cfg != nil && s.cfg.Debug {
			log.Printf("[DEBUG] Bedrock Converse failed for model %s: %v", modelID, err)
		}
		return nil, fmt.Errorf("bedrock converse failed: %w", err)
	}

	var responseText strings.Builder
	var toolUseCalls []bedrockTypes.ToolUseBlock

	if outputMsg, ok := res.Output.(*bedrockTypes.ConverseOutputMemberMessage); ok {
		for _, block := range outputMsg.Value.Content {
			switch b := block.(type) {
			case *bedrockTypes.ContentBlockMemberText:
				responseText.WriteString(b.Value)
			case *bedrockTypes.ContentBlockMemberToolUse:
				toolUseCalls = append(toolUseCalls, b.Value)
			}
		}
	}

	var inputTokens, outputTokens int
	if res.Usage != nil {
		if res.Usage.InputTokens != nil {
			inputTokens = int(*res.Usage.InputTokens)
		}
		if res.Usage.OutputTokens != nil {
			outputTokens = int(*res.Usage.OutputTokens)
		}
	}

	stopReason := string(res.StopReason)
	finishReason := "stop"
	if stopReason != "end_turn" && stopReason != "" {
		finishReason = stopReason
	}
	if len(toolUseCalls) > 0 {
		finishReason = "tool_calls"
	}

	completionID := fmt.Sprintf("chatcmpl-%s", uuid.New().String()[:12])
	nowUnix := time.Now().Unix()

	resp := &ChatCompletionResponse{
		ID:      completionID,
		Object:  "chat.completion",
		Created: nowUnix,
		Model:   modelID,
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: map[string]interface{}{
					"role":    "assistant",
					"content": responseText.String(),
				},
				FinishReason: finishReason,
			},
		},
		Usage: ChatCompletionUsage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
			TotalTokens:      inputTokens + outputTokens,
		},
		StopReason:   stopReason,
		RawOutputMsg: res.Output,
	}

	return resp, nil
}

// ==============================================================================
// 4. ストリーミング ConverseStream 呼び出し (SSE 出力)
// ==============================================================================

type StreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type StreamChunk struct {
	ID        string                 `json:"id"`
	Object    string                 `json:"object"`
	Created   int64                  `json:"created"`
	Model     string                 `json:"model"`
	Choices   []StreamChoice         `json:"choices"`
	Usage     *ChatCompletionUsage   `json:"usage,omitempty"`
	Citations []interface{}          `json:"citations,omitempty"`
}

func (s *BedrockService) CallConverseStream(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	modelID string,
	systemPrompts []bedrockTypes.SystemContentBlock,
	messages []bedrockTypes.Message,
	temperature *float64,
	topP *float64,
	maxTokens *int,
	toolConfig *bedrockTypes.ToolConfiguration,
	citations []interface{},
	priorUsage *ChatCompletionUsage,
	onComplete func(usage ChatCompletionUsage, actualCitations []interface{}, fullAnswer string),
) error {
	infConfig := s.BuildInferenceConfig(temperature, topP, maxTokens)

	input := &bedrockruntime.ConverseStreamInput{
		ModelId:         aws.String(modelID),
		Messages:        messages,
		InferenceConfig: infConfig,
	}
	if len(systemPrompts) > 0 {
		input.System = systemPrompts
	}
	if toolConfig != nil {
		input.ToolConfig = toolConfig
	}

	res, err := s.awsBundle.BedrockRuntime.ConverseStream(ctx, input)
	if err != nil {
		if s.cfg != nil && s.cfg.Debug {
			log.Printf("[DEBUG] Bedrock ConverseStream failed for model %s: %v", modelID, err)
		}
		return fmt.Errorf("bedrock converse_stream error: %w", err)
	}
	stream := res.GetStream()
	defer stream.Close()

	completionID := fmt.Sprintf("chatcmpl-%s", uuid.New().String()[:12])
	nowUnix := time.Now().Unix()

	// Initial role chunk
	initialChunk := StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: nowUnix,
		Model:   modelID,
		Choices: []StreamChoice{
			{
				Index: 0,
				Delta: StreamDelta{Role: "assistant", Content: ""},
			},
		},
	}
	sendSSEChunk(w, flusher, initialChunk)

	var streamInputTokens, streamOutputTokens int
	finalStopReason := "end_turn"
	var fullAnswer strings.Builder

	for event := range stream.Events() {
		switch e := event.(type) {
		case *bedrockTypes.ConverseStreamOutputMemberContentBlockDelta:
			if deltaText, ok := e.Value.Delta.(*bedrockTypes.ContentBlockDeltaMemberText); ok {
				fullAnswer.WriteString(deltaText.Value)
				chunk := StreamChunk{
					ID:      completionID,
					Object:  "chat.completion.chunk",
					Created: nowUnix,
					Model:   modelID,
					Choices: []StreamChoice{
						{
							Index: 0,
							Delta: StreamDelta{Content: deltaText.Value},
						},
					},
				}
				sendSSEChunk(w, flusher, chunk)
			}
		case *bedrockTypes.ConverseStreamOutputMemberMessageStop:
			finalStopReason = string(e.Value.StopReason)
		case *bedrockTypes.ConverseStreamOutputMemberMetadata:
			if e.Value.Usage != nil {
				if e.Value.Usage.InputTokens != nil {
					streamInputTokens = int(*e.Value.Usage.InputTokens)
				}
				if e.Value.Usage.OutputTokens != nil {
					streamOutputTokens = int(*e.Value.Usage.OutputTokens)
				}
			}
		}
	}

	// Calculate combined usage
	priorPrompt := 0
	priorCompletion := 0
	if priorUsage != nil {
		priorPrompt = priorUsage.PromptTokens
		priorCompletion = priorUsage.CompletionTokens
	}
	totalPrompt := priorPrompt + streamInputTokens
	totalCompletion := priorCompletion + streamOutputTokens
	combinedUsage := ChatCompletionUsage{
		PromptTokens:     totalPrompt,
		CompletionTokens: totalCompletion,
		TotalTokens:      totalPrompt + totalCompletion,
	}

	finishReasonStr := "stop"
	if finalStopReason != "end_turn" && finalStopReason != "" {
		finishReasonStr = finalStopReason
	}

	actualCitations, _ := FilterInterfaceCitations(fullAnswer.String(), citations)

	endChunk := StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: nowUnix,
		Model:   modelID,
		Choices: []StreamChoice{
			{
				Index:        0,
				Delta:        StreamDelta{},
				FinishReason: &finishReasonStr,
			},
		},
		Usage:     &combinedUsage,
		Citations: actualCitations,
	}
	sendSSEChunk(w, flusher, endChunk)

	if onComplete != nil {
		onComplete(combinedUsage, actualCitations, fullAnswer.String())
	}

	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()

	return nil
}

func (s *BedrockService) StreamPrebuiltTextAsSSE(
	w http.ResponseWriter,
	flusher http.Flusher,
	modelID string,
	text string,
	stopReason string,
	usage *ChatCompletionUsage,
	citations []interface{},
	onComplete func(usage ChatCompletionUsage),
) {
	completionID := fmt.Sprintf("chatcmpl-%s", uuid.New().String()[:12])
	nowUnix := time.Now().Unix()

	finishReason := "stop"
	if stopReason != "end_turn" && stopReason != "" {
		finishReason = stopReason
	}

	// Initial role chunk
	sendSSEChunk(w, flusher, StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: nowUnix,
		Model:   modelID,
		Choices: []StreamChoice{{Index: 0, Delta: StreamDelta{Role: "assistant"}}},
	})

	if text != "" {
		sendSSEChunk(w, flusher, StreamChunk{
			ID:      completionID,
			Object:  "chat.completion.chunk",
			Created: nowUnix,
			Model:   modelID,
			Choices: []StreamChoice{{Index: 0, Delta: StreamDelta{Content: text}}},
		})
	}

	endChunk := StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: nowUnix,
		Model:   modelID,
		Choices: []StreamChoice{{Index: 0, Delta: StreamDelta{}, FinishReason: &finishReason}},
		Usage:   usage,
		Citations: citations,
	}
	sendSSEChunk(w, flusher, endChunk)

	if onComplete != nil && usage != nil {
		onComplete(*usage)
	}

	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func sendSSEChunk(w http.ResponseWriter, flusher http.Flusher, chunk StreamChunk) {
	data, _ := json.Marshal(chunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// ==============================================================================
// 5. Knowledge Base Retrieve API & Pre-Filtering
// ==============================================================================

type RetrievalResult struct {
	Content  string                 `json:"content"`
	Location map[string]interface{} `json:"location"`
	Metadata map[string]interface{} `json:"metadata"`
	Score    float64                `json:"score"`
}

func BuildRetrievalFilter(
	allowedGroups []string,
	tags []string,
	documentIDs []string,
) agentTypes.RetrievalFilter {
	// 1. Group conditions: 'public' + allowedGroups
	groupSet := make(map[string]bool)
	groupSet["public"] = true
	for _, g := range allowedGroups {
		if strings.TrimSpace(g) != "" {
			groupSet[strings.TrimSpace(g)] = true
		}
	}
	var groupsToInclude []string
	for g := range groupSet {
		groupsToInclude = append(groupsToInclude, g)
	}

	var groupFilter agentTypes.RetrievalFilter
	if len(groupsToInclude) == 1 {
		groupFilter = &agentTypes.RetrievalFilterMemberEquals{
			Value: agentTypes.FilterAttribute{
				Key:   aws.String("group"),
				Value: agentDocument.NewLazyDocument(groupsToInclude[0]),
			},
		}
	} else {
		var orList []agentTypes.RetrievalFilter
		for _, grp := range groupsToInclude {
			orList = append(orList, &agentTypes.RetrievalFilterMemberEquals{
				Value: agentTypes.FilterAttribute{
					Key:   aws.String("group"),
					Value: agentDocument.NewLazyDocument(grp),
				},
			})
		}
		groupFilter = &agentTypes.RetrievalFilterMemberOrAll{
			Value: orList,
		}
	}

	var cleanTags []string
	for _, t := range tags {
		if strings.TrimSpace(t) != "" {
			cleanTags = append(cleanTags, strings.TrimSpace(t))
		}
	}
	var cleanDocIDs []string
	for _, d := range documentIDs {
		if strings.TrimSpace(d) != "" {
			cleanDocIDs = append(cleanDocIDs, strings.TrimSpace(d))
		}
	}

	if len(cleanTags) == 0 && len(cleanDocIDs) == 0 {
		return groupFilter
	}

	var metaConditions []agentTypes.RetrievalFilter
	for _, t := range cleanTags {
		metaConditions = append(metaConditions, &agentTypes.RetrievalFilterMemberListContains{
			Value: agentTypes.FilterAttribute{
				Key:   aws.String("tags"),
				Value: agentDocument.NewLazyDocument(t),
			},
		})
	}

	if len(cleanDocIDs) == 1 {
		metaConditions = append(metaConditions, &agentTypes.RetrievalFilterMemberEquals{
			Value: agentTypes.FilterAttribute{
				Key:   aws.String("document_id"),
				Value: agentDocument.NewLazyDocument(cleanDocIDs[0]),
			},
		})
	} else if len(cleanDocIDs) > 1 {
		metaConditions = append(metaConditions, &agentTypes.RetrievalFilterMemberIn{
			Value: agentTypes.FilterAttribute{
				Key:   aws.String("document_id"),
				Value: agentDocument.NewLazyDocument(cleanDocIDs),
			},
		})
	}

	var metaFilter agentTypes.RetrievalFilter
	if len(metaConditions) == 1 {
		metaFilter = metaConditions[0]
	} else {
		metaFilter = &agentTypes.RetrievalFilterMemberOrAll{
			Value: metaConditions,
		}
	}

	return &agentTypes.RetrievalFilterMemberAndAll{
		Value: []agentTypes.RetrievalFilter{groupFilter, metaFilter},
	}
}

func (s *BedrockService) RetrieveKnowledge(
	ctx context.Context,
	kbID string,
	query string,
	allowedGroups []string,
	numberOfResults int,
	tags []string,
	matchedTags []string,
	matchedDocIDs []string,
) ([]RetrievalResult, error) {
	if numberOfResults <= 0 {
		numberOfResults = s.cfg.RAGNumberOfResults
	}

	filter := BuildRetrievalFilter(allowedGroups, tags, nil)
	results, err := s.executeSingleRetrieve(ctx, kbID, query, filter, numberOfResults)
	if err != nil {
		return nil, err
	}

	// 0 results fallback with natural query
	if len(results) == 0 {
		fallbackQuery := prompts.BuildFallbackNaturalQuery(query)
		if fallbackQuery != nil {
			log.Printf("Managed KB returned 0 results for short query '%s'; retrying with '%s'", query, *fallbackQuery)
			results, _ = s.executeSingleRetrieve(ctx, kbID, *fallbackQuery, filter, numberOfResults)
		}
	}

	// Hybrid metadata search if matched tags or doc IDs detected in query
	if len(tags) == 0 && (len(matchedTags) > 0 || len(matchedDocIDs) > 0) {
		metaFilter := BuildRetrievalFilter(allowedGroups, matchedTags, matchedDocIDs)
		effectiveQuery := query
		if fb := prompts.BuildFallbackNaturalQuery(query); fb != nil {
			effectiveQuery = *fb
		}
		metaResults, err := s.executeSingleRetrieve(ctx, kbID, effectiveQuery, metaFilter, numberOfResults)
		if err == nil && len(metaResults) > 0 {
			results = mergeRetrievalResults(results, metaResults, numberOfResults)
		}
	}

	return results, nil
}

func (s *BedrockService) executeSingleRetrieve(
	ctx context.Context,
	kbID string,
	query string,
	filter agentTypes.RetrievalFilter,
	limit int,
) ([]RetrievalResult, error) {
	managedSearch := &agentTypes.ManagedSearchConfiguration{
		NumberOfResults: aws.Int32(int32(limit)),
	}
	if filter != nil {
		managedSearch.Filter = filter
	}

	retrievalConfig := &agentTypes.KnowledgeBaseRetrievalConfiguration{
		ManagedSearchConfiguration: managedSearch,
	}

	input := &bedrockagentruntime.RetrieveInput{
		KnowledgeBaseId: aws.String(kbID),
		RetrievalQuery: &agentTypes.KnowledgeBaseQuery{
			Text: aws.String(query),
		},
		RetrievalConfiguration: retrievalConfig,
	}

	resp, err := s.awsBundle.BedrockAgentRuntime.Retrieve(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "Incompatible configuration") {
			log.Printf("Bedrock Knowledge Base configuration incompatibility detected: %v. This application is configured for managed knowledge bases (managedSearchConfiguration).", err)
		} else {
			log.Printf("Error retrieving knowledge from Bedrock KB: %v", err)
		}
		return nil, err
	}

	results := make([]RetrievalResult, 0)
	for _, item := range resp.RetrievalResults {
		text := ""
		if item.Content != nil && item.Content.Text != nil {
			text = *item.Content.Text
		}
		score := 0.0
		if item.Score != nil {
			score = *item.Score
		}

		locMap := make(map[string]interface{})
		if item.Location != nil && item.Location.S3Location != nil && item.Location.S3Location.Uri != nil {
			locMap["s3Location"] = map[string]interface{}{"uri": *item.Location.S3Location.Uri}
		}

		metaMap := make(map[string]interface{})
		for k, v := range item.Metadata {
			var unmarshaled interface{}
			if err := v.UnmarshalSmithyDocument(&unmarshaled); err == nil {
				metaMap[k] = unmarshaled
			}
		}

		results = append(results, RetrievalResult{
			Content:  text,
			Location: locMap,
			Metadata: metaMap,
			Score:    score,
		})
	}

	return results, nil
}

func mergeRetrievalResults(primary, secondary []RetrievalResult, limit int) []RetrievalResult {
	mergedMap := make(map[string]RetrievalResult)

	for _, item := range append(primary, secondary...) {
		s3URI := ""
		if loc, ok := item.Location["s3Location"].(map[string]interface{}); ok {
			s3URI, _ = loc["uri"].(string)
		}
		key := fmt.Sprintf("%s:%s", s3URI, item.Content)
		if chunkID, ok := item.Metadata["_chunk_id"].(string); ok && chunkID != "" {
			key = chunkID
		}

		existing, found := mergedMap[key]
		if !found || item.Score > existing.Score {
			mergedMap[key] = item
		}
	}

	var list []RetrievalResult
	for _, v := range mergedMap {
		list = append(list, v)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Score > list[j].Score
	})

	if len(list) > limit {
		list = list[:limit]
	}
	return list
}

func FilterKnowledgeResultsByScore(results []RetrievalResult, threshold float64) []RetrievalResult {
	var filtered []RetrievalResult
	for _, r := range results {
		if r.Score >= threshold {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
