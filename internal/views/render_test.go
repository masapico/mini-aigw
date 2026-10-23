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
		"DefaultModelName":  "Amazon Nova 2 Lite",
		"Config":            &config.Config{KnowledgeS3Bucket: "test-bucket", AWSRegion: "ap-northeast-1"},
		"RecentDocs":        []models.KnowledgeDocument{},
		"TotalRequests":     100,
		"RAGRequests":       25,
		"TotalTokens":       50000,
		"TotalCostUSD":      0.1234,
		"DailyStatsJSON":    `[{"date":"10/18","requests":10,"tokens":5000,"cost":0.01}]`,
		"RecentUsageLogs":   []models.ChatUsageLog{},
		"ApiKeyStats":       []interface{}{},
		"ModelStats":        []interface{}{},
		"GroupStats":        []interface{}{},
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

func TestRenderer_RenderAPIKeys(t *testing.T) {
	r, err := InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	u := &models.User{
		ID:       "admin-123",
		Username: "admin",
		Roles:    `["admin"]`,
	}

	rawKey := "sk-live-test12345678901234567890"
	modelID := "amazon.nova-lite-v1:0"
	sysPrompt := "テスト用システムプロンプト"
	key := models.ApiKey{
		ID:            "key-uuid-1",
		Name:          "テストキー",
		Prefix:        "sk-live-test12...",
		ModelID:       &modelID,
		SystemPrompt:  &sysPrompt,
		AllowedGroups: `["public","sales"]`,
		CreatedAt:     time.Now(),
	}

	data := map[string]interface{}{
		"User":                   u,
		"IsAdmin":                true,
		"CurrentPath":            "/api-keys",
		"Keys":                   []models.ApiKey{key},
		"AvailableGroups":        []string{"public", "sales", "dev"},
		"AvailableModels":        []models.LLMModel{{ID: "amazon.nova-lite-v1:0", Name: "Nova Lite"}},
		"BuiltinKnowledgePrompt": "【利用ガイドライン】retrieve_knowledge",
		"BuiltinGroundingRules":  "【回答方針・根拠提示ルール】",
	}

	// 1. Full page render
	w := httptest.NewRecorder()
	err = r.Render(w, "api_keys.html", data)
	if err != nil {
		t.Fatalf("failed to render api_keys.html: %v", err)
	}
	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("AIプロキシ APIキー管理")) {
		t.Errorf("expected api_keys.html to contain 'AIプロキシ APIキー管理'")
	}
	if !bytes.Contains([]byte(body), []byte("editKeyModal-key-uuid-1")) {
		t.Errorf("expected api_keys.html to contain edit modal id 'editKeyModal-key-uuid-1'")
	}
	if !bytes.Contains([]byte(body), []byte("keyBuiltinPromptContainer")) {
		t.Errorf("expected api_keys.html to contain 'keyBuiltinPromptContainer'")
	}

	// 2. Partial render
	wPartial := httptest.NewRecorder()
	err = r.RenderPartial(wPartial, "api_keys_section.html", data)
	if err != nil {
		t.Fatalf("failed to render api_keys_section.html: %v", err)
	}
	partialBody := wPartial.Body.String()
	if !bytes.Contains([]byte(partialBody), []byte("editKeyModal-key-uuid-1")) {
		t.Errorf("expected partial to contain 'editKeyModal-key-uuid-1'")
	}
	if bytes.Contains([]byte(partialBody), []byte(rawKey)) {
		t.Errorf("partial must not contain a stored raw key")
	}
	if !bytes.Contains([]byte(partialBody), []byte("editBuiltinPromptCollapse-key-uuid-1")) {
		t.Errorf("expected partial to contain builtin prompt accordion")
	}
}

func TestRenderer_RenderUsersAndGroupsModals(t *testing.T) {
	r, err := InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	dispName := "山田 太郎"
	email := "yamada@example.com"
	testUser := models.User{
		ID:          "user-uuid-1",
		AuthType:    "local",
		Username:    "yamada",
		DisplayName: &dispName,
		Email:       &email,
		Roles:       `["user"]`,
		Groups:      `["sales"]`,
		CreatedAt:   time.Now(),
	}

	desc := "営業部門ナレッジ"
	testGroup := models.Group{
		ID:          "grp-uuid-1",
		Name:        "sales",
		DisplayName: "営業部",
		Description: &desc,
		CreatedAt:   time.Now(),
	}

	userData := map[string]interface{}{
		"Users":       []models.User{testUser},
		"Groups":      []models.Group{testGroup},
		"CurrentUser": &testUser,
	}

	// 1. Users table render
	wUser := httptest.NewRecorder()
	err = r.RenderPartial(wUser, "users_table.html", userData)
	if err != nil {
		t.Fatalf("failed to render users_table.html: %v", err)
	}
	userBody := wUser.Body.String()
	if !bytes.Contains([]byte(userBody), []byte("editUserModal-user-uuid-1")) {
		t.Errorf("expected users_table to contain 'editUserModal-user-uuid-1'")
	}
	if !bytes.Contains([]byte(userBody), []byte("山田 太郎")) {
		t.Errorf("expected users_table to contain display name '山田 太郎'")
	}

	// 2. Groups table render
	groupData := map[string]interface{}{
		"Groups": []models.Group{testGroup},
	}
	wGroup := httptest.NewRecorder()
	err = r.RenderPartial(wGroup, "groups_table.html", groupData)
	if err != nil {
		t.Fatalf("failed to render groups_table.html: %v", err)
	}
	groupBody := wGroup.Body.String()
	if !bytes.Contains([]byte(groupBody), []byte("editGroupModal-grp-uuid-1")) {
		t.Errorf("expected groups_table to contain 'editGroupModal-grp-uuid-1'")
	}
	if !bytes.Contains([]byte(groupBody), []byte("営業部")) {
		t.Errorf("expected groups_table to contain group display name '営業部'")
	}
}

func TestRenderer_ProjectNameDynamic(t *testing.T) {
	r, err := InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	customName := "Custom-AI-Gateway"
	r.SetProjectName(customName)

	w := httptest.NewRecorder()
	data := map[string]interface{}{
		"User": &models.User{Username: "admin"},
	}

	err = r.Render(w, "dashboard.html", data)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("<div class=\"brand-title\">" + customName + "</div>")) {
		t.Errorf("brand-title does not contain custom project name %s, body: %s", customName, body)
	}
	if !bytes.Contains([]byte(body), []byte("<title>ダッシュボード - " + customName + "</title>")) {
		t.Errorf("title does not contain custom project name %s", customName)
	}
}
