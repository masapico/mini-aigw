package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
	"mini-aigw/internal/views"
)

func setupTestDashboardEnv(t *testing.T) (*DashboardHandler, *models.User) {
	tmpDir := t.TempDir()
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

	handler := NewDashboardHandler(database, cfg, renderer)

	adminUser := &models.User{
		ID:       "admin-1",
		Username: "admin",
		Roles:    `["admin"]`,
		Groups:   `["group000"]`,
	}
	if err := database.Create(adminUser).Error; err != nil {
		t.Fatalf("Failed to create admin user: %v", err)
	}

	// Insert sample usage logs
	now := time.Now()
	keyName := "chatbot-key"
	modelID := "jp.amazon.nova-2-lite-v1:0"
	logs := []models.ChatUsageLog{
		{
			ApiKeyName:         keyName,
			ModelID:            modelID,
			TotalTokens:        1500,
			PromptTokens:       1000,
			CompletionTokens:   500,
			EstimatedCost:      0.00018,
			LatencyMs:          320,
			TimeToFirstTokenMs: 120,
			Status:             "success",
			CreatedAt:          now,
		},
		{
			ApiKeyName:         keyName,
			ModelID:            modelID,
			TotalTokens:        2000,
			PromptTokens:       1500,
			CompletionTokens:   500,
			EstimatedCost:      0.00021,
			LatencyMs:          400,
			TimeToFirstTokenMs: 150,
			Status:             "success",
			CreatedAt:          now.Add(-24 * time.Hour),
		},
	}
	for _, l := range logs {
		_ = database.Create(&l)
	}

	return handler, adminUser
}

func TestDashboardHandler_ShowDashboard(t *testing.T) {
	handler, adminUser := setupTestDashboardEnv(t)

	req := httptest.NewRequest("GET", "/dashboard", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ShowDashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "利用推移 & コスト分析") {
		t.Errorf("Dashboard missing daily chart section")
	}
	if !strings.Contains(body, "累計概算コスト") {
		t.Errorf("Dashboard missing estimated cost card")
	}
	if !strings.Contains(body, "CSVエクスポート") {
		t.Errorf("Dashboard missing CSV export button")
	}
	if !strings.Contains(body, "dailyUsageChart") {
		t.Errorf("Dashboard missing dailyUsageChart canvas")
	}
}

func TestDashboardHandler_ExportCSV(t *testing.T) {
	handler, adminUser := setupTestDashboardEnv(t)

	req := httptest.NewRequest("GET", "/dashboard/export-csv", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ExportCSV(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/csv") {
		t.Errorf("Expected Content-Type text/csv, got %s", contentType)
	}

	bodyBytes := rec.Body.Bytes()
	// Check UTF-8 BOM
	if len(bodyBytes) < 3 || bodyBytes[0] != 0xEF || bodyBytes[1] != 0xBB || bodyBytes[2] != 0xBF {
		t.Errorf("Expected UTF-8 BOM in CSV output")
	}

	bodyStr := string(bodyBytes[3:])
	if !strings.Contains(bodyStr, "LogID,Timestamp,ApiKeyName,ModelID") {
		t.Errorf("Expected CSV header in output, got:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "chatbot-key") {
		t.Errorf("Expected sample log in CSV output")
	}
}

