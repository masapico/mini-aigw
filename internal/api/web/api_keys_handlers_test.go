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

func setupTestAPIKeysEnv(t *testing.T) (*APIKeysHandler, *chi.Mux, *models.User, *models.User) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-api-keys-test-*")
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
	normalUser := &models.User{
		ID:       "user-uuid-2",
		Username: "viewer",
		Roles:    `["user"]`,
	}
	database.Create(adminUser)
	database.Create(normalUser)

	// Seed groups and models
	grp := models.Group{Name: "sales", DisplayName: "営業部"}
	database.Create(&grp)

	model := models.LLMModel{
		ID:        "amazon.nova-lite-v1:0",
		Name:      "Nova Lite",
		Provider:  "bedrock",
		IsActive:  true,
		IsDefault: true,
	}
	database.Create(&model)

	handler := NewAPIKeysHandler(database, cfg, renderer)

	r := chi.NewRouter()
	r.Get("/api-keys", handler.ShowAPIKeys)
	r.Post("/api-keys/create", handler.CreateKey)
	r.Post("/api-keys/{id}/edit", handler.EditKey)
	r.Delete("/api-keys/{id}", handler.DeleteKey)

	return handler, r, adminUser, normalUser
}

func TestAPIKeysHandler_ShowAPIKeys(t *testing.T) {
	_, r, adminUser, _ := setupTestAPIKeysEnv(t)

	req := httptest.NewRequest("GET", "/api-keys", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	body := w.Body.String()
	if !strings.Contains(body, "AIプロキシ APIキー管理") {
		t.Errorf("expected page title 'AIプロキシ APIキー管理'")
	}
	if !strings.Contains(body, "retrieve_knowledge") {
		t.Errorf("expected builtin prompt containing 'retrieve_knowledge'")
	}
	if !strings.Contains(body, "keyModeBadge") {
		t.Errorf("expected keyModeBadge in page")
	}
}

func TestAPIKeysHandler_CreateAndEditKey(t *testing.T) {
	handler, r, adminUser, normalUser := setupTestAPIKeysEnv(t)

	// 1. Create API key with knowledge group
	form := url.Values{}
	form.Set("name", "初期キー")
	form.Set("model_id", "amazon.nova-lite-v1:0")
	form.Set("system_prompt", "初期プロンプト")
	form.Add("allowed_groups", "public")
	form.Add("allowed_groups", "sales")

	req := httptest.NewRequest("POST", "/api-keys/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "初期キー") {
		t.Errorf("expected rendered partial to contain '初期キー'")
	}
	if !strings.Contains(body, "sk-live-") {
		t.Errorf("expected rendered partial to contain raw key")
	}

	var key models.ApiKey
	if err := handler.db.Where("name = ?", "初期キー").First(&key).Error; err != nil {
		t.Fatalf("key not found in db: %v", err)
	}
	if strings.Contains(key.KeyHash, "sk-live-") || key.KeyHash == "" {
		t.Errorf("expected only a hash to be stored")
	}
	if handler.db.Migrator().HasColumn(&models.ApiKey{}, "raw_key") {
		t.Errorf("raw_key column must not exist (plaintext keys must not be stored)")
	}

	// 2. Edit API key via POST /api-keys/{id}/edit
	newPrompt := "更新後システムプロンプト。丁寧な敬語で回答してください。"
	editForm := url.Values{}
	editForm.Set("name", "更新後キー")
	editForm.Set("model_id", "amazon.nova-lite-v1:0")
	editForm.Set("system_prompt", newPrompt)
	editForm.Add("allowed_groups", "public")

	editReq := httptest.NewRequest("POST", "/api-keys/"+key.ID+"/edit", strings.NewReader(editForm.Encode()))
	editReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	editReq = editReq.WithContext(context.WithValue(editReq.Context(), middleware.UserContextKey, adminUser))
	wEdit := httptest.NewRecorder()

	r.ServeHTTP(wEdit, editReq)

	if wEdit.Code != http.StatusOK {
		t.Fatalf("expected 200 for edit, got %d: %s", wEdit.Code, wEdit.Body.String())
	}
	editBody := wEdit.Body.String()
	if !strings.Contains(editBody, "更新後キー") {
		t.Errorf("expected partial to contain '更新後キー'")
	}
	if strings.Contains(editBody, "sk-live-") && strings.Contains(editBody, "NewRawKey") {
		t.Errorf("edit response must not re-display a secret key")
	}
	if strings.Contains(editBody, `value="sk-live-`) {
		t.Errorf("edit response must not expose full API key")
	}

	var updatedKey models.ApiKey
	handler.db.First(&updatedKey, "id = ?", key.ID)
	if updatedKey.Name != "更新後キー" {
		t.Errorf("expected name '更新後キー', got '%s'", updatedKey.Name)
	}
	if updatedKey.SystemPrompt == nil || *updatedKey.SystemPrompt != newPrompt {
		t.Errorf("expected updated system prompt")
	}
	if updatedKey.KeyHash != key.KeyHash {
		t.Errorf("expected key hash to be preserved untouched")
	}
	if len(updatedKey.GetAllowedGroups()) != 1 || updatedKey.GetAllowedGroups()[0] != "public" {
		t.Errorf("expected allowed groups ['public'], got %v", updatedKey.GetAllowedGroups())
	}

	// 3. Edit as unauthorized user
	unauthReq := httptest.NewRequest("POST", "/api-keys/"+key.ID+"/edit", strings.NewReader(editForm.Encode()))
	unauthReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unauthReq = unauthReq.WithContext(context.WithValue(unauthReq.Context(), middleware.UserContextKey, normalUser))
	wUnauth := httptest.NewRecorder()

	r.ServeHTTP(wUnauth, unauthReq)
	if wUnauth.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for non-admin, got %d", wUnauth.Code)
	}

	// 4. Create key without allowed groups (LLM-only mode)
	llmForm := url.Values{}
	llmForm.Set("name", "LLM専用キー")
	llmReq := httptest.NewRequest("POST", "/api-keys/create", strings.NewReader(llmForm.Encode()))
	llmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	llmReq = llmReq.WithContext(context.WithValue(llmReq.Context(), middleware.UserContextKey, adminUser))
	wLLM := httptest.NewRecorder()

	r.ServeHTTP(wLLM, llmReq)
	if wLLM.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wLLM.Code)
	}

	var llmKey models.ApiKey
	handler.db.Where("name = ?", "LLM専用キー").First(&llmKey)
	if len(llmKey.GetAllowedGroups()) != 0 {
		t.Errorf("expected 0 allowed groups for LLM-only key, got %v", llmKey.GetAllowedGroups())
	}
	if !strings.Contains(wLLM.Body.String(), "なし (LLM専用)") {
		t.Errorf("expected table to display 'なし (LLM専用)' for empty groups")
	}

	// 5. Delete key
	delReq := httptest.NewRequest("DELETE", "/api-keys/"+key.ID, nil)
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), middleware.UserContextKey, adminUser))
	wDel := httptest.NewRecorder()

	r.ServeHTTP(wDel, delReq)
	if wDel.Code != http.StatusOK {
		t.Errorf("expected 200 for delete, got %d", wDel.Code)
	}

	var deletedKey models.ApiKey
	if err := handler.db.First(&deletedKey, "id = ?", key.ID).Error; err == nil {
		t.Errorf("expected key to be deleted from DB")
	}
}

