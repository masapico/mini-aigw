package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mini-aigw/internal/config"
	"mini-aigw/internal/logging"
)

type DocumentExtractor interface {
	ExtractContentFromBytes(ctx context.Context, fileBytes []byte, filename string) string
	ExtractContentFromBase64(ctx context.Context, b64String string, filename string) string
}

type XbergService struct {
	xbergPath string
	ocr       bool
	timeout   time.Duration
}

func NewXbergService(cfg *config.Config) *XbergService {
	path := cfg.XbergPath
	if path == "" {
		path = "xberg"
	}
	timeout := defaultXbergTimeout
	if cfg.XbergTimeoutSec > 0 {
		timeout = time.Duration(cfg.XbergTimeoutSec) * time.Second
	}
	return &XbergService{
		xbergPath: path,
		ocr:       cfg.XbergOCR,
		timeout:   timeout,
	}
}

// resolveXbergBinary finds the viable xberg binary
func (s *XbergService) resolveXbergBinary() (string, error) {
	// 1. Explicit path in config
	if s.xbergPath != "" && s.xbergPath != "xberg" {
		if fi, err := os.Stat(s.xbergPath); err == nil {
			if !fi.IsDir() {
				if abs, err := filepath.Abs(s.xbergPath); err == nil {
					return abs, nil
				}
				return s.xbergPath, nil
			}
			// Directory was specified: search for xberg.exe / xberg inside it
			for _, name := range []string{"xberg.exe", "xberg"} {
				candidate := filepath.Join(s.xbergPath, name)
				if cfi, err := os.Stat(candidate); err == nil && !cfi.IsDir() {
					if abs, err := filepath.Abs(candidate); err == nil {
						return abs, nil
					}
					return candidate, nil
				}
			}
		}
	}

	// 2. Check current working directory for xberg / xberg.exe, and xberg-cli/xberg
	for _, candidate := range []string{
		"./xberg-cli/xberg.exe", "xberg-cli/xberg.exe", "./xberg-cli/xberg", "xberg-cli/xberg",
		"./xberg.exe", "xberg.exe", "./xberg", "xberg",
	} {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			abs, err := filepath.Abs(candidate)
			if err == nil {
				return abs, nil
			}
			return candidate, nil
		}
	}

	// 3. Check system PATH
	path, err := exec.LookPath("xberg")
	if err == nil {
		return path, nil
	}
	pathExe, err := exec.LookPath("xberg.exe")
	if err == nil {
		return pathExe, nil
	}

	return "", fmt.Errorf("xberg binary not found")
}

// DefaultMaxExtractedLength is the maximum rune length allowed for extracted attachment text
// (approx. 30,000 - 50,000 tokens) to prevent exceeding Bedrock context window / token limits.
const DefaultMaxExtractedLength = 100000

// SanitizeExtractedText removes null bytes and harmful control characters (0x00-0x1F except \n, \r, \t),
// removes 0x7F (DEL), and strips invalid UTF-8 runes to avoid Bedrock ValidationException.
func SanitizeExtractedText(raw string) string {
	if raw == "" {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(raw))
	for _, r := range raw {
		if r == 0x00 {
			continue
		}
		if r == '\n' || r == '\r' || r == '\t' {
			sb.WriteRune(r)
			continue
		}
		if r < 0x20 || r == 0x7F || r == utf8.RuneError {
			continue
		}
		sb.WriteRune(r)
	}
	return strings.TrimSpace(sb.String())
}

// TruncateExtractedText safely truncates text to maxLen runes and appends a warning note if truncated.
func TruncateExtractedText(text string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = DefaultMaxExtractedLength
	}
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	return string(runes[:maxLen]) + "\n\n[... Warning: Document content truncated because it exceeded the size limit ...]"
}

