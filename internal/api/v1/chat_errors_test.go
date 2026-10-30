package v1

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/smithy-go"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/logging"
	"mini-aigw/internal/models"
	"mini-aigw/internal/services"
)

func TestClassifyBedrockError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"validation", &smithy.GenericAPIError{Code: "ValidationException", Message: "Input is too long for requested model."}, http.StatusBadRequest, "ValidationException"},
		{"throttling", &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Too many requests"}, http.StatusTooManyRequests, "ThrottlingException"},
		{"quota", &smithy.GenericAPIError{Code: "ServiceQuotaExceededException"}, http.StatusTooManyRequests, "ServiceQuotaExceededException"},
		{"model timeout", &smithy.GenericAPIError{Code: "ModelTimeoutException"}, http.StatusGatewayTimeout, "ModelTimeoutException"},
		{"unavailable", &smithy.GenericAPIError{Code: "ServiceUnavailableException"}, http.StatusServiceUnavailable, "ServiceUnavailableException"},
		{"other aws", &smithy.GenericAPIError{Code: "InternalServerException"}, http.StatusBadGateway, "InternalServerException"},
		{"deadline", fmt.Errorf("bedrock converse failed: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, "timeout"},
		{"canceled", fmt.Errorf("bedrock converse failed: %w", context.Canceled), 499, "client_closed_request"},
		{"unknown", errors.New("boom"), http.StatusBadGateway, "upstream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := classifyBedrockError(fmt.Errorf("wrapped: %w", tc.err))
			if c.Status != tc.wantStatus || c.Code != tc.wantCode {
				t.Fatalf("expected %d/%s, got %d/%s", tc.wantStatus, tc.wantCode, c.Status, c.Code)
			}
		})
	}

	c := classifyBedrockError(&smithy.GenericAPIError{Code: "ValidationException", Message: "Input is too long for requested model."})
	if !strings.Contains(c.Message, "添付ファイルが大きすぎる可能性") {
		t.Fatalf("expected input-too-long hint, got %q", c.Message)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestChatHandler(t *testing.T, cfg *config.Config, rt http.RoundTripper) *ChatHandler {
	t.Helper()
	dir := t.TempDir()
	cfg.DatabaseURL = "sqlite:///" + filepath.Join(dir, "test.db")
	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	// TempDir 削除前に DB を閉じる (Windows では開いたままのファイルを削除できない)
	if sqlDB, err := database.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	client := bedrockruntime.New(bedrockruntime.Options{
		Region:           "ap-northeast-1",
		Credentials:      credentials.NewStaticCredentialsProvider("AKIDTEST", "SECRETTEST", ""),
		HTTPClient:       &http.Client{Transport: rt},
		RetryMaxAttempts: 1,
	})
	bundle := &services.AWSClientBundle{BedrockRuntime: client, Config: cfg}
	bedrockSvc := services.NewBedrockService(bundle, services.NewXbergService(cfg), cfg, database)
	return NewChatHandler(bedrockSvc, nil, cfg, database)
}

func chatRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	apiKey := &models.ApiKey{ID: "key-1", Name: "test-key"}
	return req.WithContext(context.WithValue(req.Context(), middleware.APIKeyContextKey, apiKey))
}

func TestRecordUsageLog_RevokedKeyKeepsLogWithoutKeyID(t *testing.T) {
	h := newTestChatHandler(t, &config.Config{}, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected bedrock call")
	}))

	revoked := &models.ApiKey{ID: "revoked-key", Name: "失効済みキー"}
	h.recordUsageLog(revoked, "test.model", false, false, nil, nil, nil, time.Now(), "success", nil)

	var entry models.ChatUsageLog
	deadline := time.Now().Add(5 * time.Second)
	for h.db.First(&entry, "api_key_name = ?", revoked.Name).Error != nil {
		if time.Now().After(deadline) {
			t.Fatal("expected usage log to be recorded for revoked key")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if entry.ApiKeyID != nil {
		t.Errorf("expected api_key_id to be NULL, got %q", *entry.ApiKeyID)
	}
}

func TestChatCompletions_BedrockValidationErrorReturns400AndLogs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "error.log")
	if err := logging.Init(logPath); err != nil {
		t.Fatal(err)
	}
	defer logging.Close()

	cfg := &config.Config{
		XbergPath:               "non_existent_xberg_binary",
		DefaultModelID:          "test.model",
		MaxTotalAttachmentChars: 50000,
		ChatMaxBodyMB:           30,
	}
	h := newTestChatHandler(t, cfg, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header: http.Header{
				"Content-Type":     []string{"application/json"},
				"X-Amzn-Errortype": []string{"ValidationException"},
				"X-Amzn-Requestid": []string{"aws-req-1"},
			},
			Body:    io.NopCloser(strings.NewReader(`{"message":"Input is too long for requested model."}`)),
			Request: r,
		}, nil
	}))

	doc := base64.StdEncoding.EncodeToString([]byte("大きな文書の本文"))
	body := `{"model":"test.model","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"要約して"},` +
		`{"type":"file","file":{"filename":"【資料】.txt","file_data":"data:text/plain;base64,` + doc + `"}}]}]}`
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, chatRequest(t, body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("expected JSON error body, got %q", rec.Body.String())
	}
	if resp["error"]["code"] != "ValidationException" || resp["error"]["type"] != "invalid_request_error" {
		t.Fatalf("unexpected error body: %v", resp)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(raw)
	for _, want := range []string{`"stage":"direct_converse"`, `"aws_error_code":"ValidationException"`, `"aws_request_id":"aws-req-1"`, `"filename":"【資料】.txt"`, `"api_key_id":"key-1"`} {
		if !strings.Contains(logText, want) {
			t.Errorf("expected error log to contain %s, got: %s", want, logText)
		}
	}
	if strings.Contains(logText, doc) || strings.Contains(logText, "大きな文書の本文") {
		t.Errorf("error log must not contain attachment payload or extracted text")
	}
}

func TestChatCompletions_BodyTooLargeReturns413(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "error.log")
	if err := logging.Init(logPath); err != nil {
		t.Fatal(err)
	}
	defer logging.Close()

	cfg := &config.Config{XbergPath: "non_existent_xberg_binary", DefaultModelID: "test.model", ChatMaxBodyMB: 1}
	h := newTestChatHandler(t, cfg, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("Bedrock must not be called for oversized requests")
		return nil, nil
	}))

	big := strings.Repeat("A", 2<<20)
	body := `{"messages":[{"role":"user","content":"` + big + `"}]}`
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, chatRequest(t, body))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "request_too_large") {
		t.Fatalf("expected request_too_large error code, got %q", rec.Body.String())
	}
	raw, _ := os.ReadFile(logPath)
	if !strings.Contains(string(raw), "chat request body too large") {
		t.Fatalf("expected error log entry, got %q", string(raw))
	}
}
