package v1

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
	"mini-aigw/internal/services"
)

type KnowledgeDownloadHandler struct {
	knowledgeService *services.KnowledgeService
	db               *gorm.DB
	cfg              *config.Config
}

func NewKnowledgeDownloadHandler(
	knowledgeService *services.KnowledgeService,
	db *gorm.DB,
	cfg *config.Config,
) *KnowledgeDownloadHandler {
	return &KnowledgeDownloadHandler{
		knowledgeService: knowledgeService,
		db:               db,
		cfg:              cfg,
	}
}

func (h *KnowledgeDownloadHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "doc_id")
	if docID == "" {
		docID = chi.URLParam(r, "file_id")
	}

	var doc models.KnowledgeDocument
	if err := h.db.First(&doc, "id = ?", docID).Error; err != nil {
		http.Error(w, `{"error":{"message":"Document not found","type":"not_found"}}`, http.StatusNotFound)
		return
	}

	if doc.SyncStatus == services.SyncStatusDeleting {
		http.Error(w, `{"error":{"message":"Document not found","type":"not_found"}}`, http.StatusNotFound)
		return
	}

	switch h.authorizeDownload(r, &doc) {
	case downloadAllowed:
	case downloadLinkExpired:
		http.Error(w, `{"error":{"message":"Download link expired","type":"forbidden"}}`, http.StatusForbidden)
		return
	default:
		http.Error(w, `{"error":{"message":"Unauthorized: Access to document is forbidden","type":"forbidden"}}`, http.StatusForbidden)
		return
	}

	stream, err := h.knowledgeService.GetDocumentStream(r.Context(), doc.S3Bucket, doc.S3Key)
	if err != nil {
		log.Printf("Failed to fetch document %s from S3: %v", doc.S3Key, err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":"Failed to fetch document from S3: %v","type":"s3_error"}}`, err), http.StatusNotFound)
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

type downloadAuthResult int

const (
	downloadForbidden downloadAuthResult = iota
	downloadAllowed
	downloadLinkExpired
)

// authorizeDownload は Web セッション・API キー・署名付き URL・public グループの順に
// 文書のダウンロード可否を判定する。
func (h *KnowledgeDownloadHandler) authorizeDownload(r *http.Request, doc *models.KnowledgeDocument) downloadAuthResult {
	if doc.GroupName == "public" {
		return downloadAllowed
	}

	// 1. Web session check
	if u := middleware.GetUserFromContext(r.Context()); u != nil {
		if u.HasRole("admin") {
			return downloadAllowed
		}
		for _, g := range u.GetGroups() {
			if g == doc.GroupName {
				return downloadAllowed
			}
		}
	}

	// 2. API key check (Header or Query parameter)
	rawKey := ""
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		rawKey = strings.TrimPrefix(authHeader, "Bearer ")
	} else if qKey := r.URL.Query().Get("api_key"); qKey != "" {
		rawKey = qKey
	}
	if rawKey != "" {
		keyHash := auth.HashAPIKey(strings.TrimSpace(rawKey))
		var apiKey models.ApiKey
		if err := h.db.First(&apiKey, "key_hash = ?", keyHash).Error; err == nil {
			for _, g := range apiKey.GetAllowedGroups() {
				if g == doc.GroupName {
					return downloadAllowed
				}
			}
		}
	}

	// 3. Signed URL (RAG 回答の根拠資料リンク)
	q := r.URL.Query()
	if q.Get("sig") != "" && h.cfg != nil {
		switch auth.VerifyDownload(h.cfg.SecretKey, doc.ID, q.Get("exp"), q.Get("sig"), time.Now()) {
		case auth.DownloadTokenValid:
			return downloadAllowed
		case auth.DownloadTokenExpired:
			return downloadLinkExpired
		}
	}

	return downloadForbidden
}
