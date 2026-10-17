package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ApiKey struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	UserID        *string   `gorm:"size:64;index" json:"user_id"`
	Name          string    `gorm:"size:100;not null" json:"name"`
	KeyHash       string    `gorm:"size:64;uniqueIndex;not null" json:"key_hash"`
	Prefix        string    `gorm:"size:32;not null" json:"prefix"`
	RawKey        *string   `gorm:"size:128" json:"raw_key,omitempty"` // 作成時・再コピー用
	AllowedGroups string    `gorm:"type:text;not null;default:'[\"public\"]'" json:"allowed_groups"`
	ModelID       *string   `gorm:"size:100" json:"model_id"`
	SystemPrompt  *string   `gorm:"type:text" json:"system_prompt"`
	CreatedAt     time.Time `json:"created_at"`

	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (k *ApiKey) BeforeCreate(tx *gorm.DB) error {
	if k.ID == "" {
		k.ID = uuid.New().String()
	}
	return nil
}

func (k *ApiKey) GetAllowedGroups() []string {
	if k.AllowedGroups == "" {
		return []string{"public"}
	}
	var groups []string
	if err := json.Unmarshal([]byte(k.AllowedGroups), &groups); err != nil {
		return []string{"public"}
	}
	if groups == nil {
		return []string{}
	}
	return groups
}

func (k *ApiKey) SetAllowedGroups(groups []string) {
	bytes, _ := json.Marshal(groups)
	k.AllowedGroups = string(bytes)
}

func (k *ApiKey) GetModelID() string {
	if k.ModelID == nil {
		return ""
	}
	return *k.ModelID
}

func (k *ApiKey) GetSystemPrompt() string {
	if k.SystemPrompt == nil {
		return ""
	}
	return *k.SystemPrompt
}

func (k *ApiKey) GetRawKey() string {
	if k.RawKey == nil {
		return ""
	}
	return *k.RawKey
}