// cleanXbergOutput removes the CLI diagnostic footer ("--- extraction envelope ---") from stdout and sanitizes the output
func cleanXbergOutput(raw string) string {
	cleaned := raw
	const marker = "--- extraction envelope ---"
	if idx := strings.Index(raw, "\r\n"+marker); idx != -1 {
		cleaned = raw[:idx]
	} else if idx := strings.Index(raw, "\n"+marker); idx != -1 {
		cleaned = raw[:idx]
	} else if strings.HasPrefix(raw, marker) {
		return ""
	}
	return SanitizeExtractedText(cleaned)
}

// AttachmentInfo は添付ファイル1件の抽出結果メタ情報（エラーログ記録用）。本文は含めない。
type AttachmentInfo struct {
	Filename            string `json:"filename"`
	MimeType            string `json:"mime,omitempty"`
	DecodedBytes        int    `json:"decoded_bytes"`
	DecodeError         string `json:"decode_error,omitempty"`
	XbergUsed           bool   `json:"xberg_used"`
	XbergExitCode       int    `json:"xberg_exit"`
	XbergDurationMs     int64  `json:"xberg_duration_ms"`
	XbergError          string `json:"xberg_error,omitempty"`
	XbergStderr         string `json:"xberg_stderr,omitempty"`
	FallbackUsed        bool   `json:"fallback_used"`
	ExtractedChars      int    `json:"extracted_chars"`
	Truncated           bool   `json:"truncated"`
	OmittedByTotalLimit bool   `json:"omitted_by_total_limit,omitempty"`
}

const (
	// xbergStderrTailBytes はログに残す xberg stderr 末尾のバイト数
	xbergStderrTailBytes = 2048
	// xbergMaxStdoutBytes は xberg stdout の読み取り上限（異常出力によるメモリ枯渇防止）
	xbergMaxStdoutBytes = 32 << 20
	defaultXbergTimeout = 60 * time.Second
)

// ExtractContentFromBytes extracts Markdown text from bytes using xberg CLI with UTF-8 fallback,
// with null-byte/control character sanitization and safe size truncation.
func (s *XbergService) ExtractContentFromBytes(ctx context.Context, fileBytes []byte, filename string) string {
	text, _ := s.extractFromBytes(ctx, fileBytes, filename)
	return text
}

// ExtractContentFromBase64 decodes Base64 (supporting data URLs) and extracts Markdown text
func (s *XbergService) ExtractContentFromBase64(ctx context.Context, b64String string, filename string) string {
	text, _ := s.ExtractWithInfoFromBase64(ctx, b64String, filename)
	return text
}

// ExtractWithInfoFromBase64 は ExtractContentFromBase64 と同じ抽出を行い、計測メタ情報も返す。
func (s *XbergService) ExtractWithInfoFromBase64(ctx context.Context, b64String string, filename string) (string, AttachmentInfo) {
	data, err := decodeBase64Payload(b64String)
	if err != nil {
		log.Printf("Failed to base64 decode attachment %s: %v", filename, err)
		info := AttachmentInfo{Filename: filename, DecodeError: err.Error()}
		logging.Warn(ctx, "attachment base64 decode failed", "attachment", info)
		return fmt.Sprintf("[Attachment decode error: %s]", filename), info
	}
	return s.extractFromBytes(ctx, data, filename)
}

func (s *XbergService) extractFromBytes(ctx context.Context, fileBytes []byte, filename string) (string, AttachmentInfo) {
	info := AttachmentInfo{Filename: filename, DecodedBytes: len(fileBytes)}
	if len(fileBytes) == 0 {
		return "", info
	}

	finish := func(text string) (string, AttachmentInfo) {
		info.Truncated = utf8.RuneCountInString(text) > DefaultMaxExtractedLength
		text = TruncateExtractedText(text, DefaultMaxExtractedLength)
		info.ExtractedChars = utf8.RuneCountInString(text)
		return text, info
	}

	binPath, err := s.resolveXbergBinary()
	if err == nil {
		info.XbergUsed = true
		started := time.Now()
		extracted, run, runErr := s.runXbergExtract(ctx, binPath, fileBytes, filename)
		info.XbergDurationMs = time.Since(started).Milliseconds()
		info.XbergExitCode = run.exitCode
		info.XbergStderr = run.stderrTail
		if runErr == nil && strings.TrimSpace(extracted) != "" {
			return finish(SanitizeExtractedText(extracted))
		}
		if runErr != nil {
			info.XbergError = runErr.Error()
		} else {
			info.XbergError = "empty output"
		}
		log.Printf("xberg extraction failed for %s: %v. Falling back to UTF-8 decoding.", filename, info.XbergError)
	} else {
		info.XbergError = err.Error()
		log.Printf("xberg binary not available (%v). Falling back to UTF-8 decoding for %s.", err, filename)
	}

	info.FallbackUsed = true
	defer func() {
		logging.Warn(ctx, "xberg extraction fell back", "attachment", info)
	}()

	// Fallback: UTF-8 plain text check
	if utf8.Valid(fileBytes) {
		sanitized := SanitizeExtractedText(string(fileBytes))
		if sanitized != "" {
			return finish(sanitized)
		}
	}

	return fmt.Sprintf("[Binary file: %s (%d bytes)]", filename, len(fileBytes)), info
}

