package services

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"

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

