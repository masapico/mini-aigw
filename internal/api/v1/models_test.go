package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
)

func TestModelsHandler_ListModels(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-models-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := &config.Config{
		DatabaseURL:     "sqlite:///" + dbPath,
		AvailableModels: []string{"default.model.v1"},
	}

	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	handler := NewModelsHandler(database, cfg)

	// 1. Initial state when DB has no models: fall back to cfg.AvailableModels
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	handler.ListModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var res ModelListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}
	if len(res.Data) != 1 || res.Data[0].ID != "default.model.v1" {
		t.Errorf("expected 1 fallback model 'default.model.v1', got %+v", res.Data)
	}

	// 2. Add an active model in DB
	m := models.LLMModel{
		ID:        "anthropic.claude-3-haiku-20240307-v1:0",
		Name:      "Claude 3 Haiku",
		Provider:  "bedrock",
		IsActive:  true,
		IsDefault: true,
	}
	if err := database.Create(&m).Error; err != nil {
		t.Fatalf("failed to create model: %v", err)
	}

	rec = httptest.NewRecorder()
	handler.ListModels(rec, req)

	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}
	if len(res.Data) != 1 || res.Data[0].ID != m.ID {
		t.Errorf("expected active model from DB, got %+v", res.Data)
	}
}

