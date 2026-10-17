package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"mini-aigw/internal/config"
)

type DocumentExtractor interface {
	ExtractContentFromBytes(ctx context.Context, fileBytes []byte, filename string) string
	ExtractContentFromBase64(ctx context.Context, b64String string, filename string) string
}

type XbergService struct {
	xbergPath string
}

func NewXbergService(cfg *config.Config) *XbergService {
	path := cfg.XbergPath
	if path == "" {
		path = "xberg"
	}
	return &XbergService{
		xbergPath: path,
	}
}

// resolveXbergBinary finds the viable xberg binary
func (s *XbergService) resolveXbergBinary() (string, error) {
	// 1. Explicit path in config
	if s.xbergPath != "" && s.xbergPath != "xberg" {
		if _, err := os.Stat(s.xbergPath); err == nil {
			return s.xbergPath, nil
		}
	}

	// 2. Check current working directory for xberg / xberg.exe
	for _, candidate := range []string{"./xberg.exe", "xberg.exe", "./xberg", "xberg"} {
		if _, err := os.Stat(candidate); err == nil {
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

// ExtractContentFromBytes extracts Markdown text from bytes using xberg CLI with UTF-8 fallback
func (s *XbergService) ExtractContentFromBytes(ctx context.Context, fileBytes []byte, filename string) string {
	if len(fileBytes) == 0 {
		return ""
	}

	binPath, err := s.resolveXbergBinary()
	if err == nil {
		// Run xberg CLI via temporary file
		extracted, runErr := s.runXbergExtract(ctx, binPath, fileBytes, filename)
		if runErr == nil && strings.TrimSpace(extracted) != "" {
			return extracted
		}
		log.Printf("xberg extraction failed for %s: %v. Falling back to UTF-8 decoding.", filename, runErr)
	} else {
		log.Printf("xberg binary not available (%v). Falling back to UTF-8 decoding for %s.", err, filename)
	}

	// Fallback: UTF-8 plain text check
	if utf8.Valid(fileBytes) {
		return string(fileBytes)
	}

	return fmt.Sprintf("[Binary file: %s (%d bytes)]", filename, len(fileBytes))
}

func (s *XbergService) runXbergExtract(ctx context.Context, binPath string, fileBytes []byte, filename string) (string, error) {
	ext := filepath.Ext(filename)
	tmpFile, err := os.CreateTemp("", "aigw-doc-*"+ext)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(fileBytes); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}
	_ = tmpFile.Close()

	// 30 seconds execution timeout
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, binPath, "extract", tmpPath, "--format", "markdown")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("xberg exec error: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

// ExtractContentFromBase64 decodes Base64 (supporting data URLs) and extracts Markdown text
func (s *XbergService) ExtractContentFromBase64(ctx context.Context, b64String string, filename string) string {
	rawB64 := b64String
	// Strip data:*:*;base64, prefix if present
	if idx := strings.Index(rawB64, ";base64,"); idx != -1 {
		rawB64 = rawB64[idx+len(";base64,"):]
	} else if idx := strings.Index(rawB64, ","); idx != -1 {
		rawB64 = rawB64[idx+1:]
	}

	data, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		// Try raw unpadded decoding
		data, err = base64.RawStdEncoding.DecodeString(rawB64)
		if err != nil {
			log.Printf("Failed to base64 decode attachment %s: %v", filename, err)
			return fmt.Sprintf("[Attachment decode error: %s]", filename)
		}
	}

	return s.ExtractContentFromBytes(ctx, data, filename)
}

