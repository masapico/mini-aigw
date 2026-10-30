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

	data := h.buildTableData(r, user, isAdmin)
	data["User"] = user
	data["IsAdmin"] = isAdmin
	data["CurrentPath"] = "/knowledge"
	data["Bucket"] = h.cfg.KnowledgeS3Bucket
	data["Groups"] = groups
	data["FilterGroups"] = filterGroups

	if err := h.renderer.Render(w, "knowledge.html", data); err != nil {
		log.Printf("Failed to render knowledge.html: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

func (h *KnowledgeHandler) RenderTable(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	isAdmin := user.HasRole("admin")
	data := h.buildTableData(r, user, isAdmin)
	data["User"] = user
	data["IsAdmin"] = isAdmin

	if err := h.renderer.RenderPartial(w, "knowledge_table.html", data); err != nil {
		log.Printf("Failed to render knowledge_table.html: %v", err)
		http.Error(w, "Failed to render table", http.StatusInternalServerError)
	}
}

func (h *KnowledgeHandler) buildTableData(r *http.Request, user *models.User, isAdmin bool) map[string]interface{} {
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
	query.Preload("Uploader").Order("created_at desc").Offset(offset).Limit(pageSize).Find(&documents)

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

	if !canUploadToGroup(user, groupName) {
		http.Error(w, fmt.Sprintf("Forbidden: You cannot upload to group '%s'", groupName), http.StatusForbidden)
		return
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
	prefix := uploadPrefix(groupName)

	// Reject a filename that is already registered in the group. A document waiting for its
	// deletion to be synced does not block: it is moved aside so the new one can take its key.
	existing, err := h.knowledgeService.FindDocumentByKey(r.Context(), services.BuildObjectKey(prefix, header.Filename))
	if err != nil {
		log.Printf("Duplicate check failed: %v", err)
		http.Error(w, "Failed to check existing documents", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		if existing.SyncStatus != services.SyncStatusDeleting {
			w.Header().Set("HX-Retarget", "#upload-alert")
			w.Header().Set("HX-Reswap", "innerHTML")
			h.renderNameCheck(w, existing)
			return
		}
		if err := h.knowledgeService.ReleaseDeletingKey(r.Context(), existing); err != nil {
			log.Printf("Failed to release deleting document key: %v", err)
			http.Error(w, "Failed to replace document pending deletion", http.StatusInternalServerError)
			return
		}
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
		SyncStatus:  services.SyncStatusS3Uploaded,
	}
	doc.SetTags(tags)

	if err := h.db.Create(&doc).Error; err != nil {
		log.Printf("Failed to save knowledge document record: %v", err)
		if rbErr := h.knowledgeService.RemoveUploadedObjects(r.Context(), h.cfg.KnowledgeS3Bucket, uploadRes.S3Key); rbErr != nil {
			log.Printf("Failed to roll back S3 upload %s: %v", uploadRes.S3Key, rbErr)
		}
		http.Error(w, "Failed to register document", http.StatusInternalServerError)
		return
	}

	// Trigger table and pending-change refresh
	w.Header().Set("HX-Trigger", "refreshKnowledgeTable, refreshKnowledgeSyncStatus")
	h.RenderTable(w, r)
}

// HandleCheckName reports whether the filename is already registered in the target group,
// so the upload form can warn before the file is sent.
func (h *KnowledgeHandler) HandleCheckName(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupName := strings.TrimSpace(r.URL.Query().Get("group_name"))
	if groupName == "" {
		groupName = "public"
	}
	filename := strings.TrimSpace(r.URL.Query().Get("filename"))
	if filename == "" || !canUploadToGroup(user, groupName) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return
	}

	existing, err := h.knowledgeService.FindDocumentByKey(r.Context(), services.BuildObjectKey(uploadPrefix(groupName), filename))
	if err != nil {
		log.Printf("Duplicate check failed: %v", err)
		http.Error(w, "Failed to check existing documents", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return
	}
	h.renderNameCheck(w, existing)
}

func (h *KnowledgeHandler) renderNameCheck(w http.ResponseWriter, existing *models.KnowledgeDocument) {
	data := map[string]interface{}{
		"Existing":  existing,
		"Replacing": existing.SyncStatus == services.SyncStatusDeleting,
	}
	if err := h.renderer.RenderPartial(w, "knowledge_name_check.html", data); err != nil {
		log.Printf("Failed to render knowledge_name_check.html: %v", err)
		http.Error(w, "Failed to render name check", http.StatusInternalServerError)
	}
}

func uploadPrefix(groupName string) string {
	if groupName == "public" {
		return "public"
	}
	return fmt.Sprintf("groups/%s", groupName)
}

func canUploadToGroup(user *models.User, groupName string) bool {
	if user.HasRole("admin") {
		return true
	}
	for _, g := range user.GetGroups() {
		if g == groupName {
			return true
		}
	}
	return false
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
	if !doc.CanDelete(user) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	err := h.knowledgeService.DeleteKnowledgeDocument(r.Context(), docID)
	if err != nil {
		log.Printf("Delete failed: %v", err)
		http.Error(w, fmt.Sprintf("Delete failed: %v", err), http.StatusInternalServerError)
		return
	}

	// The row stays as "削除待ち" until the next sync; refresh the table and pending counts.
	w.Header().Set("HX-Trigger", "refreshKnowledgeTable, refreshKnowledgeSyncStatus")
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
		if err := h.renderer.RenderPartial(w, "knowledge_sync_status.html", data); err != nil {
			log.Printf("Failed to render sync status partial: %v", err)
			http.Error(w, "Failed to render sync status", http.StatusInternalServerError)
		}
		return
	}

	if jobStatus != nil && jobStatus.Status == "COMPLETE" {
		_, _ = h.knowledgeService.SyncCompletedDocuments(r.Context(), jobStatus)
		w.Header().Set("HX-Trigger", "refreshKnowledgeTable")
	}

	isSyncing := jobStatus.Status == "IN_PROGRESS" || jobStatus.Status == "STARTING"

	data := map[string]interface{}{
		"IsAdmin":       isAdmin,
		"IsSyncing":     isSyncing,
		"LatestSyncJob": jobStatus,
	}
	h.addPendingChanges(r, data)
	if err := h.renderer.RenderPartial(w, "knowledge_sync_status.html", data); err != nil {
		log.Printf("Failed to render sync status partial: %v", err)
		http.Error(w, "Failed to render sync status", http.StatusInternalServerError)
	}
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
		if err := h.renderer.RenderPartial(w, "knowledge_sync_status.html", data); err != nil {
			log.Printf("Failed to render sync status partial: %v", err)
			http.Error(w, "Failed to render sync status", http.StatusInternalServerError)
		}
		return
	}

	isSyncing := status == "IN_PROGRESS" || status == "STARTING"
	data := map[string]interface{}{
		"IsAdmin":           true,
		"IsSyncing":         isSyncing,
		"JustStartedJobID":  jobID,
		"LatestSyncJob":     &services.IngestionJobStatus{JobID: jobID, Status: status},
	}
	h.addPendingChanges(r, data)
	if err := h.renderer.RenderPartial(w, "knowledge_sync_status.html", data); err != nil {
		log.Printf("Failed to render sync status partial: %v", err)
		http.Error(w, "Failed to render sync status", http.StatusInternalServerError)
	}
}

func (h *KnowledgeHandler) addPendingChanges(r *http.Request, data map[string]interface{}) {
	added, deleted := h.knowledgeService.CountPendingChanges(r.Context())
	data["PendingAdded"] = added
	data["PendingDeleted"] = deleted
	data["HasPendingChanges"] = added+deleted > 0
}

func (h *KnowledgeHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	docID := chi.URLParam(r, "id")
	var doc models.KnowledgeDocument
	// A DELETING document has already been removed from S3.
	if err := h.db.First(&doc, "id = ?", docID).Error; err != nil || doc.SyncStatus == services.SyncStatusDeleting {
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

