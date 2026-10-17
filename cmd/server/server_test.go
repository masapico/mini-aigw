package main

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
	"mini-aigw/internal/services"
	"mini-aigw/internal/views"
)

func TestFullRouterEndpoints(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-server-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := &config.Config{
		DatabaseURL:       "sqlite:///" + dbPath,
		ProjectName:       "mini-aigw-test",
		SessionCookieName: "test_session",
		AvailableModels:   []string{"openai.gpt-oss-20b-1:0"},
		DefaultModelID:    "openai.gpt-oss-20b-1:0",
	}

	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

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
	oidcSvc, _ := services.NewOIDCService(context.Background(), cfg)

	router := SetupRouter(cfg, database, renderer, bedrockSvc, knowledgeSvc, oidcSvc)

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
}
