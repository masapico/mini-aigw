package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	agentTypes "github.com/aws/aws-sdk-go-v2/service/bedrockagent/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

type KnowledgeService struct {
	awsBundle *AWSClientBundle
	db        *gorm.DB
	cfg       *config.Config

	mu             sync.RWMutex
	deletedDocIDs  map[string]bool
	deletedS3Keys  map[string]bool
}

func NewKnowledgeService(awsBundle *AWSClientBundle, db *gorm.DB, cfg *config.Config) *KnowledgeService {
	return &KnowledgeService{
		awsBundle:     awsBundle,
		db:            db,
		cfg:           cfg,
		deletedDocIDs: make(map[string]bool),
		deletedS3Keys: make(map[string]bool),
	}
}

type UploadResult struct {
	S3Key   string
	MetaKey string
}

func (s *KnowledgeService) UploadDocumentWithMetadata(
	ctx context.Context,
	bucket string,
	prefix string,
	filename string,
	content []byte,
	group string,
	documentID string,
	contentType string,
	tags []string,
) (*UploadResult, error) {
	prefixClean := strings.Trim(prefix, "/")
	objKey := filename
	if prefixClean != "" {
		objKey = fmt.Sprintf("%s/%s", prefixClean, filename)
	}
	metaKey := fmt.Sprintf("%s.metadata.json", objKey)

	s.mu.Lock()
	delete(s.deletedS3Keys, objKey)
	if documentID != "" {
		delete(s.deletedDocIDs, documentID)
	}
	s.mu.Unlock()

	sourcePrefix := ""
	if prefixClean != "" {
		sourcePrefix = prefixClean + "/"
	}

	metaAttrs := map[string]interface{}{
		"group":         group,
		"filename":      filename,
		"source_prefix": sourcePrefix,
	}
	if documentID != "" {
		metaAttrs["document_id"] = documentID
	}
	if len(tags) > 0 {
		var cleanTags []string
		for _, t := range tags {
			trimmed := strings.TrimSpace(t)
			if trimmed != "" {
				cleanTags = append(cleanTags, trimmed)
			}
		}
		if len(cleanTags) > 0 {
			metaAttrs["tags"] = cleanTags
			metaAttrs["tag_string"] = strings.Join(cleanTags, ", ")
		}
	}

	metaPayload := map[string]interface{}{
		"metadataAttributes": metaAttrs,
	}
	metaBytes, err := json.MarshalIndent(metaPayload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// 1. Upload main file
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err = s.awsBundle.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(objKey),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload s3 object: %w", err)
	}

	// 2. Upload metadata json
	_, err = s.awsBundle.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(metaKey),
		Body:        bytes.NewReader(metaBytes),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload s3 metadata: %w", err)
	}

	log.Printf("Uploaded document %s and metadata %s to bucket %s", objKey, metaKey, bucket)
	return &UploadResult{S3Key: objKey, MetaKey: metaKey}, nil
}

type DocumentDownload struct {
	Body          io.ReadCloser
	ContentType   string
	ContentLength int64
}

func (s *KnowledgeService) GetDocumentStream(ctx context.Context, bucket, key string) (*DocumentDownload, error) {
	out, err := s.awsBundle.S3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}

	contentType := "application/octet-stream"
	if out.ContentType != nil {
		contentType = *out.ContentType
	}
	contentLength := int64(0)
	if out.ContentLength != nil {
		contentLength = *out.ContentLength
	}

	return &DocumentDownload{
		Body:          out.Body,
		ContentType:   contentType,
		ContentLength: contentLength,
	}, nil
}

func (s *KnowledgeService) StartIngestionJobDetail(ctx context.Context, kbID, dataSourceID string) (string, string, error) {
	input := &bedrockagent.StartIngestionJobInput{
		KnowledgeBaseId: aws.String(kbID),
		DataSourceId:    aws.String(dataSourceID),
	}
	out, err := s.awsBundle.BedrockAgent.StartIngestionJob(ctx, input)
	if err != nil {
		return "", "", err
	}

	jobID := ""
	if out.IngestionJob.IngestionJobId != nil {
		jobID = *out.IngestionJob.IngestionJobId
	}
	status := string(out.IngestionJob.Status)
	return jobID, status, nil
}

