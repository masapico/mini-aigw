package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
	"mini-aigw/internal/views"
)

func setupTestModelsEnv(t *testing.T) (*WebModelsHandler, *chi.Mux, *models.User) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-models-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := &config.Config{
		DatabaseURL: "sqlite:///" + dbPath,
		Debug:       false,
	}

	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	renderer, err := views.InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	adminUser := &models.User{
		ID:       "admin-uuid-1",
		Username: "admin",
		Roles:    `["admin"]`,
	}
	database.Create(adminUser)

	handler := NewWebModelsHandler(database, renderer)

	r := chi.NewRouter()
	r.Get("/models", handler.ShowModels)
	r.Post("/models/create", handler.CreateModel)
	r.Post("/models/{id}/edit", handler.EditModel)
	r.Post("/models/{id}/toggle", handler.ToggleModel)

	return handler, r, adminUser
}

func TestWebModelsHandler_CreateAndEditSupportsTemperature(t *testing.T) {
	handler, r, adminUser := setupTestModelsEnv(t)

	// 1. Create model with supports_temperature = false (e.g. Claude Haiku 5.5)
	form := url.Values{}
	form.Set("id", "jp.anthropic.claude-haiku-5-5")
	form.Set("name", "Claude Haiku 5.5")
	form.Set("provider", "bedrock")
	form.Set("input_price_per_1m", "0.80")
	form.Set("output_price_per_1m", "4.00")
	form.Set("supports_temperature_submitted", "1")
	// checkbox unchecked: supports_temperature is omitted or false

	req := httptest.NewRequest("POST", "/models/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	var created models.LLMModel
	if err := handler.db.First(&created, "id = ?", "jp.anthropic.claude-haiku-5-5").Error; err != nil {
		t.Fatalf("failed to find created model: %v", err)
	}
	if created.IsTemperatureSupported() {
		t.Errorf("expected SupportsTemperature to be false for claude-haiku-5-5")
	}

	// 2. Edit model to toggle supports_temperature to true
	editForm := url.Values{}
	editForm.Set("name", "Claude Haiku 5.5 (Updated)")
	editForm.Set("input_price_per_1m", "0.80")
	editForm.Set("output_price_per_1m", "4.00")
	editForm.Set("supports_temperature_submitted", "1")
	editForm.Set("supports_temperature", "true")

	editReq := httptest.NewRequest("POST", "/models/jp.anthropic.claude-haiku-5-5/edit", strings.NewReader(editForm.Encode()))
	editReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	editReq = editReq.WithContext(context.WithValue(editReq.Context(), middleware.UserContextKey, adminUser))

	editRR := httptest.NewRecorder()
	r.ServeHTTP(editRR, editReq)

	if editRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on edit, got %d: %s", editRR.Code, editRR.Body.String())
	}

	var updated models.LLMModel
	if err := handler.db.First(&updated, "id = ?", "jp.anthropic.claude-haiku-5-5").Error; err != nil {
		t.Fatalf("failed to find updated model: %v", err)
	}
	if !updated.IsTemperatureSupported() {
		t.Errorf("expected SupportsTemperature to be true after update")
	}
	if updated.Name != "Claude Haiku 5.5 (Updated)" {
		t.Errorf("expected updated name, got %s", updated.Name)
	}

	// 3. Show models page rendering
	showReq := httptest.NewRequest("GET", "/models", nil)
	showReq = showReq.WithContext(context.WithValue(showReq.Context(), middleware.UserContextKey, adminUser))
	showRR := httptest.NewRecorder()
	r.ServeHTTP(showRR, showReq)

	if showRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on show models, got %d", showRR.Code)
	}
	bodyStr := showRR.Body.String()
	if !strings.Contains(bodyStr, "Claude Haiku 5.5") {
		t.Errorf("expected HTML body to contain model name, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Temp 送信") {
		t.Errorf("expected HTML body to contain Temp 送信 badge")
	}
}

