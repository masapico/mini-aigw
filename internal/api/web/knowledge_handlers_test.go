package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
	"mini-aigw/internal/services"
	"mini-aigw/internal/views"
)

func TestKnowledgeHandler_ShowKnowledge_NonAdminUser(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-web-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

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

	// Create test users
	adminUser := models.User{
		ID:          "admin-uuid-1",
		Username:    "admin",
		DisplayName: stringPtr("System Admin"),
		Roles:       `["admin"]`,
	}
	normalUser := models.User{
		ID:          "user-uuid-2",
		Username:    "07477",
		DisplayName: stringPtr("masahiro"),
		Roles:       `["user"]`,
		Groups:      `["group000"]`,
	}
	database.Create(&adminUser)
	database.Create(&normalUser)

	// Create a document uploaded by admin in public group
	docAdminID := adminUser.ID
	doc := models.KnowledgeDocument{
		ID:          "doc-uuid-1",
		S3Key:       "public/guidelines.pdf",
		S3Bucket:    "test-bucket",
		Filename:    "自治体におけるAI活用・導入ガイドブック.pdf",
		FileSize:    7 * 1024 * 1024,
		ContentType: "application/pdf",
		GroupName:   "public",
		UploaderID:  &docAdminID,
		SyncStatus:  "S3_UPLOADED",
		CreatedAt:   time.Now().Add(-10 * time.Minute),
	}
	database.Create(&doc)

	ks := services.NewKnowledgeService(nil, database, cfg)
	handler := NewKnowledgeHandler(ks, database, cfg, renderer)

	// Request as normalUser
	req := httptest.NewRequest("GET", "/knowledge", nil)
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, &normalUser)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ShowKnowledge(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	body := w.Body.String()
	// Must contain the document filename
	if !containsString(body, "自治体におけるAI活用・導入ガイドブック.pdf") {
		t.Errorf("expected body to contain document filename")
	}
	// Must contain display name "System Admin", NOT the raw UUID
	if !containsString(body, "System Admin") {
		t.Errorf("expected body to contain 'System Admin' display name")
	}
	if containsString(body, "admin-uuid-1") {
		t.Errorf("body should NOT contain raw uploader UUID 'admin-uuid-1'")
	}
	// Normal user must NOT have delete button for admin document
	if containsString(body, `hx-delete="/knowledge/doc-uuid-1"`) {
		t.Errorf("normal user must not see delete button for admin document")
	}

	// Request RenderTable as normalUser
	reqTable := httptest.NewRequest("GET", "/knowledge/table", nil)
	reqTable = reqTable.WithContext(ctx)
	wTable := httptest.NewRecorder()

	handler.RenderTable(wTable, reqTable)

	respTable := wTable.Result()
	if respTable.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for RenderTable, got %d", respTable.StatusCode)
	}
	bodyTable := wTable.Body.String()
	if !containsString(bodyTable, "System Admin") {
		t.Errorf("expected table partial to contain 'System Admin'")
	}
	if !containsString(bodyTable, "text-nowrap") {
		t.Errorf("expected table partial to contain text-nowrap on sync status td")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && containsSubstr(s, substr)))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func stringPtr(s string) *string {
	return &s
}
