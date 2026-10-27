package services

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

type mockTransport struct {
	roundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTripFunc(req)
}

func TestExecuteSingleRetrieve_ManagedSearchConfiguration(t *testing.T) {
	var capturedBody string
	mockClient := &http.Client{
		Transport: &mockTransport{
			roundTripFunc: func(r *http.Request) (*http.Response, error) {
				bodyBytes, _ := io.ReadAll(r.Body)
				capturedBody = string(bodyBytes)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"retrievalResults": []}`)),
				}, nil
			},
		},
	}

	customClient := bedrockagentruntime.New(bedrockagentruntime.Options{
		Region:      "us-east-1",
		HTTPClient:  mockClient,
		Credentials: aws.AnonymousCredentials{},
	})

	bundle := &AWSClientBundle{
		BedrockAgentRuntime: customClient,
	}
	svc := NewBedrockService(bundle, nil, &config.Config{RAGNumberOfResults: 5})

	filter := BuildRetrievalFilter([]string{"sales"}, nil, nil)
	results, err := svc.executeSingleRetrieve(context.Background(), "test-kb-id", "test query", filter, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results == nil {
		t.Fatalf("expected non-nil results slice")
	}

	if !strings.Contains(capturedBody, "managedSearchConfiguration") {
		t.Errorf("expected request body to contain managedSearchConfiguration, got: %s", capturedBody)
	}
	if strings.Contains(capturedBody, "vectorSearchConfiguration") {
		t.Errorf("request body should NOT contain vectorSearchConfiguration, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, `"numberOfResults":3`) {
		t.Errorf("expected request body to contain numberOfResults:3, got: %s", capturedBody)
	}
}

func TestFilterKnowledgeResultsByScore(t *testing.T) {
	results := []RetrievalResult{
		{Content: "doc1", Score: 0.9},
		{Content: "doc2", Score: 0.5},
		{Content: "doc3", Score: 0.75},
	}

	filtered := FilterKnowledgeResultsByScore(results, 0.7)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 results, got %d", len(filtered))
	}
	if filtered[0].Content != "doc1" || filtered[1].Content != "doc3" {
		t.Errorf("unexpected filtered results: %+v", filtered)
	}
}

func TestExecuteSingleRetrieve_IncompatibleConfigError(t *testing.T) {
	mockClient := &http.Client{
		Transport: &mockTransport{
			roundTripFunc: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"__type":"ValidationException","message":"Incompatible configuration: vectorSearchConfiguration is not supported for managed knowledge bases."}`)),
				}, nil
			},
		},
	}

	customClient := bedrockagentruntime.New(bedrockagentruntime.Options{
		Region:      "us-east-1",
		HTTPClient:  mockClient,
		Credentials: aws.AnonymousCredentials{},
	})

	bundle := &AWSClientBundle{
		BedrockAgentRuntime: customClient,
	}
	svc := NewBedrockService(bundle, nil, &config.Config{RAGNumberOfResults: 5})

	_, err := svc.executeSingleRetrieve(context.Background(), "test-kb-id", "test query", nil, 3)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Incompatible configuration") {
		t.Errorf("expected error to mention 'Incompatible configuration', got: %v", err)
	}
}

