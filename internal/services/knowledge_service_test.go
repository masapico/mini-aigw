package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/models"
)

func TestKnowledgeService_SyncCompletedDocuments(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-sync-test-*")
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

	ks := NewKnowledgeService(nil, database, cfg)
	ctx := context.Background()

	baseTime := time.Date(2026, 10, 18, 3, 30, 0, 0, time.UTC)
	timeBefore := baseTime.Add(-10 * time.Minute)
	timeRightBefore := baseTime.Add(-10 * time.Second)
	timeAfter := baseTime.Add(10 * time.Minute)

	doc1 := models.KnowledgeDocument{
		ID:          "doc-1-synced",
		S3Key:       "public/doc1.pdf",
		S3Bucket:    "test-bucket",
		Filename:    "doc1.pdf",
		FileSize:    100,
		ContentType: "application/pdf",
		GroupName:   "public",
		SyncStatus:  "S3_UPLOADED",
		CreatedAt:   timeBefore,
	}
	doc2 := models.KnowledgeDocument{
		ID:          "doc-2-synced",
		S3Key:       "public/doc2.pdf",
		S3Bucket:    "test-bucket",
		Filename:    "doc2.pdf",
		FileSize:    200,
		ContentType: "application/pdf",
		GroupName:   "public",
		SyncStatus:  "S3_UPLOADED",
		CreatedAt:   timeRightBefore,
	}
	doc3 := models.KnowledgeDocument{
		ID:          "doc-3-pending",
		S3Key:       "public/doc3.pdf",
		S3Bucket:    "test-bucket",
		Filename:    "doc3.pdf",
		FileSize:    300,
		ContentType: "application/pdf",
		GroupName:   "public",
		SyncStatus:  "S3_UPLOADED",
		CreatedAt:   timeAfter,
	}

	if err := database.Create(&doc1).Error; err != nil {
		t.Fatalf("failed to insert doc1: %v", err)
	}
	if err := database.Create(&doc2).Error; err != nil {
		t.Fatalf("failed to insert doc2: %v", err)
	}
	if err := database.Create(&doc3).Error; err != nil {
		t.Fatalf("failed to insert doc3: %v", err)
	}

	// 1. In-progress job should NOT update anything
	inProgressJob := &IngestionJobStatus{
		JobID:     "job-in-progress",
		Status:    "IN_PROGRESS",
		StartedAt: &baseTime,
	}
	updated, err := ks.SyncCompletedDocuments(ctx, inProgressJob)
	if err != nil {
		t.Fatalf("SyncCompletedDocuments error: %v", err)
	}
	if updated != 0 {
		t.Errorf("expected 0 updated documents for IN_PROGRESS job, got %d", updated)
	}

	// 2. COMPLETE job should update doc1 and doc2, but not doc3
	completeJob := &IngestionJobStatus{
		JobID:     "job-complete",
		Status:    "COMPLETE",
		StartedAt: &baseTime,
		UpdatedAt: &baseTime,
	}
	updated, err = ks.SyncCompletedDocuments(ctx, completeJob)
	if err != nil {
		t.Fatalf("SyncCompletedDocuments error: %v", err)
	}
	if updated != 2 {
		t.Errorf("expected 2 updated documents for COMPLETE job, got %d", updated)
	}

	var d1, d2, d3 models.KnowledgeDocument
	database.First(&d1, "id = ?", "doc-1-synced")
	database.First(&d2, "id = ?", "doc-2-synced")
	database.First(&d3, "id = ?", "doc-3-pending")

	if d1.SyncStatus != "SYNCED" {
		t.Errorf("expected doc1 status to be SYNCED, got %s", d1.SyncStatus)
	}
	if d2.SyncStatus != "SYNCED" {
		t.Errorf("expected doc2 status to be SYNCED, got %s", d2.SyncStatus)
	}
	if d3.SyncStatus != "S3_UPLOADED" {
		t.Errorf("expected doc3 status to remain S3_UPLOADED, got %s", d3.SyncStatus)
	}
}


func newKnowledgeTestService(t *testing.T) (*KnowledgeService, *gorm.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	cfg := &config.Config{DatabaseURL: "sqlite:///" + dbPath}
	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := database.DB(); err == nil {
			sqlDB.Close()
		}
	})
	return NewKnowledgeService(nil, database, cfg), database
}

