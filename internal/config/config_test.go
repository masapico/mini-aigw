package config

import (
	"path/filepath"
	"testing"
)

func TestLoadConfig_AttachmentAndLoggingDefaults(t *testing.T) {
	t.Setenv("DATA_DIR", "testdata-dir")
	cfg := LoadConfig()

	if cfg.MaxTotalAttachmentChars != 50000 {
		t.Errorf("expected MAX_TOTAL_ATTACHMENT_CHARS default 50000, got %d", cfg.MaxTotalAttachmentChars)
	}
	if cfg.XbergOCR {
		t.Errorf("expected XBERG_OCR default false")
	}
	if cfg.XbergTimeoutSec != 60 || cfg.ChatMaxBodyMB != 30 || cfg.ServerWriteTimeoutSec != 300 {
		t.Errorf("unexpected defaults: xberg_timeout=%d body_mb=%d write_timeout=%d", cfg.XbergTimeoutSec, cfg.ChatMaxBodyMB, cfg.ServerWriteTimeoutSec)
	}
	if want := filepath.Join("testdata-dir", "logs", "error.log"); cfg.ErrorLogFile != want {
		t.Errorf("expected ERROR_LOG_FILE default %q, got %q", want, cfg.ErrorLogFile)
	}
}

func TestLoadConfig_AttachmentLimitFromEnv(t *testing.T) {
	t.Setenv("MAX_TOTAL_ATTACHMENT_CHARS", "12345")
	t.Setenv("XBERG_OCR", "true")
	t.Setenv("ERROR_LOG_FILE", "/var/log/mini-aigw/error.log")
	cfg := LoadConfig()

	if cfg.MaxTotalAttachmentChars != 12345 {
		t.Errorf("expected 12345, got %d", cfg.MaxTotalAttachmentChars)
	}
	if !cfg.XbergOCR {
		t.Errorf("expected XBERG_OCR=true to be applied")
	}
	if cfg.ErrorLogFile != "/var/log/mini-aigw/error.log" {
		t.Errorf("unexpected ERROR_LOG_FILE: %q", cfg.ErrorLogFile)
	}
}
