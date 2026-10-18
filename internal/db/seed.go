package db

import (
	"log"

	"gorm.io/gorm"

	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

func SeedInitialData(db *gorm.DB, cfg *config.Config) error {
	// 1. グループのシード
	var groupCount int64
	db.Model(&models.Group{}).Count(&groupCount)
	if groupCount == 0 {
		desc0 := "グループ0のナレッジ"
		desc1 := "グループ1のナレッジ"
		groups := []models.Group{
			{Name: "group000", DisplayName: "グループ000", Description: &desc0},
			{Name: "group001", DisplayName: "グループ001", Description: &desc1},
		}
		for _, g := range groups {
			if err := db.Create(&g).Error; err != nil {
				return err
			}
		}
		log.Println("Seeded default groups: 'group000', 'group001'")
	}

	// 2. admin ユーザーのシード
	var userCount int64
	db.Model(&models.User{}).Count(&userCount)
	if userCount == 0 {
		hash, err := auth.HashPassword("Password1234")
		if err != nil {
			return err
		}
		email := "admin@internal.local"
		displayName := "System Admin"
		adminUser := models.User{
			AuthType:     "local",
			Username:     "admin",
			PasswordHash: &hash,
			Email:        &email,
			DisplayName:  &displayName,
			Roles:        `["admin"]`,
			Groups:       `["group000", "group001"]`,
		}
		if err := db.Create(&adminUser).Error; err != nil {
			return err
		}
		log.Println("Seeded default admin user: 'admin' (password: 'Password1234')")
	}

	// 3. モデルマスタのシード
	var modelCount int64
	db.Model(&models.LLMModel{}).Count(&modelCount)
	if modelCount == 0 {
		modelsList := []models.LLMModel{
			{
				ID:        "jp.amazon.nova-2-lite-v1:0",
				Name:      "Amazon Nova 2 Lite",
				Provider:  "bedrock",
				IsActive:  true,
				IsDefault: true,
			},
			{
				ID:        "amazon.nova-lite-v1:0",
				Name:      "Amazon Nova Lite",
				Provider:  "bedrock",
				IsActive:  true,
				IsDefault: false,
			},
		}
		for _, m := range modelsList {
			if err := db.Create(&m).Error; err != nil {
				return err
			}
		}
		log.Println("Seeded default LLM models")
	}

	return nil
}
