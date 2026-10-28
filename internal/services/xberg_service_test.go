package services

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestSanitizeExtractedText(t *testing.T) {
	// Text containing null bytes, harmful control characters, but valid newlines and tabs
	raw := "Line 1\x00\x01\x02\r\nLine 2\twith tab\x07 and bell\x7F"
	sanitized := SanitizeExtractedText(raw)
	expected := "Line 1\r\nLine 2\twith tab and bell"
	if sanitized != expected {
		t.Fatalf("expected %q, got %q", expected, sanitized)
	}
}

func TestTruncateExtractedText(t *testing.T) {
	text := "1234567890"
	truncated := TruncateExtractedText(text, 5)
	if !strings.HasPrefix(truncated, "12345\n\n[... Warning:") {
		t.Fatalf("expected truncated text starting with '12345\\n\\n[... Warning:', got: %s", truncated)
	}

	notTruncated := TruncateExtractedText(text, 20)
	if notTruncated != text {
		t.Fatalf("expected unchanged text, got: %s", notTruncated)
	}
}

func TestXbergService_ResolveXbergCliSubdirectory(t *testing.T) {
	// Create temporary directory mimicking project root with xberg-cli/xberg
	tmpDir, err := os.MkdirTemp("", "xberg-cli-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cliDir := filepath.Join(tmpDir, "xberg-cli")
	if err := os.MkdirAll(cliDir, 0755); err != nil {
		t.Fatalf("failed to create cli dir: %v", err)
	}
	fakeBin := filepath.Join(cliDir, "xberg")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatalf("failed to write fake xberg: %v", err)
	}

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	_ = os.Chdir(tmpDir)

	svc := NewXbergService(&config.Config{XbergPath: "xberg"})
	resolved, err := svc.resolveXbergBinary()
	if err != nil {
		t.Fatalf("expected to resolve xberg-cli/xberg, got error: %v", err)
	}
	if !strings.HasSuffix(resolved, filepath.Join("xberg-cli", "xberg")) {
		t.Fatalf("expected resolved path to end with xberg-cli/xberg, got: %s", resolved)
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

// realXbergServiceOrSkip は実 xberg バイナリ (XBERG_PATH またはリポジトリ同梱の xberg-cli/) を使う
// XbergService を返す。見つからない場合はテストをスキップする。
func realXbergServiceOrSkip(t *testing.T) *XbergService {
	t.Helper()
	candidates := []string{os.Getenv("XBERG_PATH"), filepath.Join("..", "..", "xberg-cli"), `C:\xberg-cli`}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err != nil {
			continue
		}
		svc := NewXbergService(&config.Config{XbergPath: c})
		if _, err := svc.resolveXbergBinary(); err == nil {
			return svc
		}
	}
	t.Skip("xberg binary not present (set XBERG_PATH or place xberg-cli/ at repo root), skipping real extraction test")
	return nil
}

func TestXbergService_RealPDFExtractionIfAvailable(t *testing.T) {
	svc := realXbergServiceOrSkip(t)
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


// TestXbergService_RealSamplePDFs は実際にエラーが発生した PDF の抽出を検証する。
// XBERG_TEST_PDF にファイルパスを指定するか、リポジトリ直下の error_sample/*.pdf を配置して実行する。
func TestXbergService_RealSamplePDFs(t *testing.T) {
	var files []string
	if p := os.Getenv("XBERG_TEST_PDF"); p != "" {
		files = append(files, p)
	} else {
		files, _ = filepath.Glob(filepath.Join("..", "..", "error_sample", "*.pdf"))
	}
	if len(files) == 0 {
		t.Skip("no sample PDFs (set XBERG_TEST_PDF or place files in error_sample/)")
	}
	svc := realXbergServiceOrSkip(t)

	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("failed to read %s: %v", f, err)
			}
			b64 := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(data)
			started := time.Now()
			text, info := svc.ExtractWithInfoFromBase64(context.Background(), b64, filepath.Base(f))
			elapsed := time.Since(started)

			t.Logf("bytes=%d chars=%d truncated=%v xberg_exit=%d duration=%v stderr=%q",
				info.DecodedBytes, info.ExtractedChars, info.Truncated, info.XbergExitCode, elapsed, info.XbergStderr)
			if info.FallbackUsed {
				t.Fatalf("xberg extraction failed and fell back: %s", info.XbergError)
			}
			if info.XbergExitCode != 0 {
				t.Fatalf("expected xberg exit 0, got %d", info.XbergExitCode)
			}
			if strings.TrimSpace(text) == "" {
				t.Fatalf("expected non-empty extracted text")
			}
			if strings.Contains(text, "data:image") || strings.Contains(text, "![") {
				t.Fatalf("expected no image content in extracted text")
			}
			if strings.ContainsRune(text, 0) {
				t.Fatalf("expected no NUL bytes in extracted text")
			}
			if elapsed > 30*time.Second {
				t.Fatalf("extraction took too long: %v", elapsed)
			}
		})
	}
}

