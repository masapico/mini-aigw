package services

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"mini-aigw/internal/config"
)

func TestXbergService_FallbackPlainText(t *testing.T) {
	cfg := &config.Config{XbergPath: "non_existent_xberg_binary"}
	svc := NewXbergService(cfg)
	ctx := context.Background()

	content := "Hello World! This is a plain text document."
	extracted := svc.ExtractContentFromBytes(ctx, []byte(content), "sample.txt")

	if extracted != content {
		t.Fatalf("expected '%s', got '%s'", content, extracted)
	}
}

func TestXbergService_ExtractFromBase64(t *testing.T) {
	cfg := &config.Config{XbergPath: "non_existent_xberg_binary"}
	svc := NewXbergService(cfg)
	ctx := context.Background()

	original := "# Markdown Header\nSome body text."
	b64 := base64.StdEncoding.EncodeToString([]byte(original))
	dataURL := "data:text/markdown;base64," + b64

	extracted := svc.ExtractContentFromBase64(ctx, dataURL, "sample.md")
	if extracted != original {
		t.Fatalf("expected '%s', got '%s'", original, extracted)
	}
}

func TestXbergService_BinaryFallback(t *testing.T) {
	cfg := &config.Config{XbergPath: "non_existent_xberg_binary"}
	svc := NewXbergService(cfg)
	ctx := context.Background()

	// Invalid UTF-8 sequence
	invalidBytes := []byte{0xff, 0xfe, 0xfd, 0x00, 0x12}
	extracted := svc.ExtractContentFromBytes(ctx, invalidBytes, "raw.bin")

	if !strings.Contains(extracted, "[Binary file: raw.bin") {
		t.Fatalf("expected binary fallback message, got '%s'", extracted)
	}
}

