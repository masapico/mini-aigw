package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

