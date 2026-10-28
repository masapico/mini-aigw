package logging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

func readLogLines(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	var lines []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %q (%v)", line, err)
		}
		lines = append(lines, entry)
	}
	return lines
}

func TestErrorLog_AppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "error.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// 既存内容が保持され追記されることを確認するため、事前に1行書き込んでおく
	if err := os.WriteFile(path, []byte(`{"msg":"existing"}`+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := Init(path); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	ctx := context.WithValue(context.Background(), chiMiddleware.RequestIDKey, "req-123")
	Error(ctx, "chat completion failed", "stage", "direct_converse", "status", 400)
	Warn(context.Background(), "xberg extraction fell back", "filename", "a.pdf")

	lines := readLogLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (1 existing + 2 new), got %d", len(lines))
	}
	if lines[0]["msg"] != "existing" {
		t.Fatalf("expected existing line to be preserved, got %v", lines[0])
	}
	if lines[1]["level"] != "ERROR" || lines[1]["request_id"] != "req-123" || lines[1]["stage"] != "direct_converse" {
		t.Fatalf("unexpected error entry: %v", lines[1])
	}
	if lines[2]["level"] != "WARN" || lines[2]["filename"] != "a.pdf" {
		t.Fatalf("unexpected warn entry: %v", lines[2])
	}
}

func TestRecoverer_LogsPanicWithStack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "error.log")
	if err := Init(path); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	h := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	lines := readLogLines(t, path)
	if len(lines) != 1 || lines[0]["panic"] != "boom" {
		t.Fatalf("expected panic entry, got %v", lines)
	}
	if stack, _ := lines[0]["stack"].(string); !strings.Contains(stack, "goroutine") {
		t.Fatalf("expected stack trace in panic entry, got %q", stack)
	}
}
