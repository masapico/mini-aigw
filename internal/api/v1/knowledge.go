package v1

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

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

	isAuthorized := false

	// 1. Web session check
	u := middleware.GetUserFromContext(r.Context())
	if u != nil {
		if u.HasRole("admin") || doc.GroupName == "public" {
			isAuthorized = true
		} else {
			for _, g := range u.GetGroups() {
				if g == doc.GroupName {
					isAuthorized = true
					break
				}
			}
		}
	}

	// 2. API key check (Header or Query parameter)
	if !isAuthorized {
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
				if doc.GroupName == "public" {
					isAuthorized = true
				} else {
					for _, g := range apiKey.GetAllowedGroups() {
						if g == doc.GroupName {
							isAuthorized = true
							break
						}
					}
				}
			}
		}
	}

	// If no auth headers and no session, allow download if public or query has valid token
	if !isAuthorized && doc.GroupName == "public" {
		isAuthorized = true
	}

	if !isAuthorized {
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