func TestConvertOpenAIMessagesToBedrock_Attachments(t *testing.T) {
	cfg := &config.Config{XbergPath: "non_existent_xberg_binary"}
	xbergSvc := NewXbergService(cfg)
	svc := NewBedrockService(nil, xbergSvc, cfg)

	docText := "Extracted Sample Text"
	b64Doc := base64.StdEncoding.EncodeToString([]byte(docText))

	// 1. PDF data URI sent via image_url should NOT become an ImageBlock (which fails in Bedrock),
	//    instead it should be extracted via XbergService into a ContentBlockMemberText.
	rawMessages := []map[string]interface{}{
		{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Please summarize",
				},
				map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url": "data:application/pdf;base64," + b64Doc,
					},
				},
				map[string]interface{}{
					"type": "file",
					"file": map[string]interface{}{
						"filename":  "report.pdf",
						"file_data": "data:application/pdf;base64," + b64Doc,
					},
				},
				map[string]interface{}{
					"type": "document",
					"source": map[string]interface{}{
						"type":       "base64",
						"media_type": "application/pdf",
						"data":       b64Doc,
					},
				},
				map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("fakepng")),
					},
				},
			},
		},
	}

	_, msgs := svc.ConvertOpenAIMessagesToBedrock(context.Background(), rawMessages)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	blocks := msgs[0].Content
	if len(blocks) != 5 {
		t.Fatalf("expected 5 content blocks, got %d", len(blocks))
	}

	// Block 1: PDF via image_url -> Text block with document.pdf
	tb1, ok := blocks[1].(*bedrockTypes.ContentBlockMemberText)
	if !ok {
		t.Fatalf("expected block[1] (PDF via image_url) to be ContentBlockMemberText, got %T", blocks[1])
	}
	if !strings.Contains(tb1.Value, "document.pdf") || !strings.Contains(tb1.Value, docText) {
		t.Errorf("unexpected block[1] text: %s", tb1.Value)
	}

	// Block 2: Nested OpenAI file -> Text block with report.pdf
	tb2, ok := blocks[2].(*bedrockTypes.ContentBlockMemberText)
	if !ok {
		t.Fatalf("expected block[2] (nested file) to be ContentBlockMemberText, got %T", blocks[2])
	}
	if !strings.Contains(tb2.Value, "report.pdf") || !strings.Contains(tb2.Value, docText) {
		t.Errorf("unexpected block[2] text: %s", tb2.Value)
	}

	// Block 3: Anthropic document source -> Text block with document.pdf
	tb3, ok := blocks[3].(*bedrockTypes.ContentBlockMemberText)
	if !ok {
		t.Fatalf("expected block[3] (document source) to be ContentBlockMemberText, got %T", blocks[3])
	}
	if !strings.Contains(tb3.Value, "document.pdf") || !strings.Contains(tb3.Value, docText) {
		t.Errorf("unexpected block[3] text: %s", tb3.Value)
	}

	// Block 4: Actual PNG via image_url -> Image block
	imgBlock, ok := blocks[4].(*bedrockTypes.ContentBlockMemberImage)
	if !ok {
		t.Fatalf("expected block[4] (PNG via image_url) to be ContentBlockMemberImage, got %T", blocks[4])
	}
	if imgBlock.Value.Format != bedrockTypes.ImageFormatPng {
		t.Errorf("expected PNG format, got %v", imgBlock.Value.Format)
	}
}

func TestBuildInferenceConfigForModel_SamplingUnsupported(t *testing.T) {
	cfg := &config.Config{
		DefaultMaxTokens:   2048,
		MaxAllowedTokens:   4096,
		DefaultTemperature: nil,
	}
	svc := NewBedrockService(nil, nil, cfg)

	temp := 0.7
	topP := 0.9
	maxTok := 1000

	// 1. Model that does not support sampling (heuristic: jp.anthropic.claude-haiku-5-5)
	confUnsupported := svc.BuildInferenceConfigForModel("jp.anthropic.claude-haiku-5-5", &temp, &topP, &maxTok)
	if confUnsupported.Temperature != nil {
		t.Errorf("expected Temperature to be nil for claude-haiku-5-5, got %v", *confUnsupported.Temperature)
	}
	if confUnsupported.TopP != nil {
		t.Errorf("expected TopP to be nil for claude-haiku-5-5, got %v", *confUnsupported.TopP)
	}
	if confUnsupported.MaxTokens == nil || *confUnsupported.MaxTokens != 1000 {
		t.Errorf("expected MaxTokens to be 1000, got %v", confUnsupported.MaxTokens)
	}

	// 2. Standard model that supports sampling
	confSupported := svc.BuildInferenceConfigForModel("apac.amazon.nova-lite-v1:0", &temp, &topP, &maxTok)
	if confSupported.Temperature == nil || *confSupported.Temperature != float32(temp) {
		t.Errorf("expected Temperature to be %v, got %v", temp, confSupported.Temperature)
	}
	if confSupported.MaxTokens == nil || *confSupported.MaxTokens != 1000 {
		t.Errorf("expected MaxTokens to be 1000, got %v", confSupported.MaxTokens)
	}
}

func TestBuildInferenceConfigForModel_NilSafety(t *testing.T) {
	defTemp := 0.5
	cfg := &config.Config{
		DefaultMaxTokens:   2048,
		MaxAllowedTokens:   4096,
		DefaultTemperature: &defTemp,
	}
	svc := NewBedrockService(nil, nil, cfg)

	topP := 0.8
	// temperature is nil, but DefaultTemperature is set, and topP is set
	// This previously risked nil-pointer dereference (*temperature == 1.0)
	conf := svc.BuildInferenceConfigForModel("standard-model", nil, &topP, nil)
	if conf.Temperature == nil || *conf.Temperature != float32(defTemp) {
		t.Errorf("expected Temperature to be default %v, got %v", defTemp, conf.Temperature)
	}
}

