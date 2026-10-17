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