func TestAPIKeysHandler_DeleteKeyWithUsageLogs(t *testing.T) {
	handler, r, adminUser, _ := setupTestAPIKeysEnv(t)

	key := models.ApiKey{Name: "利用済みキー", KeyHash: "hash-used", Prefix: "sk-live-abc..."}
	if err := handler.db.Create(&key).Error; err != nil {
		t.Fatalf("failed to create key: %v", err)
	}
	usageLog := models.ChatUsageLog{ApiKeyID: &key.ID, ApiKeyName: key.Name, ModelID: "amazon.nova-lite-v1:0", TotalTokens: 42}
	if err := handler.db.Create(&usageLog).Error; err != nil {
		t.Fatalf("failed to create usage log: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api-keys/"+key.ID, nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, adminUser))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for delete, got %d: %s", w.Code, w.Body.String())
	}

	var deletedKey models.ApiKey
	if err := handler.db.First(&deletedKey, "id = ?", key.ID).Error; err == nil {
		t.Errorf("expected key to be deleted from DB")
	}

	var kept models.ChatUsageLog
	if err := handler.db.First(&kept, "id = ?", usageLog.ID).Error; err != nil {
		t.Fatalf("expected usage log to be preserved: %v", err)
	}
	if kept.ApiKeyID != nil {
		t.Errorf("expected api_key_id to be NULL, got %q", *kept.ApiKeyID)
	}
	if kept.ApiKeyName != key.Name || kept.TotalTokens != 42 {
		t.Errorf("expected usage stats to be preserved, got name=%q tokens=%d", kept.ApiKeyName, kept.TotalTokens)
	}
}