func TestSupportsTemperature_DBIntegration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open memory sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.LLMModel{}); err != nil {
		t.Fatalf("failed to auto migrate: %v", err)
	}

	// Insert models: one with SupportsTemperature = false, one with true
	m1 := models.LLMModel{
		ID:                  "custom.model.no-temp",
		Name:                "Custom Model No Temp",
		SupportsTemperature: models.BoolPtr(false),
		IsActive:            true,
	}
	m2 := models.LLMModel{
		ID:                  "custom.model.with-temp",
		Name:                "Custom Model With Temp",
		SupportsTemperature: models.BoolPtr(true),
		IsActive:            true,
	}
	db.Create(&m1)
	db.Create(&m2)

	cfg := &config.Config{DefaultMaxTokens: 1024, MaxAllowedTokens: 4096}
	svc := NewBedrockService(nil, nil, cfg, db)

	// Verify DB lookup
	if svc.SupportsTemperature("custom.model.no-temp") {
		t.Errorf("expected SupportsTemperature to be false for custom.model.no-temp")
	}
	if !svc.SupportsTemperature("custom.model.with-temp") {
		t.Errorf("expected SupportsTemperature to be true for custom.model.with-temp")
	}

	// Verify BuildInferenceConfigForModel respects DB flag
	temp := 0.7
	confNoTemp := svc.BuildInferenceConfigForModel("custom.model.no-temp", &temp, nil, nil)
	if confNoTemp.Temperature != nil {
		t.Errorf("expected Temperature to be nil for custom.model.no-temp")
	}

	confWithTemp := svc.BuildInferenceConfigForModel("custom.model.with-temp", &temp, nil, nil)
	if confWithTemp.Temperature == nil {
		t.Errorf("expected Temperature to be non-nil for custom.model.with-temp")
	}

	// Update DB and invalidate cache
	db.Model(&models.LLMModel{}).Where("id = ?", "custom.model.no-temp").Update("supports_temperature", true)
	svc.InvalidateSamplingCache("custom.model.no-temp")

	if !svc.SupportsTemperature("custom.model.no-temp") {
		t.Errorf("expected SupportsTemperature to be true after DB update and cache invalidation")
	}
}

type mockResponseWriterFlusher struct {
	header http.Header
	buf    strings.Builder
}

func (m *mockResponseWriterFlusher) Header() http.Header {
	if m.header == nil {
		m.header = make(http.Header)
	}
	return m.header
}

func (m *mockResponseWriterFlusher) Write(b []byte) (int, error) {
	return m.buf.Write(b)
}

func (m *mockResponseWriterFlusher) WriteHeader(statusCode int) {}

func (m *mockResponseWriterFlusher) Flush() {}

func TestSendSSEError(t *testing.T) {
	mw := &mockResponseWriterFlusher{}
	errMsg := "ValidationException: input token count exceeded"

	sendSSEError(mw, mw, errMsg)

	output := mw.buf.String()
	if !strings.Contains(output, `data: {"error":{"message":"ValidationException: input token count exceeded","type":"bedrock_error"}}`) {
		t.Fatalf("expected SSE error JSON payload, got: %q", output)
	}
	if !strings.Contains(output, "data: [DONE]\n\n") {
		t.Fatalf("expected [DONE] terminator, got: %q", output)
	}
}

func TestFormatAttachedDocumentBlock(t *testing.T) {
	// Case 1: text with null bytes and control characters
	raw := "Contract Body\x00\x02\nSection 1\tValid"
	block := formatAttachedDocumentBlock("contract.pdf", raw, nil)
	expected := "--- Attached Document: contract.pdf ---\nContract Body\nSection 1\tValid\n--- End of Document ---"
	if block.Value != expected {
		t.Fatalf("expected %q, got %q", expected, block.Value)
	}

	// Case 2: empty text fallback
	emptyBlock := formatAttachedDocumentBlock("empty.pdf", "", nil)
	expectedEmpty := "--- Attached Document: empty.pdf ---\n[Empty document content: empty.pdf]\n--- End of Document ---"
	if emptyBlock.Value != expectedEmpty {
		t.Fatalf("expected %q, got %q", expectedEmpty, emptyBlock.Value)
	}
}

