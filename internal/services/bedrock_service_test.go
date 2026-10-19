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

	"mini-aigw/internal/config"
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

