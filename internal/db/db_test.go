package db

import (
	"os"
	"path/filepath"
	"testing"

	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

func TestInitDBAndSeed(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := &config.Config{
		DatabaseURL: "sqlite:///" + dbPath,
		Debug:       false,
	}

	database, err := InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	// Verify PRAGMA journal_mode
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("database.DB() failed: %v", err)
	}
	var journalMode string
	row := sqlDB.QueryRow("PRAGMA journal_mode;")
	if err := row.Scan(&journalMode); err != nil {
		t.Fatalf("failed to scan journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode 'wal', got '%s'", journalMode)
	}

	// Test SeedInitialData
	if err := SeedInitialData(database, cfg); err != nil {
		t.Fatalf("SeedInitialData failed: %v", err)
	}

	// Verify Admin user was seeded
	var adminUser models.User
	if err := database.Where("username = ?", "admin").First(&adminUser).Error; err != nil {
		t.Fatalf("failed to find seeded admin user: %v", err)
	}
	if !adminUser.HasRole("admin") {
		t.Errorf("expected seeded user to have admin role")
	}

	// Verify Groups
	var groups []models.Group
	if err := database.Find(&groups).Error; err != nil {
		t.Fatalf("failed to query groups: %v", err)
	}
	if len(groups) < 2 {
		t.Errorf("expected at least 2 groups, got %d", len(groups))
	}

	// Verify Models
	var llmModels []models.LLMModel
	if err := database.Find(&llmModels).Error; err != nil {
		t.Fatalf("failed to query llm models: %v", err)
	}
	if len(llmModels) == 0 {
		t.Errorf("expected seeded llm models, got 0")
	}

	// Calling SeedInitialData again should be idempotent
	if err := SeedInitialData(database, cfg); err != nil {
		t.Fatalf("second SeedInitialData call failed: %v", err)
	}
}
