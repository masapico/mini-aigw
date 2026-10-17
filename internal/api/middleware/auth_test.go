package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
)

func setupTestDB(t *testing.T) (*config.Config, *models.User, *models.ApiKey) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-mw-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := &config.Config{
		DatabaseURL:       "sqlite:///" + dbPath,
		SessionCookieName: "test_session",
	}

	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	// Create user
	u := &models.User{
		Username: "testuser",
		Roles:    `["admin"]`,
		Groups:   `["group000"]`,
	}
	if err := database.Create(u).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create API Key
	rawKey := "sk-live-test12345678"
	keyHash := auth.HashAPIKey(rawKey)
	apiKey := &models.ApiKey{
		Name:          "Test Key",
		KeyHash:       keyHash,
		Prefix:        "sk-live-te",
		AllowedGroups: `["group000"]`,
	}
	if err := database.Create(apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	return cfg, u, apiKey
}

func TestVerifyAPIKey(t *testing.T) {
	_, _, _ = setupTestDB(t)

	handler := VerifyAPIKey(db.DB)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := GetAPIKeyFromContext(r.Context())
		if k == nil {
			http.Error(w, "no key", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k.Name))
	}))

	// Case 1: Missing Bearer Header
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing auth header, got %d", rec.Code)
	}

	// Case 2: Invalid Bearer token
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid bearer token, got %d", rec.Code)
	}

	// Case 3: Valid Bearer token
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-live-test12345678")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for valid bearer token, got %d", rec.Code)
	}
	if rec.Body.String() != "Test Key" {
		t.Errorf("expected body 'Test Key', got '%s'", rec.Body.String())
	}
}

func TestSessionAndRequireAuth(t *testing.T) {
	cfg, u, _ := setupTestDB(t)

	// Create valid session
	validSessionID := "valid-session-123"
	sess := &models.Session{
		SessionID: validSessionID,
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := db.DB.Create(sess).Error; err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	chain := SessionAuthMiddleware(db.DB, cfg)(RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		currentUser := GetUserFromContext(r.Context())
		if currentUser == nil {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(currentUser.Username))
	})))

	// Case 1: No Cookie
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect for missing session, got %d", rec.Code)
	}

	// Case 2: Valid Cookie
	req = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{
		Name:  cfg.SessionCookieName,
		Value: validSessionID,
	})
	rec = httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for valid session, got %d", rec.Code)
	}
	if rec.Body.String() != "testuser" {
		t.Errorf("expected body 'testuser', got '%s'", rec.Body.String())
	}
}