// writeArgRecordingXberg は受け取った引数をファイルに記録してダミー出力を返す偽 xberg を作成する。
func writeArgRecordingXberg(t *testing.T) (binPath, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary is not supported on Windows")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	binPath = filepath.Join(dir, "xberg")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\necho 'extracted body'\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake xberg: %v", err)
	}
	return binPath, argsFile
}

func TestXbergService_ArgsDisableImagesAndOCRByDefault(t *testing.T) {
	binPath, argsFile := writeArgRecordingXberg(t)
	svc := NewXbergService(&config.Config{XbergPath: binPath})

	text, info := svc.extractFromBytes(context.Background(), []byte("%PDF-1.4 dummy"), "doc.pdf")
	if text != "extracted body" || info.FallbackUsed || info.XbergExitCode != 0 {
		t.Fatalf("unexpected result: text=%q info=%+v", text, info)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake xberg did not record args: %v", err)
	}
	args := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"--extract-images false",
		"--pdf-extract-images false",
		"--ocr false",
		"--content-format markdown",
		"--no-config-discovery",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("expected args to contain %q, got: %s", want, args)
		}
	}
}

func TestXbergService_ArgsEnableOCRWhenConfigured(t *testing.T) {
	binPath, argsFile := writeArgRecordingXberg(t)
	svc := NewXbergService(&config.Config{XbergPath: binPath, XbergOCR: true})

	_ = svc.ExtractContentFromBytes(context.Background(), []byte("%PDF-1.4 dummy"), "doc.pdf")

	raw, _ := os.ReadFile(argsFile)
	args := strings.Join(strings.Fields(string(raw)), " ")
	if !strings.Contains(args, "--ocr true") {
		t.Fatalf("expected --ocr true when XBERG_OCR=true, got: %s", args)
	}
	if !strings.Contains(args, "--pdf-extract-images false") {
		t.Fatalf("expected image extraction to stay disabled, got: %s", args)
	}
}

func TestXbergService_FailureRecordsStderrAndFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary is not supported on Windows")
	}
	binPath := filepath.Join(t.TempDir(), "xberg")
	script := "#!/bin/sh\necho 'pdf parse error: broken xref' >&2\nexit 3\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := NewXbergService(&config.Config{XbergPath: binPath})

	text, info := svc.extractFromBytes(context.Background(), []byte{0x25, 0x50, 0x44, 0x46, 0x2d, 0xff, 0xfe}, "broken.pdf")
	if !info.FallbackUsed || info.XbergExitCode != 3 {
		t.Fatalf("expected fallback with exit code 3, got %+v", info)
	}
	if !strings.Contains(info.XbergStderr, "broken xref") {
		t.Fatalf("expected stderr tail to be recorded, got %q", info.XbergStderr)
	}
	if !strings.Contains(text, "[Binary file: broken.pdf") {
		t.Fatalf("expected binary placeholder, got %q", text)
	}
}

func TestXbergService_TimeoutIsConfigurable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary is not supported on Windows")
	}
	binPath := filepath.Join(t.TempDir(), "xberg")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := NewXbergService(&config.Config{XbergPath: binPath, XbergTimeoutSec: 1})

	started := time.Now()
	_, info := svc.extractFromBytes(context.Background(), []byte("plain text"), "a.txt")
	if time.Since(started) > 4*time.Second {
		t.Fatalf("expected extraction to time out after ~1s, took %v", time.Since(started))
	}
	if !info.FallbackUsed || !strings.Contains(info.XbergError, "timed out") {
		t.Fatalf("expected timeout fallback, got %+v", info)
	}
}

func TestDecodeBase64Payload_Variants(t *testing.T) {
	original := []byte("hello?>world~~")
	std := base64.StdEncoding.EncodeToString(original)
	cases := map[string]string{
		"data url":      "data:application/pdf;base64," + std,
		"with newlines": std[:8] + "\r\n" + std[8:] + "\n",
		"unpadded":      base64.RawStdEncoding.EncodeToString(original),
		"url-safe":      base64.URLEncoding.EncodeToString(original),
	}
	for name, in := range cases {
		got, err := decodeBase64Payload(in)
		if err != nil || string(got) != string(original) {
			t.Errorf("%s: expected %q, got %q (err=%v)", name, original, got, err)
		}
	}
	if _, err := decodeBase64Payload("!!!not base64!!!"); err == nil {
		t.Errorf("expected error for invalid base64")
	}
}
