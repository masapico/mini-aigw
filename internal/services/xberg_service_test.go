package services

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
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

func TestCleanXbergOutput(t *testing.T) {
	raw := "Hello PDF World\r\n--- extraction envelope ---\r\nmime type: application/pdf\r\npages: 1\r\n"
	cleaned := cleanXbergOutput(raw)
	if cleaned != "Hello PDF World" {
		t.Fatalf("expected 'Hello PDF World', got %q", cleaned)
	}
}

func TestXbergService_ResolveBinaryFromDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "xberg-dir-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fakeExe := filepath.Join(tmpDir, "xberg.exe")
	if err := os.WriteFile(fakeExe, []byte("dummy"), 0755); err != nil {
		t.Fatalf("failed to write dummy exe: %v", err)
	}

	svc := NewXbergService(&config.Config{XbergPath: tmpDir})
	resolved, err := svc.resolveXbergBinary()
	if err != nil {
		t.Fatalf("expected binary to resolve from directory, got error: %v", err)
	}
	if filepath.Base(resolved) != "xberg.exe" {
		t.Fatalf("expected resolved binary to end with xberg.exe, got: %s", resolved)
	}
}

func TestXbergService_RealPDFExtractionIfAvailable(t *testing.T) {
	xbergDir := `C:\xberg-cli`
	if _, err := os.Stat(filepath.Join(xbergDir, "xberg.exe")); err != nil {
		t.Skip("C:\\xberg-cli\\xberg.exe not present, skipping real PDF extraction test")
	}

	// Test with directory path to verify both directory resolution and --content-format markdown
	svc := NewXbergService(&config.Config{XbergPath: xbergDir})
	samplePDF := []byte("%PDF-1.4\n" +
		"1 0 obj <</Type /Catalog /Pages 2 0 R>> endobj\n" +
		"2 0 obj <</Type /Pages /Kids [3 0 R] /Count 1>> endobj\n" +
		"3 0 obj <</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources <</Font <</F1 5 0 R>>>> >> endobj\n" +
		"4 0 obj <</Length 44>> stream\n" +
		"BT\n/F1 24 Tf\n100 700 Td\n(Hello PDF World) Tj\nET\n" +
		"endstream\nendobj\n" +
		"5 0 obj <</Type /Font /Subtype /Type1 /BaseFont /Helvetica>> endobj\n" +
		"xref\n0 6\n" +
		"0000000000 65535 f \n" +
		"0000000009 00000 n \n" +
		"0000000058 00000 n \n" +
		"0000000115 00000 n \n" +
		"0000000224 00000 n \n" +
		"0000000318 00000 n \n" +
		"trailer <</Size 6 /Root 1 0 R>>\n" +
		"startxref\n395\n%%EOF")

	extracted := svc.ExtractContentFromBytes(context.Background(), samplePDF, "test.pdf")
	if !strings.Contains(extracted, "Hello PDF World") {
		t.Fatalf("expected extracted PDF text to contain 'Hello PDF World', got: %q", extracted)
	}
	if strings.Contains(extracted, "extraction envelope") {
		t.Fatalf("expected extraction envelope to be stripped, got: %q", extracted)
	}
}

