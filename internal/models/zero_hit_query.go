package models

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ZeroHitQuery は RAG 検索でヒット 0 件またはスコア閾値未満となった質問を集約管理するモデルです
type ZeroHitQuery struct {
	ID                  string    `gorm:"primaryKey;size:64" json:"id"`
	Query               string    `gorm:"size:1000;not null" json:"query"` // ユーザーの質問原文
	NormalizedQueryHash string    `gorm:"size:64;index:idx_group_qhash,unique;not null" json:"normalized_query_hash"` // SHA256(GroupName + "::" + NormalizedQuery)
	GroupName           string    `gorm:"size:50;index;not null" json:"group_name"`
	HitCount            int       `gorm:"not null;default:1" json:"hit_count"` // 発生回数
	MaxScore            float64   `gorm:"not null;default:0.0" json:"max_score"` // 検索時の最高スコア
	Reason              string    `gorm:"size:30;not null;default:'ZERO_RESULTS'" json:"reason"` // "0件ヒット" | "スコア不足"
	Status              string    `gorm:"size:20;index;not null;default:'unresolved'" json:"status"` // "unresolved" | "resolved" | "ignored"
	FirstOccurredAt     time.Time `gorm:"not null" json:"first_occurred_at"`
	LastOccurredAt      time.Time `gorm:"index;not null" json:"last_occurred_at"`
	LastApiKeyName      string    `gorm:"size:100;default:'unknown'" json:"last_api_key_name"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (ZeroHitQuery) TableName() string {
	return "zero_hit_queries"
}

func (z *ZeroHitQuery) BeforeCreate(tx *gorm.DB) error {
	if z.ID == "" {
		z.ID = uuid.New().String()
	}
	if z.NormalizedQueryHash == "" {
		z.NormalizedQueryHash = ComputeQueryHash(z.GroupName, z.Query)
	}
	if z.Status == "" {
		z.Status = "unresolved"
	}
	return nil
}

// ComputeQueryHash はグループ名と空白正規化・小文字化したクエリから一意のハッシュを計算します
func ComputeQueryHash(groupName, query string) string {
	normalized := strings.ToLower(strings.TrimSpace(query))
	fields := strings.Fields(normalized)
	normalized = strings.Join(fields, " ")
	raw := strings.TrimSpace(groupName) + "::" + normalized
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

