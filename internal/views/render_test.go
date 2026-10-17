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