type IngestionJobStatus struct {
	JobID      string     `json:"job_id"`
	Status     string     `json:"status"` // STARTING, IN_PROGRESS, COMPLETE, FAILED
	StartedAt  *time.Time `json:"started_at"`
	UpdatedAt  *time.Time `json:"updated_at"`
	Stats      map[string]int64 `json:"stats"`
}

func (s *KnowledgeService) GetLatestIngestionJobStatus(ctx context.Context, kbID, dataSourceID string) (*IngestionJobStatus, error) {
	input := &bedrockagent.ListIngestionJobsInput{
		KnowledgeBaseId: aws.String(kbID),
		DataSourceId:    aws.String(dataSourceID),
		MaxResults:      aws.Int32(1),
		SortBy: &agentTypes.IngestionJobSortBy{
			Attribute: agentTypes.IngestionJobSortByAttributeStartedAt,
			Order:     agentTypes.SortOrderDescending,
		},
	}
	out, err := s.awsBundle.BedrockAgent.ListIngestionJobs(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(out.IngestionJobSummaries) == 0 {
		return &IngestionJobStatus{Status: "NONE"}, nil
	}

	latest := out.IngestionJobSummaries[0]
	res := &IngestionJobStatus{
		JobID:     aws.ToString(latest.IngestionJobId),
		Status:    string(latest.Status),
		StartedAt: latest.StartedAt,
		UpdatedAt: latest.UpdatedAt,
		Stats:     make(map[string]int64),
	}
	if latest.Statistics != nil {
		res.Stats["documentsScanned"] = latest.Statistics.NumberOfDocumentsScanned
		res.Stats["newDocumentsIndexed"] = latest.Statistics.NumberOfNewDocumentsIndexed
		res.Stats["modifiedDocumentsIndexed"] = latest.Statistics.NumberOfModifiedDocumentsIndexed
		res.Stats["deletedDocumentsIndexed"] = latest.Statistics.NumberOfDocumentsDeleted
		res.Stats["failedDocuments"] = latest.Statistics.NumberOfDocumentsFailed
	}

	return res, nil
}

// SyncCompletedDocuments updates pending documents (S3_UPLOADED) created prior to the completed job to SYNCED.
func (s *KnowledgeService) SyncCompletedDocuments(ctx context.Context, job *IngestionJobStatus) (int64, error) {
	if job == nil || job.Status != "COMPLETE" {
		return 0, nil
	}

	jobTime := job.StartedAt
	if jobTime == nil {
		jobTime = job.UpdatedAt
	}
	if jobTime == nil {
		return 0, nil
	}

	// Add 5-second buffer to absorb minor clock skew between server and AWS, converted to UTC string
	adjustedTime := jobTime.Add(5 * time.Second).UTC().Format("2006-01-02 15:04:05")

	res := s.db.WithContext(ctx).
		Model(&models.KnowledgeDocument{}).
		Where("sync_status = ? AND datetime(created_at) <= datetime(?)", "S3_UPLOADED", adjustedTime).
		Update("sync_status", "SYNCED")
	if res.Error != nil {
		log.Printf("Failed to sync completed documents: %v", res.Error)
		return 0, res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("Marked %d documents as SYNCED for completed job %s (jobTime: %s)", res.RowsAffected, job.JobID, adjustedTime)
	}
	return res.RowsAffected, nil
}

func (s *KnowledgeService) DeleteKnowledgeDocument(ctx context.Context, docID string) error {
	var doc models.KnowledgeDocument
	if err := s.db.First(&doc, "id = ?", docID).Error; err != nil {
		return err
	}

	doc.SyncStatus = "DELETING"
	s.db.Save(&doc)

	s.mu.Lock()
	s.deletedDocIDs[doc.ID] = true
	s.deletedS3Keys[doc.S3Key] = true
	s.mu.Unlock()

	// Delete from S3
	_, _ = s.awsBundle.S3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(doc.S3Bucket),
		Key:    aws.String(doc.S3Key),
	})
	_, _ = s.awsBundle.S3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(doc.S3Bucket),
		Key:    aws.String(fmt.Sprintf("%s.metadata.json", doc.S3Key)),
	})

	// Trigger Ingestion Job
	dsID := s.cfg.BedrockDataSourceID
	if doc.DataSourceID != nil && *doc.DataSourceID != "" {
		dsID = *doc.DataSourceID
	}
	_, _, _ = s.StartIngestionJobDetail(ctx, s.cfg.BedrockKBID, dsID)

	// Remove from DB
	return s.db.Delete(&doc).Error
}

