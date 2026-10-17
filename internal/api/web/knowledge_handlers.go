package web

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
	"mini-aigw/internal/services"
	"mini-aigw/internal/views"
)

type KnowledgeHandler struct {
	knowledgeService *services.KnowledgeService
	db               *gorm.DB
	cfg              *config.Config
	renderer         *views.Renderer
}

func NewKnowledgeHandler(
	knowledgeService *services.KnowledgeService,
	db *gorm.DB,
	cfg *config.Config,
	renderer *views.Renderer,
) *KnowledgeHandler {
	return &KnowledgeHandler{
		knowledgeService: knowledgeService,
		db:               db,
		cfg:              cfg,
		renderer:         renderer,
	}
}

func (h *KnowledgeHandler) ShowKnowledge(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	isAdmin := user.HasRole("admin")

	var groups []models.Group
	if isAdmin {
		h.db.Find(&groups)
	} else {
		userGroups := user.GetGroups()
		if len(userGroups) > 0 {
			h.db.Where("name IN ?", userGroups).Find(&groups)
		}
	}

	// Filter groups for dropdown
	var filterGroups []string
	if isAdmin {
		filterGroups = append(filterGroups, "public")
		var allGroups []models.Group
		h.db.Find(&allGroups)
		for _, g := range allGroups {
			filterGroups = append(filterGroups, g.Name)
		}
	} else {
		filterGroups = append(filterGroups, "public")
		filterGroups = append(filterGroups, user.GetGroups()...)
	}

	data := h.buildTableData(r, user, isAdmin, filterGroups)
	data["User"] = user
	data["IsAdmin"] = isAdmin
	data["CurrentPath"] = "/knowledge"
	data["Bucket"] = h.cfg.KnowledgeS3Bucket
	data["Groups"] = groups
	data["FilterGroups"] = filterGroups

	_ = h.renderer.Render(w, "knowledge.html", data)
}

func (h *KnowledgeHandler) RenderTable(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	isAdmin := user.HasRole("admin")
	var filterGroups []string
	if isAdmin {
		filterGroups = append(filterGroups, "public")
		var allGroups []models.Group
		h.db.Find(&allGroups)
		for _, g := range allGroups {
			filterGroups = append(filterGroups, g.Name)
		}
	} else {
		filterGroups = append(filterGroups, "public")
		filterGroups = append(filterGroups, user.GetGroups()...)
	}

	data := h.buildTableData(r, user, isAdmin, filterGroups)
	data["User"] = user
	data["IsAdmin"] = isAdmin

	_ = h.renderer.RenderPartial(w, "knowledge_table.html", data)
}

func (h *KnowledgeHandler) buildTableData(r *http.Request, user *models.User, isAdmin bool, filterGroups []string) map[string]interface{} {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize := 10

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	selectedGroup := strings.TrimSpace(r.URL.Query().Get("group"))
	selectedStatus := strings.TrimSpace(r.URL.Query().Get("status"))

	query := h.db.Model(&models.KnowledgeDocument{})

	// Group permission filter
	if !isAdmin {
		userAllowed := append(user.GetGroups(), "public")
		query = query.Where("group_name IN ?", userAllowed)
	}

	if selectedGroup != "" {
		query = query.Where("group_name = ?", selectedGroup)
	}
	if selectedStatus != "" {
		query = query.Where("sync_status = ?", selectedStatus)
	}
	if q != "" {
		pattern := "%" + q + "%"
		query = query.Where("filename LIKE ? OR tags LIKE ?", pattern, pattern)
	}

	var totalCount int64
	query.Count(&totalCount)

	totalPages := int((totalCount + int64(pageSize) - 1) / int64(pageSize))
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	var documents []models.KnowledgeDocument
	offset := (page - 1) * pageSize
	query.Order("created_at desc").Offset(offset).Limit(pageSize).Find(&documents)

	startIdx := offset + 1
	endIdx := offset + len(documents)
	if totalCount == 0 {
		startIdx = 0
		endIdx = 0
	}

	return map[string]interface{}{
		"Documents":      documents,
		"TotalCount":     totalCount,
		"Page":           page,
		"PageSize":       pageSize,
		"TotalPages":     totalPages,
		"PrevPage":       page - 1,
		"NextPage":       page + 1,
		"StartIdx":       startIdx,
		"EndIdx":         endIdx,
		"Query":          q,
		"SelectedGroup":  selectedGroup,
		"SelectedStatus": selectedStatus,
	}
}

