package web

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

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

func newKnowledgeHandlerForTest(t *testing.T) (*KnowledgeHandler, *gorm.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	cfg := &config.Config{DatabaseURL: "sqlite:///" + dbPath, KnowledgeS3Bucket: "test-bucket"}
	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := database.DB(); err == nil {
			sqlDB.Close()
		}
	})
	renderer, err := views.InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}
	ks := services.NewKnowledgeService(nil, database, cfg)
	return NewKnowledgeHandler(ks, database, cfg, renderer), database
}

func TestKnowledgeHandler_HandleCheckName(t *testing.T) {
	handler, database := newKnowledgeHandlerForTest(t)

	user := models.User{ID: "u-1", Username: "sales-user", Roles: `["user"]`, Groups: `["sales"]`}
	database.Create(&user)
	database.Create(&models.KnowledgeDocument{ID: "live", S3Key: "groups/sales/report.pdf", S3Bucket: "test-bucket", Filename: "report.pdf", ContentType: "application/pdf", GroupName: "sales", UploaderID: &user.ID, SyncStatus: "SYNCED"})
	database.Create(&models.KnowledgeDocument{ID: "gone", S3Key: "groups/sales/old.pdf", S3Bucket: "test-bucket", Filename: "old.pdf", ContentType: "application/pdf", GroupName: "sales", SyncStatus: "DELETING"})

	check := func(query string) string {
		req := httptest.NewRequest("GET", "/knowledge/check-name?"+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, &user))
		w := httptest.NewRecorder()
		handler.HandleCheckName(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", query, w.Code)
		}
		return w.Body.String()
	}

	if body := check("group_name=sales&filename=report.pdf"); !strings.Contains(body, "data-name-conflict") {
		t.Errorf("live document with the same name should be reported as a conflict, got %q", body)
	}
	body := check("group_name=sales&filename=old.pdf")
	if strings.Contains(body, "data-name-conflict") || !strings.Contains(body, "削除待ち") {
		t.Errorf("document pending deletion should be reported as replaceable, got %q", body)
	}
	if body := check("group_name=sales&filename=new.pdf"); strings.TrimSpace(body) != "" {
		t.Errorf("unused filename should return empty body, got %q", body)
	}
	// Same filename in a group the user cannot upload to is not disclosed.
	if body := check("group_name=hr&filename=report.pdf"); strings.TrimSpace(body) != "" {
		t.Errorf("forbidden group should return empty body, got %q", body)
	}
}

func TestKnowledgeHandler_HandleUpload_RejectsDuplicateName(t *testing.T) {
	handler, database := newKnowledgeHandlerForTest(t)

	admin := models.User{ID: "admin-1", Username: "admin", Roles: `["admin"]`}
	database.Create(&admin)
	database.Create(&models.KnowledgeDocument{ID: "live", S3Key: "public/manual.pdf", S3Bucket: "test-bucket", Filename: "manual.pdf", ContentType: "application/pdf", GroupName: "public", SyncStatus: "S3_UPLOADED"})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("group_name", "public")
	fw, _ := mw.CreateFormFile("file", "manual.pdf")
	fw.Write([]byte("%PDF-1.4 new content"))
	mw.Close()

	req := httptest.NewRequest("POST", "/knowledge/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, &admin))
	w := httptest.NewRecorder()

	// The knowledge service has no AWS client: reaching S3 would panic, so this also
	// verifies that a rejected upload never touches S3.
	handler.HandleUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with retarget, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("HX-Retarget"); got != "#upload-alert" {
		t.Errorf("expected HX-Retarget #upload-alert, got %q", got)
	}
	if !strings.Contains(w.Body.String(), "data-name-conflict") {
		t.Errorf("expected conflict warning in body")
	}
	var count int64
	database.Model(&models.KnowledgeDocument{}).Count(&count)
	if count != 1 {
		t.Errorf("no document should be registered, got %d records", count)
	}
}
