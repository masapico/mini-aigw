package v1

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
	"mini-aigw/internal/services"
)

func newTestDownloadHandler(t *testing.T) (*KnowledgeDownloadHandler, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		DatabaseURL:         "sqlite:///" + filepath.Join(t.TempDir(), "test.db"),
		SecretKey:           "test-secret",
		DownloadURLTTLHours: 168,
	}
	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := database.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	docs := []models.KnowledgeDocument{
		{ID: "doc-sales", S3Key: "sales/a.pdf", S3Bucket: "b", Filename: "a.pdf", GroupName: "sales", SyncStatus: services.SyncStatusSynced},
		{ID: "doc-hr", S3Key: "hr/b.pdf", S3Bucket: "b", Filename: "b.pdf", GroupName: "hr", SyncStatus: services.SyncStatusSynced},
		{ID: "doc-deleting", S3Key: "sales/c.pdf", S3Bucket: "b", Filename: "c.pdf", GroupName: "sales", SyncStatus: services.SyncStatusDeleting},
	}
	for i := range docs {
		if err := database.Create(&docs[i]).Error; err != nil {
			t.Fatalf("create doc: %v", err)
		}
	}

	ks := services.NewKnowledgeService(nil, database, cfg)
	return NewKnowledgeDownloadHandler(ks, database, cfg), cfg
}

func signedQuery(secret, docID string, exp time.Time) string {
	return fmt.Sprintf("?exp=%d&sig=%s", exp.Unix(), auth.SignDownload(secret, docID, exp.Unix()))
}

func serveDownload(h *KnowledgeDownloadHandler, docID, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/knowledge/files/"+docID+"/download"+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("doc_id", docID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.DownloadFile(rec, req)
	return rec
}

func TestAuthorizeDownload_SignedURL(t *testing.T) {
	h, cfg := newTestDownloadHandler(t)
	doc := &models.KnowledgeDocument{ID: "doc-sales", GroupName: "sales"}
	future := time.Now().Add(time.Hour)

	tests := []struct {
		name  string
		query string
		want  downloadAuthResult
	}{
		{"no credentials", "", downloadForbidden},
		{"valid signature", signedQuery(cfg.SecretKey, doc.ID, future), downloadAllowed},
		{"signature for other document", signedQuery(cfg.SecretKey, "doc-hr", future), downloadForbidden},
		{"wrong secret", signedQuery("other-secret", doc.ID, future), downloadForbidden},
		{"expired", signedQuery(cfg.SecretKey, doc.ID, time.Now().Add(-time.Minute)), downloadLinkExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/knowledge/files/doc-sales/download"+tt.query, nil)
			if got := h.authorizeDownload(req, doc); got != tt.want {
				t.Errorf("authorizeDownload() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDownloadFile_RejectsWithoutValidSignature(t *testing.T) {
	h, cfg := newTestDownloadHandler(t)

	rec := serveDownload(h, "doc-sales", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "forbidden") {
		t.Errorf("no credentials: got %d %s", rec.Code, rec.Body.String())
	}

	// sales 文書の署名を hr 文書に流用しても許可しない
	rec = serveDownload(h, "doc-hr", signedQuery(cfg.SecretKey, "doc-sales", time.Now().Add(time.Hour)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("reused signature: got %d", rec.Code)
	}

	rec = serveDownload(h, "doc-sales", signedQuery(cfg.SecretKey, "doc-sales", time.Now().Add(-time.Minute)))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("expired: got %d %s", rec.Code, rec.Body.String())
	}

	rec = serveDownload(h, "doc-deleting", signedQuery(cfg.SecretKey, "doc-deleting", time.Now().Add(time.Hour)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("deleting document: got %d", rec.Code)
	}
}

func TestResolveDocumentAndDownloadURL_Signed(t *testing.T) {
	h, cfg := newTestDownloadHandler(t)

	_, _, _, downloadURL := h.knowledgeService.ResolveDocumentAndDownloadURL(
		"", map[string]interface{}{"document_id": "doc-sales"}, "https://example.test/aigw/")
	prefix := "https://example.test/aigw/v1/knowledge/files/doc-sales/download?"
	if !strings.HasPrefix(downloadURL, prefix) {
		t.Fatalf("unexpected download URL: %s", downloadURL)
	}

	req := httptest.NewRequest(http.MethodGet, downloadURL, nil)
	q := req.URL.Query()
	if got := auth.VerifyDownload(cfg.SecretKey, "doc-sales", q.Get("exp"), q.Get("sig"), time.Now()); got != auth.DownloadTokenValid {
		t.Errorf("generated URL signature should be valid, got %v", got)
	}
	if got := h.authorizeDownload(req, &models.KnowledgeDocument{ID: "doc-sales", GroupName: "sales"}); got != downloadAllowed {
		t.Errorf("generated URL should authorize download, got %v", got)
	}
}