func (h *KnowledgeHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	isAdmin := user.HasRole("admin")

	// Limit upload size (50MB default)
	maxMB := int64(h.cfg.MaxUploadFileSizeMB)
	if maxMB <= 0 {
		maxMB = 50
	}
	if err := r.ParseMultipartForm(maxMB * 1024 * 1024); err != nil {
		http.Error(w, "File size exceeds limit", http.StatusBadRequest)
		return
	}

	groupName := strings.TrimSpace(r.FormValue("group_name"))
	if groupName == "" {
		groupName = "public"
	}

	// Permission check on target group
	if !isAdmin {
		allowed := false
		for _, g := range user.GetGroups() {
			if g == groupName {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, fmt.Sprintf("Forbidden: You cannot upload to group '%s'", groupName), http.StatusForbidden)
			return
		}
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File upload missing", http.StatusBadRequest)
		return
	}
	defer file.Close()

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}

	// Parse tags
	rawTags := r.FormValue("tags")
	var tags []string
	if rawTags != "" {
		_ = json.Unmarshal([]byte(rawTags), &tags)
	}

	docID := uuid.New().String()
	prefix := "public"
	if groupName != "public" {
		prefix = fmt.Sprintf("groups/%s", groupName)
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Upload to S3 (both file and .metadata.json)
	uploadRes, err := h.knowledgeService.UploadDocumentWithMetadata(
		r.Context(),
		h.cfg.KnowledgeS3Bucket,
		prefix,
		header.Filename,
		fileBytes,
		groupName,
		docID,
		contentType,
		tags,
	)
	if err != nil {
		log.Printf("S3 upload failed: %v", err)
		http.Error(w, fmt.Sprintf("S3 upload failed: %v", err), http.StatusInternalServerError)
		return
	}

	doc := models.KnowledgeDocument{
		ID:          docID,
		S3Key:       uploadRes.S3Key,
		S3Bucket:    h.cfg.KnowledgeS3Bucket,
		Filename:    header.Filename,
		FileSize:    int64(len(fileBytes)),
		ContentType: contentType,
		GroupName:   groupName,
		UploaderID:  &user.ID,
		SyncStatus:  "S3_UPLOADED",
	}
	doc.SetTags(tags)

	if err := h.db.Create(&doc).Error; err != nil {
		log.Printf("Failed to save knowledge document record: %v", err)
	}

	// Trigger table refresh
	w.Header().Set("HX-Trigger", "refreshKnowledgeTable")
	h.RenderTable(w, r)
}

func (h *KnowledgeHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	docID := chi.URLParam(r, "id")
	var doc models.KnowledgeDocument
	if err := h.db.First(&doc, "id = ?", docID).Error; err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Permission: admin or uploader
	if !user.HasRole("admin") && (doc.UploaderID == nil || *doc.UploaderID != user.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	err := h.knowledgeService.DeleteKnowledgeDocument(r.Context(), docID)
	if err != nil {
		log.Printf("Delete failed: %v", err)
		http.Error(w, fmt.Sprintf("Delete failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Return empty response with 200 OK so HTMX removes the row (hx-swap="outerHTML")
	w.WriteHeader(http.StatusOK)
}

func (h *KnowledgeHandler) GetSyncStatus(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	isAdmin := user != nil && user.HasRole("admin")

	jobStatus, err := h.knowledgeService.GetLatestIngestionJobStatus(r.Context(), h.cfg.BedrockKBID, h.cfg.BedrockDataSourceID)
	if err != nil {
		data := map[string]interface{}{
			"IsAdmin":   isAdmin,
			"SyncError": fmt.Sprintf("ステータス取得失敗: %v", err),
		}
		_ = h.renderer.RenderPartial(w, "knowledge_sync_status.html", data)
		return
	}

	isSyncing := jobStatus.Status == "IN_PROGRESS" || jobStatus.Status == "STARTING"

	data := map[string]interface{}{
		"IsAdmin":       isAdmin,
		"IsSyncing":     isSyncing,
		"LatestSyncJob": jobStatus,
	}
	_ = h.renderer.RenderPartial(w, "knowledge_sync_status.html", data)
}

func (h *KnowledgeHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil || !user.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	jobID, status, err := h.knowledgeService.StartIngestionJobDetail(r.Context(), h.cfg.BedrockKBID, h.cfg.BedrockDataSourceID)
	if err != nil {
		log.Printf("Start ingestion job error: %v", err)
		data := map[string]interface{}{
			"IsAdmin":   true,
			"SyncError": fmt.Sprintf("同期ジョブ開始失敗: %v", err),
		}
		_ = h.renderer.RenderPartial(w, "knowledge_sync_status.html", data)
		return
	}

	isSyncing := status == "IN_PROGRESS" || status == "STARTING"
	data := map[string]interface{}{
		"IsAdmin":           true,
		"IsSyncing":         isSyncing,
		"JustStartedJobID":  jobID,
		"LatestSyncJob":     &services.IngestionJobStatus{JobID: jobID, Status: status},
	}
	_ = h.renderer.RenderPartial(w, "knowledge_sync_status.html", data)
}

func (h *KnowledgeHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	docID := chi.URLParam(r, "id")
	var doc models.KnowledgeDocument
	if err := h.db.First(&doc, "id = ?", docID).Error; err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Permission check
	if !user.HasRole("admin") && doc.GroupName != "public" {
		allowed := false
		for _, g := range user.GetGroups() {
			if g == doc.GroupName {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	stream, err := h.knowledgeService.GetDocumentStream(r.Context(), doc.S3Bucket, doc.S3Key)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get S3 object: %v", err), http.StatusNotFound)
		return
	}
	defer stream.Body.Close()

	encodedFilename := url.PathEscape(doc.Filename)
	w.Header().Set("Content-Type", stream.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", encodedFilename))
	if stream.ContentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", stream.ContentLength))
	}

	_, _ = io.Copy(w, stream.Body)
}

