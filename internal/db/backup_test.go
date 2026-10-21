package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

func TestCreateBackup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mini-aigw-backup-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	srcDBPath := filepath.Join(tmpDir, "source.db")
	cfg := &config.Config{
		DatabaseURL: "sqlite:///" + srcDBPath,
		Debug:       false,
	}

	// 1. ソースDBの初期化とシード
	sourceDB, err := InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	if err := SeedInitialData(sourceDB, cfg); err != nil {
		t.Fatalf("SeedInitialData failed: %v", err)
	}

	// テスト用のユーザーを追加
	testUser := models.User{
		Username: "backup_test_user",
		Roles:    `["user"]`,
	}
	if err := sourceDB.Create(&testUser).Error; err != nil {
		t.Fatalf("failed to insert test user: %v", err)
	}

	// 2. バックアップの作成
	backupDBPath := filepath.Join(tmpDir, "backups", "backup.db")
	if err := CreateBackup(srcDBPath, backupDBPath); err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	// バックアップファイルが存在することを確認
	if _, err := os.Stat(backupDBPath); os.IsNotExist(err) {
		t.Fatalf("backup file does not exist: %s", backupDBPath)
	}

	// 3. バックアップ先DBを開いて整合性を検証
	backupDB, err := gorm.Open(sqlite.Open(backupDBPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open backup database: %v", err)
	}

	var foundUser models.User
	if err := backupDB.Where("username = ?", "backup_test_user").First(&foundUser).Error; err != nil {
		t.Fatalf("failed to find user in backup db: %v", err)
	}
	if foundUser.Username != "backup_test_user" {
		t.Errorf("expected username 'backup_test_user', got '%s'", foundUser.Username)
	}

	// 検証完了後に接続を閉じる (Windows でのファイルロック解除)
	backupSQLDB, err := backupDB.DB()
	if err != nil {
		t.Fatalf("failed to get backup sql.DB: %v", err)
	}
	_ = backupSQLDB.Close()

	// 4. 既存ファイルが存在する場合の上書きテスト
	if err := CreateBackup(srcDBPath, backupDBPath); err != nil {
		t.Fatalf("second CreateBackup (overwrite) failed: %v", err)
	}
}
