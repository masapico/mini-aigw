package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ChatUsageLog struct {
	ID               string    `gorm:"primaryKey;size:64" json:"id"`
	ApiKeyID         *string   `gorm:"size:64;index" json:"api_key_id"`
	ApiKeyName       string    `gorm:"size:100;default:'unknown'" json:"api_key_name"`
	ModelID          string    `gorm:"size:100;index" json:"model_id"`
	IsStream         bool      `gorm:"not null;default:false" json:"is_stream"`
	RAGTriggered     bool      `gorm:"not null;default:false;index" json:"rag_triggered"`
	SearchQueries    *string   `gorm:"type:text" json:"search_queries"`  // JSON array: ["query1"]
	CitedDocuments   *string   `gorm:"type:text" json:"cited_documents"` // JSON array: [{"document_id":..., "filename":..., "group":..., "score":...}]
	PromptTokens     int       `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int       `gorm:"not null;default:0" json:"completion_tokens"`
	TotalTokens      int       `gorm:"not null;default:0" json:"total_tokens"`
	LatencyMs        int64     `gorm:"not null;default:0" json:"latency_ms"`
	Status           string    `gorm:"size:20;not null;default:'success'" json:"status"` // "success" | "error"
	ErrorMessage     *string   `gorm:"type:text" json:"error_message"`
	CreatedAt        time.Time `gorm:"index" json:"created_at"`

	ApiKey *ApiKey `gorm:"foreignKey:ApiKeyID" json:"api_key,omitempty"`
}

func (ChatUsageLog) TableName() string {
	return "chat_usage_logs"
}

func (c *ChatUsageLog) BeforeCreate(tx *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	return nil
}

func (c *ChatUsageLog) GetSearchQueries() []string {
	if c.SearchQueries == nil || *c.SearchQueries == "" {
		return []string{}
	}
	var queries []string
	if err := json.Unmarshal([]byte(*c.SearchQueries), &queries); err == nil {
		return queries
	}
	return []string{}
}

func (c *ChatUsageLog) GetCitedDocuments() []map[string]interface{} {
	if c.CitedDocuments == nil || *c.CitedDocuments == "" {
		return []map[string]interface{}{}
	}
	var docs []map[string]interface{}
	if err := json.Unmarshal([]byte(*c.CitedDocuments), &docs); err == nil {
		return docs
	}
	return []map[string]interface{}{}
}