func TestKnowledgeService_SyncCompletedDocuments_FinalizesDeletions(t *testing.T) {
	ks, database := newKnowledgeTestService(t)
	ctx := context.Background()

	jobStart := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	docs := []models.KnowledgeDocument{
		{ID: "del-before", S3Key: "public/a.pdf", S3Bucket: "b", Filename: "a.pdf", ContentType: "pdf", GroupName: "public", SyncStatus: SyncStatusDeleting},
		{ID: "del-after", S3Key: "public/b.pdf", S3Bucket: "b", Filename: "b.pdf", ContentType: "pdf", GroupName: "public", SyncStatus: SyncStatusDeleting},
	}
	for i := range docs {
		if err := database.Create(&docs[i]).Error; err != nil {
			t.Fatalf("insert %s: %v", docs[i].ID, err)
		}
	}
	// updated_at records when the deletion happened.
	database.Model(&models.KnowledgeDocument{}).Where("id = ?", "del-before").UpdateColumn("updated_at", jobStart.Add(-time.Minute))
	database.Model(&models.KnowledgeDocument{}).Where("id = ?", "del-after").UpdateColumn("updated_at", jobStart.Add(2*time.Second))

	ks.restoreDeletedDocuments()

	job := &IngestionJobStatus{JobID: "job-1", Status: "COMPLETE", StartedAt: &jobStart}
	if _, err := ks.SyncCompletedDocuments(ctx, job); err != nil {
		t.Fatalf("SyncCompletedDocuments: %v", err)
	}

	var count int64
	database.Model(&models.KnowledgeDocument{}).Where("id = ?", "del-before").Count(&count)
	if count != 0 {
		t.Errorf("document deleted before the job should be removed from DB")
	}
	database.Model(&models.KnowledgeDocument{}).Where("id = ?", "del-after").Count(&count)
	if count != 1 {
		t.Errorf("document deleted after the job started must stay DELETING")
	}

	if ks.IsChunkFromDeletedDocument(nil, map[string]interface{}{"document_id": "del-before"}) {
		t.Errorf("finalized document should be cleared from the exclusion map")
	}
	if !ks.IsChunkFromDeletedDocument(nil, map[string]interface{}{"document_id": "del-after"}) {
		t.Errorf("pending deletion must still be excluded from RAG")
	}

	added, deleted := ks.CountPendingChanges(ctx)
	if added != 0 || deleted != 1 {
		t.Errorf("expected pending (0 added, 1 deleted), got (%d, %d)", added, deleted)
	}
}

func TestKnowledgeService_RestoresDeletedDocumentsOnStartup(t *testing.T) {
	_, database := newKnowledgeTestService(t)

	doc := models.KnowledgeDocument{ID: "del-1", S3Key: "groups/sales/x.pdf", S3Bucket: "b", Filename: "x.pdf", ContentType: "pdf", GroupName: "sales", SyncStatus: SyncStatusDeleting}
	if err := database.Create(&doc).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Simulates a restart: a fresh service must rebuild the exclusion maps from the DB.
	restarted := NewKnowledgeService(nil, database, &config.Config{})
	if !restarted.IsChunkFromDeletedDocument(nil, map[string]interface{}{"document_id": "del-1"}) {
		t.Errorf("DELETING document should be excluded by document_id after restart")
	}
	loc := map[string]interface{}{"s3Location": map[string]interface{}{"uri": "s3://b/groups/sales/x.pdf"}}
	if !restarted.IsChunkFromDeletedDocument(loc, map[string]interface{}{}) {
		t.Errorf("DELETING document should be excluded by S3 key after restart")
	}
}

func TestKnowledgeService_ReleaseDeletingKey(t *testing.T) {
	ks, database := newKnowledgeTestService(t)
	ctx := context.Background()

	live := models.KnowledgeDocument{ID: "live", S3Key: "public/live.pdf", S3Bucket: "b", Filename: "live.pdf", ContentType: "pdf", GroupName: "public", SyncStatus: SyncStatusSynced}
	old := models.KnowledgeDocument{ID: "old", S3Key: "public/same.pdf", S3Bucket: "b", Filename: "same.pdf", ContentType: "pdf", GroupName: "public", SyncStatus: SyncStatusDeleting}
	database.Create(&live)
	database.Create(&old)

	if err := ks.ReleaseDeletingKey(ctx, &live); err == nil {
		t.Errorf("a live document must not be released")
	}

	found, err := ks.FindDocumentByKey(ctx, BuildObjectKey("public", "same.pdf"))
	if err != nil || found == nil || found.ID != "old" {
		t.Fatalf("expected to find the DELETING document, got %v (err %v)", found, err)
	}
	if err := ks.ReleaseDeletingKey(ctx, found); err != nil {
		t.Fatalf("ReleaseDeletingKey: %v", err)
	}

	// The key is free for a new document; the old record stays DELETING.
	if found, _ := ks.FindDocumentByKey(ctx, "public/same.pdf"); found != nil {
		t.Errorf("key should be free after release, found %s", found.ID)
	}
	newDoc := models.KnowledgeDocument{ID: "new", S3Key: "public/same.pdf", S3Bucket: "b", Filename: "same.pdf", ContentType: "pdf", GroupName: "public", SyncStatus: SyncStatusS3Uploaded}
	if err := database.Create(&newDoc).Error; err != nil {
		t.Fatalf("new document at the released key should be insertable: %v", err)
	}
	var reloaded models.KnowledgeDocument
	database.First(&reloaded, "id = ?", "old")
	if reloaded.SyncStatus != SyncStatusDeleting {
		t.Errorf("old record should remain DELETING, got %s", reloaded.SyncStatus)
	}

	// After a restart, the new upload at the same key must not be hidden from RAG.
	restarted := NewKnowledgeService(nil, database, &config.Config{})
	loc := map[string]interface{}{"s3Location": map[string]interface{}{"uri": "s3://b/public/same.pdf"}}
	if restarted.IsChunkFromDeletedDocument(loc, map[string]interface{}{"document_id": "new"}) {
		t.Errorf("new document at a released key must not be excluded")
	}
	if !restarted.IsChunkFromDeletedDocument(loc, map[string]interface{}{"document_id": "old"}) {
		t.Errorf("old version chunks must stay excluded by document_id")
	}
}
