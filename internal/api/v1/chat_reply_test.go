package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"mini-aigw/internal/config"
	"mini-aigw/internal/logging"
	"mini-aigw/internal/services"
)

func TestExtractToolQueries_MultipleAndDedup(t *testing.T) {
	toolUse := func(id string, input interface{}) bedrockTypes.ContentBlock {
		return &bedrockTypes.ContentBlockMemberToolUse{Value: bedrockTypes.ToolUseBlock{
			ToolUseId: &id, Input: document.NewLazyDocument(input),
		}}
	}
	resp := &services.ChatCompletionResponse{RawOutputMsg: &bedrockTypes.ConverseOutputMemberMessage{
		Value: bedrockTypes.Message{Content: []bedrockTypes.ContentBlock{
			&bedrockTypes.ContentBlockMemberText{Value: "検索します"},
			toolUse("1", map[string]interface{}{"query": "経費精算の手順について"}),
			toolUse("2", map[string]interface{}{"query": "経費精算の手順について"}),
			toolUse("3", map[string]interface{}{"query": "出張旅費の上限について"}),
			toolUse("4", map[string]interface{}{}),
		}},
	}}

	queries, invoked := extractToolQueries(resp)
	if !invoked {
		t.Fatal("expected toolInvoked=true")
	}
	if len(queries) != 2 || queries[0] != "経費精算の手順について" || queries[1] != "出張旅費の上限について" {
		t.Fatalf("unexpected queries: %v", queries)
	}

	if _, invoked := extractToolQueries(&services.ChatCompletionResponse{}); invoked {
		t.Error("expected toolInvoked=false without output message")
	}
}

func TestChatCompletions_EmptyReplyIsLoggedWithDiagnostics(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "error.log")
	if err := logging.Init(logPath); err != nil {
		t.Fatal(err)
	}
	defer logging.Close()

	cfg := &config.Config{XbergPath: "non_existent_xberg_binary", DefaultModelID: "test.model", DefaultMaxTokens: 100, MaxAllowedTokens: 1000}
	h := newTestChatHandler(t, cfg, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"output": {"message": {"role": "assistant", "content": [
					{"reasoningContent": {"reasoningText": {"text": "思考の内容"}}}
				]}},
				"stopReason": "max_tokens",
				"usage": {"inputTokens": 12, "outputTokens": 100, "totalTokens": 112},
				"metrics": {"latencyMs": 1}
			}`)),
			Request: r,
		}, nil
	}))

	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, chatRequest(t, `{"messages":[{"role":"user","content":"質問"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp services.ChatCompletionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason() != "length" {
		t.Errorf("finish_reason = %q, want length", resp.FinishReason())
	}
	if resp.ReasoningText() != "思考の内容" {
		t.Errorf("reasoning_content = %q", resp.ReasoningText())
	}

	raw, _ := os.ReadFile(logPath)
	logText := string(raw)
	for _, want := range []string{`"msg":"chat reply empty"`, `"stage":"direct"`, `"finish_reason":"length"`, `"stop_reason":"max_tokens"`, `"reasoning_chars":5`, `"completion_tokens":100`} {
		if !strings.Contains(logText, want) {
			t.Errorf("expected log to contain %s, got: %s", want, logText)
		}
	}
	if strings.Contains(logText, "思考の内容") {
		t.Error("log must not contain reasoning text")
	}
}
