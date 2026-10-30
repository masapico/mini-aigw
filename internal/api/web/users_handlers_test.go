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
	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
	"mini-aigw/internal/views"
)

func setupTestUsersEnv(t *testing.T) (*UsersHandler, *chi.Mux, *models.User, *models.User) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-users-test-*")
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
		Username: "testuser",
		Roles:    `["user"]`,
		AuthType: "local",
	}
	database.Create(adminUser)
	database.Create(normalUser)

	grp := models.Group{Name: "dev", DisplayName: "開発部"}
	database.Create(&grp)

	handler := NewUsersHandler(database, renderer)

	r := chi.NewRouter()
	r.Get("/users", handler.ShowUsers)
	r.Post("/users/create", handler.CreateUser)
	r.Post("/users/{id}/edit", handler.EditUser)
	r.Delete("/users/{id}", handler.DeleteUser)
	r.Post("/groups/create", handler.CreateGroup)
	r.Post("/groups/{id}/edit", handler.EditGroup)
	r.Delete("/groups/{id}", handler.DeleteGroup)

	return handler, r, adminUser, normalUser
}

func TestUsersHandler_EditUser(t *testing.T) {
	handler, r, adminUser, normalUser := setupTestUsersEnv(t)

	// 1. Edit normalUser as admin
	form := url.Values{}
	form.Set("display_name", "テスト太郎")
	form.Set("email", "taro@example.com")
	form.Set("password", "newsecretpassword123")
	form.Set("role", "admin")
	form.Add("selected_groups", "dev")

	req := httptest.NewRequest("POST", "/users/"+normalUser.ID+"/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "テスト太郎") {
		t.Errorf("expected rendered table to contain 'テスト太郎'")
	}
	if !strings.Contains(body, "editUserModal-"+normalUser.ID) {
		t.Errorf("expected rendered table to contain edit modal id")
	}

	var updatedUser models.User
	handler.db.First(&updatedUser, "id = ?", normalUser.ID)
	if updatedUser.DisplayName == nil || *updatedUser.DisplayName != "テスト太郎" {
		t.Errorf("expected display name 'テスト太郎'")
	}
	if updatedUser.Email == nil || *updatedUser.Email != "taro@example.com" {
		t.Errorf("expected email 'taro@example.com'")
	}
	if !updatedUser.HasRole("admin") {
		t.Errorf("expected user to have admin role")
	}
	if len(updatedUser.GetGroups()) != 1 || updatedUser.GetGroups()[0] != "dev" {
		t.Errorf("expected user groups ['dev'], got %v", updatedUser.GetGroups())
	}
	if updatedUser.PasswordHash == nil {
		t.Errorf("expected password to be updated")
	} else {
		valid, _ := auth.VerifyPassword("newsecretpassword123", *updatedUser.PasswordHash)
		if !valid {
			t.Errorf("expected password to match hash")
		}
	}

	// 2. Unauthorized attempt by normal user
	unauthReq := httptest.NewRequest("POST", "/users/"+normalUser.ID+"/edit", strings.NewReader(form.Encode()))
	unauthReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unauthReq = unauthReq.WithContext(context.WithValue(unauthReq.Context(), middleware.UserContextKey, &models.User{Roles: `["user"]`}))
	wUnauth := httptest.NewRecorder()

	r.ServeHTTP(wUnauth, unauthReq)
	if wUnauth.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", wUnauth.Code)
	}
}

func TestUsersHandler_EditGroup(t *testing.T) {
	handler, r, adminUser, normalUser := setupTestUsersEnv(t)

	var grp models.Group
	handler.db.Where("name = ?", "dev").First(&grp)

	// 1. Edit group as admin
	form := url.Values{}
	form.Set("display_name", "基盤開発本部")
	form.Set("description", "コアプラットフォーム開発チーム")

	req := httptest.NewRequest("POST", "/groups/"+grp.ID+"/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "基盤開発本部") {
		t.Errorf("expected rendered table to contain '基盤開発本部'")
	}
	if !strings.Contains(body, "コアプラットフォーム開発チーム") {
		t.Errorf("expected rendered table to contain 'コアプラットフォーム開発チーム'")
	}
	if !strings.Contains(body, "editGroupModal-"+grp.ID) {
		t.Errorf("expected rendered table to contain edit modal id")
	}

	var updatedGroup models.Group
	handler.db.First(&updatedGroup, "id = ?", grp.ID)
	if updatedGroup.DisplayName != "基盤開発本部" {
		t.Errorf("expected display name '基盤開発本部', got '%s'", updatedGroup.DisplayName)
	}
	if updatedGroup.Description == nil || *updatedGroup.Description != "コアプラットフォーム開発チーム" {
		t.Errorf("expected description 'コアプラットフォーム開発チーム'")
	}
	if updatedGroup.Name != "dev" {
		t.Errorf("group name must remain 'dev'")
	}

	// 2. Unauthorized attempt by normal user
	unauthReq := httptest.NewRequest("POST", "/groups/"+grp.ID+"/edit", strings.NewReader(form.Encode()))
	unauthReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unauthReq = unauthReq.WithContext(context.WithValue(unauthReq.Context(), middleware.UserContextKey, normalUser))
	wUnauth := httptest.NewRecorder()

	r.ServeHTTP(wUnauth, unauthReq)
	if wUnauth.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", wUnauth.Code)
	}
}

