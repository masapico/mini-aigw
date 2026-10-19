package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/eval"
	"mini-aigw/internal/services"
	"mini-aigw/internal/version"
	"mini-aigw/internal/views"
)

func TestFullRouterEndpoints(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-server-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := &config.Config{
		DatabaseURL:       "sqlite:///" + dbPath,
		ProjectName:       "mini-aigw-test",
		SessionCookieName: "test_session",
		AvailableModels:   []string{"jp.amazon.nova-2-lite-v1:0"},
		DefaultModelID:    "jp.amazon.nova-2-lite-v1:0",
	}

	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := database.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = os.RemoveAll(tmpDir)
	})

	if err := db.SeedInitialData(database, cfg); err != nil {
		t.Fatalf("SeedInitialData failed: %v", err)
	}

	renderer, err := views.InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	xberg := services.NewXbergService(cfg)
	awsBundle, _ := services.NewAWSClientBundle(context.Background(), cfg)
	bedrockSvc := services.NewBedrockService(awsBundle, xberg, cfg)
	knowledgeSvc := services.NewKnowledgeService(awsBundle, database, cfg)
	ragPipeline := services.NewRAGPipeline(bedrockSvc, knowledgeSvc, cfg)
	evalSvc := eval.NewService(cfg, ragPipeline, bedrockSvc)
	oidcSvc, _ := services.NewOIDCService(context.Background(), cfg)

	router := SetupRouter(cfg, database, renderer, bedrockSvc, knowledgeSvc, oidcSvc, evalSvc)

	// 1. Test /health
	t.Run("GET /health", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		var body map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("failed to parse json: %v", err)
		}
		if body["status"] != "healthy" {
			t.Errorf("expected status 'healthy', got '%v'", body["status"])
		}
		if body["version"] != version.Version {
			t.Errorf("expected version '%s', got '%v'", version.Version, body["version"])
		}
	})

	// 2. Test embedded Bootstrap CSS
	t.Run("GET /static/vendor/bootstrap/css/bootstrap.min.css", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/vendor/bootstrap/css/bootstrap.min.css", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Bootstrap") {
			t.Errorf("expected body to contain 'Bootstrap'")
		}
	})

	// 3. Test embedded HTMX JS
	t.Run("GET /static/vendor/htmx/htmx.min.js", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/vendor/htmx/htmx.min.js", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "htmx") {
			t.Errorf("expected body to contain 'htmx'")
		}
	})

	// 4. Test Favicon
	t.Run("GET /favicon.ico", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "image/svg+xml") {
			t.Errorf("expected image/svg+xml content-type, got '%s'", rec.Header().Get("Content-Type"))
		}
	})

	// 5. Test Login Page (HTML)
	t.Run("GET /login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "mini-aigw") {
			t.Errorf("expected body to contain 'mini-aigw'")
		}
		if !strings.Contains(body, "パスワード") {
			t.Errorf("expected body to contain 'パスワード'")
		}
	})

	// 6. Test Unauthenticated Access to Dashboard
	t.Run("GET /dashboard redirects to /login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected 303 redirect, got %d", rec.Code)
		}
		if rec.Header().Get("Location") != "/login" {
			t.Errorf("expected redirect location '/login', got '%s'", rec.Header().Get("Location"))
		}
	})

	// 7. Test /v1/models (OpenAI-compatible)
	t.Run("GET /v1/models", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		var res struct {
			Object string                   `json:"object"`
			Data   []map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if res.Object != "list" {
			t.Errorf("expected object 'list', got '%s'", res.Object)
		}
		if len(res.Data) == 0 {
			t.Errorf("expected at least 1 model in data")
		}
	})

	// 8. Test /v1/chat/completions Unauthorized without Bearer
	t.Run("POST /v1/chat/completions without API key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 unauthorized, got %d", rec.Code)
		}
	})

	// 9. Test embedded Chart.js asset
	t.Run("GET /static/vendor/chartjs/chart.umd.min.js", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/vendor/chartjs/chart.umd.min.js", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if len(rec.Body.Bytes()) < 10000 {
			t.Errorf("expected Chart.js binary size > 10KB, got %d", len(rec.Body.Bytes()))
		}
	})

	// 10. Test /dashboard/export-csv redirects when unauthorized
	t.Run("GET /dashboard/export-csv unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/export-csv", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected 303 redirect, got %d", rec.Code)
		}
	})

	// 11. Test /zero-hit-queries redirects when unauthorized
	t.Run("GET /zero-hit-queries unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/zero-hit-queries", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected 303 redirect, got %d", rec.Code)
		}
	})

	// 12. Test BasePath subpath routing
	t.Run("GET with BasePath", func(t *testing.T) {
		cfgWithBase := *cfg
		cfgWithBase.BasePath = "/aigw"
		rendererWithBase, err := views.InitRenderer(cfgWithBase.BasePath)
		if err != nil {
			t.Fatalf("InitRenderer with BasePath failed: %v", err)
		}
		routerWithBase := SetupRouter(&cfgWithBase, database, rendererWithBase, bedrockSvc, knowledgeSvc, oidcSvc, evalSvc)

		// Test /aigw/health
		req1 := httptest.NewRequest(http.MethodGet, "/aigw/health", nil)
		rec1 := httptest.NewRecorder()
		routerWithBase.ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusOK {
			t.Errorf("expected 200 for /aigw/health, got %d", rec1.Code)
		}

		// Test /health still works
		req2 := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec2 := httptest.NewRecorder()
		routerWithBase.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Errorf("expected 200 for /health, got %d", rec2.Code)
		}

		// Test /aigw/static/vendor/bootstrap/css/bootstrap.min.css
		req3 := httptest.NewRequest(http.MethodGet, "/aigw/static/vendor/bootstrap/css/bootstrap.min.css", nil)
		rec3 := httptest.NewRecorder()
		routerWithBase.ServeHTTP(rec3, req3)
		if rec3.Code != http.StatusOK {
			t.Errorf("expected 200 for /aigw/static, got %d", rec3.Code)
		}

		// Test /aigw/login renders
		req4 := httptest.NewRequest(http.MethodGet, "/aigw/login", nil)
		rec4 := httptest.NewRecorder()
		routerWithBase.ServeHTTP(rec4, req4)
		if rec4.Code != http.StatusOK {
			t.Errorf("expected 200 for /aigw/login, got %d", rec4.Code)
		}

		// Test OPTIONS /aigw/v1/chat/completions (CORS)
		req5 := httptest.NewRequest(http.MethodOptions, "/aigw/v1/chat/completions", nil)
		rec5 := httptest.NewRecorder()
		routerWithBase.ServeHTTP(rec5, req5)
		if rec5.Code != http.StatusNoContent {
			t.Errorf("expected 204 for OPTIONS /aigw/v1/chat/completions, got %d", rec5.Code)
		}
	})
}
