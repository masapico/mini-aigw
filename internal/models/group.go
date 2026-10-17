package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Group struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	Name        string    `gorm:"size:50;uniqueIndex;not null" json:"name"`
	DisplayName string    `gorm:"size:100;not null" json:"display_name"`
	Description *string   `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func (g *Group) BeforeCreate(tx *gorm.DB) error {
	if g.ID == "" {
		g.ID = uuid.New().String()
	}
	return nil
}

