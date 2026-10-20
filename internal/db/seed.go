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
				ID:                  "jp.amazon.nova-2-lite-v1:0",
				Name:                "Amazon Nova 2 Lite",
				Provider:            "bedrock",
				InputPricePer1M:     0.06,
				OutputPricePer1M:    0.24,
				SupportsTemperature: true,
				IsActive:            true,
				IsDefault:           true,
			},
			{
				ID:                  "amazon.nova-lite-v1:0",
				Name:                "Amazon Nova Lite",
				Provider:            "bedrock",
				InputPricePer1M:     0.06,
				OutputPricePer1M:    0.24,
				SupportsTemperature: true,
				IsActive:            true,
				IsDefault:           false,
			},
			{
				ID:                  "jp.anthropic.claude-haiku-5-5",
				Name:                "Claude Haiku 5.5",
				Provider:            "bedrock",
				InputPricePer1M:     0.80,
				OutputPricePer1M:    4.00,
				SupportsTemperature: false,
				IsActive:            true,
				IsDefault:           false,
			},
		}
		for _, m := range modelsList {
			if err := db.Create(&m).Error; err != nil {
				return err
			}
		}
		log.Println("Seeded default LLM models")
	} else {
		// 既存モデルの単価が 0 の場合、代表的モデルに初期単価を補完
		db.Model(&models.LLMModel{}).Where("id LIKE ? AND input_price_per_1m = 0", "%nova%lite%").Updates(map[string]interface{}{
			"input_price_per_1m":  0.06,
			"output_price_per_1m": 0.24,
		})
		db.Model(&models.LLMModel{}).Where("id LIKE ? AND input_price_per_1m = 0", "%claude%haiku%").Updates(map[string]interface{}{
			"input_price_per_1m":  0.80,
			"output_price_per_1m": 4.00,
		})
		// Claude Haiku 5.5 等のサンプリング非対応モデルの設定を更新
		db.Model(&models.LLMModel{}).Where("id LIKE ?", "%claude-haiku-5%").Updates(map[string]interface{}{
			"supports_temperature": false,
			"name":                 "Claude Haiku 5.5",
		})
	}

	return nil
}
