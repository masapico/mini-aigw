package models

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type KnowledgeDocument struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	S3Key        string    `gorm:"size:512;uniqueIndex;not null" json:"s3_key"`
	S3Bucket     string    `gorm:"size:255;not null" json:"s3_bucket"`
	Filename     string    `gorm:"size:255;not null" json:"filename"`
	FileSize     int64     `gorm:"not null" json:"file_size"`
	ContentType  string    `gorm:"size:100;not null" json:"content_type"`
	GroupName    string    `gorm:"size:50;index;not null" json:"group_name"`
	Tags         *string   `gorm:"type:text" json:"tags"`
	UploaderID   *string   `gorm:"size:64;index" json:"uploader_id"`
	SyncStatus   string    `gorm:"size:20;not null;default:'PENDING'" json:"sync_status"` // 'PENDING' | 'SYNCED' | 'DELETING' | 'ERROR'
	DataSourceID *string   `gorm:"size:100" json:"data_source_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	Uploader *User `gorm:"foreignKey:UploaderID" json:"uploader,omitempty"`
}

func (KnowledgeDocument) TableName() string {
	return "knowledge_documents"
}

func (k *KnowledgeDocument) BeforeCreate(tx *gorm.DB) error {
	if k.ID == "" {
		k.ID = uuid.New().String()
	}
	return nil
}

func (k *KnowledgeDocument) GetTags() []string {
	if k.Tags == nil || *k.Tags == "" {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal([]byte(*k.Tags), &tags); err == nil {
		var clean []string
		for _, t := range tags {
			trimmed := strings.TrimSpace(t)
			if trimmed != "" {
				clean = append(clean, trimmed)
			}
		}
		return clean
	}
	// カンマ区切りのフォールバック
	parts := strings.Split(*k.Tags, ",")
	var clean []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	return clean
}

func (k *KnowledgeDocument) SetTags(tags []string) {
	var clean []string
	for _, t := range tags {
		trimmed := strings.TrimSpace(t)
		if trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	bytes, _ := json.Marshal(clean)
	str := string(bytes)
	k.Tags = &str
}

