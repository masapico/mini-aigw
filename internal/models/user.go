package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type User struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	AuthType     string    `gorm:"size:20;not null;default:'local'" json:"auth_type"` // 'oidc' | 'local'
	OIDCSub      *string   `gorm:"size:255;uniqueIndex" json:"oidc_sub"`
	Username     string    `gorm:"size:100;uniqueIndex;not null" json:"username"`
	PasswordHash *string   `gorm:"size:255" json:"-"`
	Email        *string   `gorm:"size:255" json:"email"`
	DisplayName  *string   `gorm:"size:100" json:"display_name"`
	Roles        string    `gorm:"type:text;not null;default:'[]'" json:"roles"`   // JSON array e.g. '["admin"]'
	Groups       string    `gorm:"type:text;not null;default:'[]'" json:"groups"`  // JSON array e.g. '["group000"]'
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == "" {
		u.ID = uuid.New().String()
	}
	return nil
}

func (u *User) GetRoles() []string {
	var roles []string
	if u.Roles != "" {
		_ = json.Unmarshal([]byte(u.Roles), &roles)
	}
	return roles
}

func (u *User) SetRoles(roles []string) {
	bytes, _ := json.Marshal(roles)
	u.Roles = string(bytes)
}

func (u *User) HasRole(role string) bool {
	for _, r := range u.GetRoles() {
		if r == role {
			return true
		}
	}
	return false
}

func (u *User) GetGroups() []string {
	var groups []string
	if u.Groups != "" {
		_ = json.Unmarshal([]byte(u.Groups), &groups)
	}
	return groups
}

func (u *User) SetGroups(groups []string) {
	bytes, _ := json.Marshal(groups)
	u.Groups = string(bytes)
}

func (u *User) GetEffectiveName() string {
	if u.DisplayName != nil && *u.DisplayName != "" {
		return *u.DisplayName
	}
	return u.Username
}

type Session struct {
	SessionID  string    `gorm:"primaryKey;size:128" json:"session_id"`
	UserID     string    `gorm:"size:64;not null;index" json:"user_id"`
	RawIDToken *string   `gorm:"type:text" json:"raw_id_token"`
	ExpiresAt  time.Time `gorm:"not null;index" json:"expires_at"`
	CreatedAt  time.Time `json:"created_at"`

	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