type xbergRun struct {
	exitCode   int
	stderrTail string
}

// buildXbergArgs は xberg extract の引数を組み立てる。
// 画像抽出は常に無効化し、OCR は XBERG_OCR=true の場合のみ有効化する。
func (s *XbergService) buildXbergArgs(inputPath string) []string {
	return []string{
		"extract", inputPath,
		"--content-format", "markdown",
		"--extract-images", "false",
		"--pdf-extract-images", "false",
		"--ocr", strconv.FormatBool(s.ocr),
		"--no-config-discovery",
		"--log-level", "error",
	}
}

func (s *XbergService) runXbergExtract(ctx context.Context, binPath string, fileBytes []byte, filename string) (string, xbergRun, error) {
	run := xbergRun{exitCode: -1}
	ext := filepath.Ext(filename)
	if ext == "" && len(fileBytes) >= 5 && string(fileBytes[:5]) == "%PDF-" {
		ext = ".pdf"
	}
	tmpFile, err := os.CreateTemp("", "aigw-doc-*"+ext)
	if err != nil {
		return "", run, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(fileBytes); err != nil {
		_ = tmpFile.Close()
		return "", run, fmt.Errorf("failed to write temp file: %w", err)
	}
	_ = tmpFile.Close()

	execCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, binPath, s.buildXbergArgs(tmpPath)...)
	stdout := &limitedBuffer{limit: xbergMaxStdoutBytes}
	stderr := &tailBuffer{limit: xbergStderrTailBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	run.stderrTail = stderr.String()
	if cmd.ProcessState != nil {
		run.exitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return "", run, fmt.Errorf("xberg timed out after %s: %w", s.timeout, runErr)
		}
		return "", run, fmt.Errorf("xberg exec error: %w", runErr)
	}
	if stdout.overflow {
		log.Printf("xberg output for %s exceeded %d bytes; output was cut", filename, xbergMaxStdoutBytes)
	}

	return cleanXbergOutput(stdout.String()), run, nil
}

// decodeBase64Payload は data URL プレフィックスや改行・空白を除去し、
// 標準 / パディングなし / URL-safe の各 Base64 形式でデコードを試みる。
func decodeBase64Payload(b64String string) ([]byte, error) {
	raw := b64String
	if idx := strings.Index(raw, ";base64,"); idx != -1 {
		raw = raw[idx+len(";base64,"):]
	} else if idx := strings.Index(raw, ","); idx != -1 {
		raw = raw[idx+1:]
	}
	raw = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)

	var firstErr error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		data, err := enc.DecodeString(raw)
		if err == nil {
			return data, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// limitedBuffer は limit バイトまで保持し、それ以降は破棄する io.Writer。
type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remain := b.limit - b.Len(); remain < len(p) {
		b.overflow = true
		if remain > 0 {
			b.Buffer.Write(p[:remain])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// tailBuffer は末尾 limit バイトのみを保持する io.Writer。
type tailBuffer struct {
	buf   []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = b.buf[over:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	return strings.ToValidUTF8(string(b.buf), "")
}
