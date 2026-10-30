package services

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"mini-aigw/internal/config"
)

func TestMapFinishReason(t *testing.T) {
	cases := []struct {
		stop    string
		toolUse bool
		want    string
	}{
		{"", false, "stop"},
		{"end_turn", false, "stop"},
		{"stop_sequence", false, "stop"},
		{"max_tokens", false, "length"},
		{"model_context_window_exceeded", false, "length"},
		{"guardrail_intervened", false, "content_filter"},
		{"content_filtered", false, "content_filter"},
		{"tool_use", false, "tool_calls"},
		{"end_turn", true, "tool_calls"},
	}
	for _, c := range cases {
		if got := MapFinishReason(c.stop, c.toolUse); got != c.want {
			t.Errorf("MapFinishReason(%q, %v) = %q, want %q", c.stop, c.toolUse, got, c.want)
		}
	}
}

func textMsg(role bedrockTypes.ConversationRole, text string) bedrockTypes.Message {
	return bedrockTypes.Message{Role: role, Content: []bedrockTypes.ContentBlock{&bedrockTypes.ContentBlockMemberText{Value: text}}}
}

func assertAlternating(t *testing.T, msgs []bedrockTypes.Message) {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	if msgs[0].Role != bedrockTypes.ConversationRoleUser {
		t.Errorf("first message must be user, got %s", msgs[0].Role)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role {
			t.Errorf("messages %d and %d share role %s", i-1, i, msgs[i].Role)
		}
	}
}

func TestNormalizeConversation(t *testing.T) {
	user, asst := bedrockTypes.ConversationRoleUser, bedrockTypes.ConversationRoleAssistant

	t.Run("merges consecutive same role", func(t *testing.T) {
		out := normalizeConversation([]bedrockTypes.Message{textMsg(user, "a"), textMsg(user, "b"), textMsg(asst, "c")})
		assertAlternating(t, out)
		if len(out) != 2 || len(out[0].Content) != 2 {
			t.Fatalf("expected 2 messages with merged user content, got %+v", out)
		}
	})

	t.Run("prepends user when starting with assistant", func(t *testing.T) {
		out := normalizeConversation([]bedrockTypes.Message{textMsg(asst, "hello"), textMsg(user, "q")})
		assertAlternating(t, out)
		if len(out) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(out))
		}
	})

	t.Run("empty conversation gets placeholder", func(t *testing.T) {
		out := normalizeConversation(nil)
		assertAlternating(t, out)
	})

	t.Run("does not mutate input", func(t *testing.T) {
		in := []bedrockTypes.Message{textMsg(user, "a"), textMsg(user, "b")}
		_ = normalizeConversation(in)
		if len(in[0].Content) != 1 {
			t.Errorf("input was mutated: %d blocks", len(in[0].Content))
		}
	})
}

func TestConvertOpenAIMessages_EmptyAndConsecutive(t *testing.T) {
	svc := NewBedrockService(nil, nil, &config.Config{})
	_, msgs := svc.ConvertOpenAIMessagesToBedrock(context.Background(), []map[string]interface{}{
		{"role": "assistant", "content": "いらっしゃいませ"},
		{"role": "user", "content": ""},
		{"role": "user", "content": "質問1"},
		{"role": "user", "content": "質問2"},
	})
	assertAlternating(t, msgs)
	for _, m := range msgs {
		for _, b := range m.Content {
			if tb, ok := b.(*bedrockTypes.ContentBlockMemberText); ok && strings.TrimSpace(tb.Value) == "" {
				t.Errorf("whitespace-only text block must not be sent")
			}
		}
	}
}

func TestBuildRAGAnswerMessages(t *testing.T) {
	prior := []bedrockTypes.Message{textMsg(bedrockTypes.ConversationRoleUser, "規定を教えて")}
	msgs := BuildRAGAnswerMessages(prior, "", "【検索結果（ナレッジ）】...")
	assertAlternating(t, msgs)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.(type) {
			case *bedrockTypes.ContentBlockMemberToolUse, *bedrockTypes.ContentBlockMemberToolResult:
				t.Errorf("RAG answer messages must not contain tool blocks")
			}
		}
	}
	if tb := msgs[1].Content[0].(*bedrockTypes.ContentBlockMemberText); tb.Value != RAGSearchPreamble {
		t.Errorf("expected default preamble, got %q", tb.Value)
	}
	if len(prior) != 1 {
		t.Errorf("prior must not be modified")
	}
}

func newConverseMockService(t *testing.T, body string) *BedrockService {
	t.Helper()
	client := bedrockruntime.NewFromConfig(aws.Config{}, func(o *bedrockruntime.Options) {
		o.Region = "us-east-1"
		o.BaseEndpoint = aws.String("https://bedrock-runtime.test")
		o.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
		o.HTTPClient = &http.Client{Transport: &mockTransport{roundTripFunc: func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}}}
	})
	return NewBedrockService(&AWSClientBundle{BedrockRuntime: client}, nil, &config.Config{DefaultMaxTokens: 100, MaxAllowedTokens: 1000})
}

func TestCallConverse_ReasoningAndLength(t *testing.T) {
	svc := newConverseMockService(t, `{
		"output": {"message": {"role": "assistant", "content": [
			{"reasoningContent": {"reasoningText": {"text": "考え中"}}},
			{"text": "回答"}
		]}},
		"stopReason": "max_tokens",
		"usage": {"inputTokens": 10, "outputTokens": 5, "totalTokens": 15},
		"metrics": {"latencyMs": 1}
	}`)
	resp, err := svc.CallConverse(context.Background(), "test-model", nil,
		[]bedrockTypes.Message{textMsg(bedrockTypes.ConversationRoleUser, "hi")}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.AnswerText(); got != "回答" {
		t.Errorf("content = %q", got)
	}
	if got := resp.ReasoningText(); got != "考え中" {
		t.Errorf("reasoning_content = %q", got)
	}
	if got := resp.FinishReason(); got != "length" {
		t.Errorf("finish_reason = %q, want length", got)
	}
	if resp.StopReason != "max_tokens" {
		t.Errorf("raw stop_reason = %q", resp.StopReason)
	}
}

func TestCallConverse_NoReasoningOmitsField(t *testing.T) {
	svc := newConverseMockService(t, `{
		"output": {"message": {"role": "assistant", "content": [{"text": "ok"}]}},
		"stopReason": "end_turn",
		"usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2},
		"metrics": {"latencyMs": 1}
	}`)
	resp, err := svc.CallConverse(context.Background(), "test-model", nil,
		[]bedrockTypes.Message{textMsg(bedrockTypes.ConversationRoleUser, "hi")}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.Choices[0].Message["reasoning_content"]; ok {
		t.Errorf("reasoning_content should be omitted when empty")
	}
	if resp.FinishReason() != "stop" {
		t.Errorf("finish_reason = %q", resp.FinishReason())
	}
}
