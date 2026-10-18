package models

import (
	"time"
)

type LLMModel struct {
	ID          string    `gorm:"primaryKey;size:100" json:"id"`
	Name        string    `gorm:"size:100;not null" json:"name"`
	Description *string   `gorm:"size:255" json:"description"`
	Provider    string    `gorm:"size:50;not null;default:'bedrock'" json:"provider"`
	IsActive    bool      `gorm:"not null;default:true" json:"is_active"`
	IsDefault   bool      `gorm:"not null;default:false" json:"is_default"`
	CreatedAt   time.Time `json:"created_at"`
}

func (LLMModel) TableName() string {
	return "llm_models"
}
