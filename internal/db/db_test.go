package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

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

func TestExistingTableMigration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "migration-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := &config.Config{
		DatabaseURL: "sqlite:///" + dbPath,
		Debug:       false,
	}

	type OldLLMModel struct {
		ID               string    `gorm:"primaryKey;size:100" json:"id"`
		Name             string    `gorm:"size:100;not null" json:"name"`
		Description      *string   `gorm:"size:255" json:"description"`
		Provider         string    `gorm:"size:50;not null;default:'bedrock'" json:"provider"`
		InputPricePer1M  float64   `gorm:"column:input_price_per_1m;not null;default:0.0" json:"input_price_per_1m"`
		OutputPricePer1M float64   `gorm:"column:output_price_per_1m;not null;default:0.0" json:"output_price_per_1m"`
		IsActive         bool      `gorm:"not null;default:true" json:"is_active"`
		IsDefault        bool      `gorm:"not null;default:false" json:"is_default"`
		CreatedAt        time.Time `json:"created_at"`
	}

	sqliteDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	if err := sqliteDB.Table("llm_models").AutoMigrate(&OldLLMModel{}); err != nil {
		t.Fatalf("failed to create old table: %v", err)
	}
	oldModel := OldLLMModel{
		ID:       "existing.model.1",
		Name:     "Existing Model",
		Provider: "bedrock",
		IsActive: true,
	}
	if err := sqliteDB.Table("llm_models").Create(&oldModel).Error; err != nil {
		t.Fatalf("failed to insert existing model: %v", err)
	}
	sqlDB, _ := sqliteDB.DB()
	sqlDB.Close()

	// 2. Now call InitDB which runs AutoMigrate(&models.LLMModel{})
	database, err := InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed on existing database: %v", err)
	}

	// 3. Verify existing model has supports_temperature = true
	var m models.LLMModel
	if err := database.First(&m, "id = ?", "existing.model.1").Error; err != nil {
		t.Fatalf("failed to query existing model: %v", err)
	}
	if !m.IsTemperatureSupported() {
		t.Errorf("expected existing model to have SupportsTemperature=true, got %v", m.SupportsTemperature)
	}

	// 4. Verify inserting a new model with SupportsTemperature = false is saved as false
	newModel := models.LLMModel{
		ID:                  "new.model.false",
		Name:                "New Model False",
		SupportsTemperature: models.BoolPtr(false),
		IsActive:            true,
	}
	if err := database.Create(&newModel).Error; err != nil {
		t.Fatalf("failed to insert new model: %v", err)
	}

	var saved models.LLMModel
	if err := database.First(&saved, "id = ?", "new.model.false").Error; err != nil {
		t.Fatalf("failed to find new model: %v", err)
	}
	if saved.IsTemperatureSupported() {
		t.Errorf("expected new model to have SupportsTemperature=false, got true")
	}
}