func TestUsersHandler_DeleteUser_WithReferences(t *testing.T) {
	handler, r, adminUser, normalUser := setupTestUsersEnv(t)

	session := models.Session{SessionID: "session-1", UserID: normalUser.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := handler.db.Create(&session).Error; err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	key := models.ApiKey{UserID: &normalUser.ID, Name: "発行済みキー", KeyHash: "hash-issued", Prefix: "sk-live-abc..."}
	if err := handler.db.Create(&key).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}
	doc := models.KnowledgeDocument{ID: "doc-1", S3Key: "groups/dev/a.pdf", S3Bucket: "test-bucket", Filename: "a.pdf", ContentType: "application/pdf", GroupName: "dev", UploaderID: &normalUser.ID, SyncStatus: "SYNCED"}
	if err := handler.db.Create(&doc).Error; err != nil {
		t.Fatalf("failed to create document: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/users/"+normalUser.ID, nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, adminUser))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for delete, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	handler.db.Model(&models.User{}).Where("id = ?", normalUser.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected user to be deleted, count = %d", count)
	}

	// セッションは破棄され、削除済みユーザーのログインが残らないこと
	handler.db.Model(&models.Session{}).Where("user_id = ?", normalUser.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected sessions to be deleted, count = %d", count)
	}

	// 発行済みキーは有効なまま発行者の紐付けだけが外れること
	var keptKey models.ApiKey
	if err := handler.db.First(&keptKey, "id = ?", key.ID).Error; err != nil {
		t.Fatalf("expected api key to be preserved: %v", err)
	}
	if keptKey.UserID != nil {
		t.Errorf("expected api key user_id to be NULL, got %q", *keptKey.UserID)
	}

	// ナレッジ文書は残りアップローダーの紐付けだけが外れること
	var keptDoc models.KnowledgeDocument
	if err := handler.db.First(&keptDoc, "id = ?", doc.ID).Error; err != nil {
		t.Fatalf("expected document to be preserved: %v", err)
	}
	if keptDoc.UploaderID != nil {
		t.Errorf("expected document uploader_id to be NULL, got %q", *keptDoc.UploaderID)
	}
}

func TestUsersHandler_DeleteGroup_CleanupsUserAndApiKey(t *testing.T) {
	handler, r, adminUser, normalUser := setupTestUsersEnv(t)

	var grp models.Group
	handler.db.Where("name = ?", "dev").First(&grp)

	// グループ "dev" に所属するユーザーを用意
	userWithDev := models.User{
		Username: "dev-member",
		AuthType: "local",
	}
	userWithDev.SetGroups([]string{"dev", "sales"})
	if err := handler.db.Create(&userWithDev).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// グループ "dev" を許可する APIKey を用意
	apiKeyWithDev := models.ApiKey{
		Name:    "dev-key",
		Prefix:  "sk-dev",
		KeyHash: "dummyhash",
	}
	apiKeyWithDev.SetAllowedGroups([]string{"public", "dev"})
	if err := handler.db.Create(&apiKeyWithDev).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	// 1. 一般ユーザーは削除権限なし (403)
	unauthReq := httptest.NewRequest("DELETE", "/groups/"+grp.ID, nil)
	unauthReq = unauthReq.WithContext(context.WithValue(unauthReq.Context(), middleware.UserContextKey, normalUser))
	wUnauth := httptest.NewRecorder()
	r.ServeHTTP(wUnauth, unauthReq)
	if wUnauth.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", wUnauth.Code)
	}

	// 2. 管理者が削除実行 (200)
	req := httptest.NewRequest("DELETE", "/groups/"+grp.ID, nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, adminUser))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// グループが削除されていること
	var count int64
	handler.db.Model(&models.Group{}).Where("id = ?", grp.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected group to be deleted, count = %d", count)
	}

	// ユーザーの所属グループから "dev" が消え、"sales" のみ残っていること
	var updatedUser models.User
	handler.db.First(&updatedUser, "id = ?", userWithDev.ID)
	userGroups := updatedUser.GetGroups()
	if len(userGroups) != 1 || userGroups[0] != "sales" {
		t.Errorf("expected user groups ['sales'], got %v", userGroups)
	}

	// APIKey の許可グループから "dev" が消え、"public" のみ残っていること
	var updatedKey models.ApiKey
	handler.db.First(&updatedKey, "id = ?", apiKeyWithDev.ID)
	keyGroups := updatedKey.GetAllowedGroups()
	if len(keyGroups) != 1 || keyGroups[0] != "public" {
		t.Errorf("expected api key groups ['public'], got %v", keyGroups)
	}
}

