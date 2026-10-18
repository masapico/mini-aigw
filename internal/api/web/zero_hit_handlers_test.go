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
	"time"

	"github.com/go-chi/chi/v5"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
	"mini-aigw/internal/services"
	"mini-aigw/internal/views"
)

func setupTestZeroHitEnv(t *testing.T) (*ZeroHitHandler, *chi.Mux, *models.User, *models.User) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-zerohit-test-*")
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
		ID:       "admin-1",
		Username: "admin",
		Roles:    `["admin"]`,
	}
	normalUser := &models.User{
		ID:       "user-1",
		Username: "sales_user",
		Roles:    `["user"]`,
		Groups:   `["sales"]`,
	}
	database.Create(adminUser)
	database.Create(normalUser)

	zeroHitSvc := services.NewZeroHitService(database)
	handler := NewZeroHitHandler(zeroHitSvc, renderer)

	r := chi.NewRouter()
	r.Get("/zero-hit-queries", handler.ShowZeroHitQueries)
	r.Post("/zero-hit-queries/{id}/status", handler.HandleUpdateStatus)
	r.Delete("/zero-hit-queries/{id}", handler.HandleDelete)
	r.Get("/zero-hit-queries/export-csv", handler.HandleExportCSV)

	// Seed queries
	now := time.Now()
	database.Create(&models.ZeroHitQuery{
		ID:                  "zh-1",
		Query:               "営業特別値引き承認フロー",
		NormalizedQueryHash: models.ComputeQueryHash("sales", "営業特別値引き承認フロー"),
		GroupName:           "sales",
		HitCount:            3,
		MaxScore:            0.22,
		Reason:              "スコア不足",
		Status:              "unresolved",
		FirstOccurredAt:     now,
		LastOccurredAt:      now,
		LastApiKeyName:      "sales-key",
	})
	database.Create(&models.ZeroHitQuery{
		ID:                  "zh-2",
		Query:               "人事健康診断日程について",
		NormalizedQueryHash: models.ComputeQueryHash("hr", "人事健康診断日程について"),
		GroupName:           "hr",
		HitCount:            5,
		MaxScore:            0.0,
		Reason:              "0件ヒット",
		Status:              "unresolved",
		FirstOccurredAt:     now,
		LastOccurredAt:      now,
		LastApiKeyName:      "hr-key",
	})

	return handler, r, adminUser, normalUser
}

func TestZeroHitHandler_ShowZeroHitQueries(t *testing.T) {
	_, r, adminUser, normalUser := setupTestZeroHitEnv(t)

	// 1. Admin は全クエリ (sales, hr) を閲覧可能
	req := httptest.NewRequest(http.MethodGet, "/zero-hit-queries", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "営業特別値引き承認フロー") || !strings.Contains(body, "人事健康診断日程について") {
		t.Errorf("admin should see both queries, got:\n%s", body)
	}

	// 2. 一般ユーザー (sales) は sales のみ閲覧可能
	req2 := httptest.NewRequest(http.MethodGet, "/zero-hit-queries", nil)
	ctx2 := context.WithValue(req2.Context(), middleware.UserContextKey, normalUser)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2.WithContext(ctx2))

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}
	body2 := rec2.Body.String()
	if !strings.Contains(body2, "営業特別値引き承認フロー") {
		t.Errorf("sales user should see sales query")
	}
	if strings.Contains(body2, "人事健康診断日程について") {
		t.Errorf("sales user should NOT see hr query")
	}
}

func TestZeroHitHandler_UpdateStatus(t *testing.T) {
	_, r, adminUser, _ := setupTestZeroHitEnv(t)

	// 1. 解決済みに更新
	form := url.Values{}
	form.Set("status", "resolved")
	form.Set("current_filter", "unresolved")

	req := httptest.NewRequest(http.MethodPost, "/zero-hit-queries/zh-1/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// 2. 対象外に更新
	form2 := url.Values{}
	form2.Set("status", "ignored")
	form2.Set("current_filter", "unresolved")

	req2 := httptest.NewRequest(http.MethodPost, "/zero-hit-queries/zh-2/status", strings.NewReader(form2.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx2 := context.WithValue(req2.Context(), middleware.UserContextKey, adminUser)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2.WithContext(ctx2))

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}
}

func TestZeroHitHandler_DeleteAndExportCSV(t *testing.T) {
	_, r, adminUser, _ := setupTestZeroHitEnv(t)

	// 1. 削除
	req := httptest.NewRequest(http.MethodDelete, "/zero-hit-queries/zh-1", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d", rec.Code)
	}

	// 2. CSVエクスポート
	reqCSV := httptest.NewRequest(http.MethodGet, "/zero-hit-queries/export-csv", nil)
	ctxCSV := context.WithValue(reqCSV.Context(), middleware.UserContextKey, adminUser)
	recCSV := httptest.NewRecorder()
	r.ServeHTTP(recCSV, reqCSV.WithContext(ctxCSV))

	if recCSV.Code != http.StatusOK {
		t.Fatalf("expected 200 on csv export, got %d", recCSV.Code)
	}
	csvBody := recCSV.Body.String()
	if !strings.Contains(csvBody, "質問クエリ") || !strings.Contains(csvBody, "人事健康診断日程について") {
		t.Errorf("csv should contain header and remaining query, got:\n%s", csvBody)
	}
}