func (s *KnowledgeService) IsChunkFromDeletedDocument(chunkLocation map[string]interface{}, chunkMetadata map[string]interface{}) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if docID, ok := chunkMetadata["document_id"].(string); ok && s.deletedDocIDs[docID] {
		return true
	}

	if s3Loc, ok := chunkLocation["s3Location"].(map[string]interface{}); ok {
		if uri, ok := s3Loc["uri"].(string); ok {
			for deletedKey := range s.deletedS3Keys {
				if strings.HasSuffix(uri, "/"+deletedKey) || strings.HasSuffix(uri, deletedKey) {
					return true
				}
			}
		}
	}

	return false
}

func (s *KnowledgeService) FindMatchingMetadataFromQuery(query string) ([]string, []string) {
	qLower := strings.ToLower(query)
	var docs []models.KnowledgeDocument
	s.db.Find(&docs)

	tagSet := make(map[string]bool)
	var matchedDocIDs []string

	for _, doc := range docs {
		matched := false
		for _, tag := range doc.GetTags() {
			tLower := strings.ToLower(tag)
			if len(tLower) >= 2 && strings.Contains(qLower, tLower) {
				tagSet[tag] = true
				matched = true
			}
		}
		fLower := strings.ToLower(doc.Filename)
		baseName := strings.TrimSuffix(fLower, "."+strings.ToLower(doc.ContentType))
		if len(baseName) >= 3 && strings.Contains(qLower, baseName) {
			matched = true
		}
		if matched {
			matchedDocIDs = append(matchedDocIDs, doc.ID)
		}
	}

	var matchedTags []string
	for t := range tagSet {
		matchedTags = append(matchedTags, t)
	}
	return matchedTags, matchedDocIDs
}

func (s *KnowledgeService) ResolveDocumentAndDownloadURL(
	s3URI string,
	chunkMetadata map[string]interface{},
	baseURL string,
) (docID, filename, group, downloadURL string) {
	filename = "Document"
	group = "public"

	if fn, ok := chunkMetadata["filename"].(string); ok && fn != "" {
		filename = fn
	}
	if grp, ok := chunkMetadata["group"].(string); ok && grp != "" {
		group = grp
	}
	if dID, ok := chunkMetadata["document_id"].(string); ok && dID != "" {
		docID = dID
	}

	// Reverse lookup in DB if needed
	var doc models.KnowledgeDocument
	found := false
	if docID != "" {
		if err := s.db.First(&doc, "id = ?", docID).Error; err == nil {
			found = true
		}
	}
	if !found && s3URI != "" {
		if strings.HasPrefix(s3URI, "s3://") {
			trimmed := strings.TrimPrefix(s3URI, "s3://")
			parts := strings.SplitN(trimmed, "/", 2)
			if len(parts) == 2 {
				key := parts[1]
				if err := s.db.First(&doc, "s3_key = ?", key).Error; err == nil {
					found = true
					docID = doc.ID
				}
			}
		}
	}

	if found {
		filename = doc.Filename
		group = doc.GroupName
		cleanBase := strings.TrimRight(baseURL, "/")
		downloadURL = fmt.Sprintf("%s/v1/knowledge/files/%s/download", cleanBase, doc.ID)
	}

	return docID, filename, group, downloadURL
}
