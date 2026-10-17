package views

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

func TestInitRenderer_AllTemplatesValid(t *testing.T) {
	r, err := InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	w := httptest.NewRecorder()
	u := &models.User{
		ID:       "test-id",
		Username: "admin",
		Roles:    `["admin"]`,
		Groups:   `["group000"]`,
	}

	data := map[string]interface{}{
		"User":              u,
		"IsAdmin":           true,
		"CurrentPath":       "/",
		"DocCount":          10,
		"KeyCount":          3,
		"ActiveModelsCount": 2,
		"DefaultModelName":  "GPT-OSS 20B",
		"Config":            &config.Config{KnowledgeS3Bucket: "test-bucket", AWSRegion: "ap-northeast-1"},
		"RecentDocs":        []models.KnowledgeDocument{},
		"TotalRequests":     100,
		"RAGRequests":       25,
		"TotalTokens":       50000,
		"RecentUsageLogs":   []models.ChatUsageLog{},
	}

	err = r.Render(w, "dashboard.html", data)
	if err != nil {
		t.Fatalf("failed to render dashboard.html: %v", err)
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("システム概要")) {
		t.Errorf("rendered dashboard does not contain 'システム概要'")
	}
}

func TestRenderer_RenderLogin(t *testing.T) {
	r, err := InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	w := httptest.NewRecorder()
	data := map[string]interface{}{
		"OIDCEnabled": true,
		"Error":       "",
	}

	err = r.Render(w, "login.html", data)
	if err != nil {
		t.Fatalf("failed to render login.html: %v", err)
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("mini-aigw ログイン")) {
		t.Errorf("rendered login does not contain 'mini-aigw ログイン'")
	}
}

func TestRenderer_RenderKnowledgeSyncStatus(t *testing.T) {
	r, err := InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	w := httptest.NewRecorder()
	now := time.Now()
	data := map[string]interface{}{
		"IsAdmin":   true,
		"IsSyncing": false,
		"LatestSyncJob": map[string]interface{}{
			"Status":    "COMPLETE",
			"UpdatedAt": &now,
			"Stats":     map[string]int64{"newDocumentsIndexed": 5, "documentsScanned": 5},
		},
	}

	err = r.RenderPartial(w, "knowledge_sync_status.html", data)
	if err != nil {
		t.Fatalf("failed to render knowledge_sync_status.html: %v", err)
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("同期完了 (COMPLETE)")) {
		t.Errorf("rendered sync status does not contain '同期完了 (COMPLETE)'")
	}
}

func TestRenderer_RenderKnowledge_NonAdminUser(t *testing.T) {
	r, err := InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	regularUser := &models.User{
		ID:       "user-123",
		Username: "regular_user",
		Roles:    `["user"]`,
		Groups:   `["group000"]`,
	}

	adminUploader := &models.User{
		ID:          "admin-uuid",
		Username:    "admin",
		DisplayName: stringPtr("System Admin"),
		Roles:       `["admin"]`,
	}

	adminID := "admin-uuid"
	user123ID := "user-123"
	docs := []models.KnowledgeDocument{
		{
			ID:          "doc-admin",
			Filename:    "guideline.pdf",
			FileSize:    1024 * 1024,
			ContentType: "application/pdf",
			GroupName:   "public",
			UploaderID:  &adminID,
			Uploader:    adminUploader,
			SyncStatus:  "SYNCED",
			CreatedAt:   time.Now().Add(-1 * time.Hour),
		},
		{
			ID:          "doc-user",
			Filename:    "my_report.docx",
			FileSize:    500 * 1024,
			ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			GroupName:   "group000",
			UploaderID:  &user123ID,
			Uploader:    regularUser,
			SyncStatus:  "S3_UPLOADED",
			CreatedAt:   time.Now(),
		},
		{
			ID:          "doc-no-uploader",
			Filename:    "system_imported.txt",
			FileSize:    1234,
			ContentType: "text/plain",
			GroupName:   "public",
			UploaderID:  nil,
			Uploader:    nil,
			SyncStatus:  "SYNCED",
			CreatedAt:   time.Now(),
		},
	}

	data := map[string]interface{}{
		"User":           regularUser,
		"IsAdmin":        false,
		"CurrentPath":    "/knowledge",
		"Bucket":         "test-kb-bucket",
		"Groups":         []models.Group{{Name: "group000", DisplayName: "営業部"}},
		"FilterGroups":   []string{"public", "group000"},
		"Documents":      docs,
		"TotalCount":     int64(len(docs)),
		"Page":           1,
		"PageSize":       10,
		"TotalPages":     1,
		"SelectedGroup":  "",
		"SelectedStatus": "",
		"Query":          "",
	}

	// 1. Full page render test
	w := httptest.NewRecorder()
	err = r.Render(w, "knowledge.html", data)
	if err != nil {
		t.Fatalf("failed to render knowledge.html for non-admin user: %v", err)
	}

	body := w.Body.String()
	// Must contain uploader display name "System Admin"
	if !bytes.Contains([]byte(body), []byte("System Admin")) {
		t.Errorf("expected rendered page to contain uploader display name 'System Admin', got: %s", body)
	}
	// Must contain uploader display name "regular_user"
	if !bytes.Contains([]byte(body), []byte("regular_user")) {
		t.Errorf("expected rendered page to contain uploader username 'regular_user'")
	}
	// Document owned by regular user must have delete button
	if !bytes.Contains([]byte(body), []byte(`hx-delete="/knowledge/doc-user"`)) {
		t.Errorf("expected delete button for owned document doc-user")
	}
	// Document owned by admin must NOT have delete button for regular user
	if bytes.Contains([]byte(body), []byte(`hx-delete="/knowledge/doc-admin"`)) {
		t.Errorf("did not expect delete button for admin document doc-admin for regular user")
	}

	// 2. Partial render test
	wPartial := httptest.NewRecorder()
	err = r.RenderPartial(wPartial, "knowledge_table.html", data)
	if err != nil {
		t.Fatalf("failed to render knowledge_table.html partial for non-admin user: %v", err)
	}
	if !bytes.Contains(wPartial.Body.Bytes(), []byte("System Admin")) {
		t.Errorf("expected rendered partial to contain 'System Admin'")
	}
}

func stringPtr(s string) *string {
	return &s
}


