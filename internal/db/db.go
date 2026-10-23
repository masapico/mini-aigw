package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

var DB *gorm.DB

func InitDB(cfg *config.Config) (*gorm.DB, error) {
	dbPath := cfg.DatabaseURL
	if strings.HasPrefix(dbPath, "sqlite:///") {
		dbPath = strings.TrimPrefix(dbPath, "sqlite:///")
	} else if strings.HasPrefix(dbPath, "sqlite://") {
		dbPath = strings.TrimPrefix(dbPath, "sqlite://")
	}

	// データベース用ディレクトリの作成
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create db directory %s: %w", dir, err)
		}
	}

	// modernc.org/sqlite ドライバー向け接続文字列
	// _pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	logLevel := logger.Warn
	if cfg.Debug {
		logLevel = logger.Info
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}

	// コネクションプール設定 (高並行性対応)
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)

	// PRAGMA の確実な適用
	if err := applyPragmas(sqlDB); err != nil {
		return nil, fmt.Errorf("failed to apply sqlite pragmas: %w", err)
	}

	// 自動マイグレーション
	err = db.AutoMigrate(
		&models.User{},
		&models.Session{},
		&models.Group{},
		&models.ApiKey{},
		&models.LLMModel{},
		&models.KnowledgeDocument{},
		&models.ChatUsageLog{},
		&models.ZeroHitQuery{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to auto migrate: %w", err)
	}

	// 旧バージョンで平文保存していた API キー (raw_key) を物理的に削除する。
	// キーは発行時に1回のみ表示し、以降はハッシュのみを保持する。
	if db.Migrator().HasColumn("api_keys", "raw_key") {
		if err := db.Exec("ALTER TABLE api_keys DROP COLUMN raw_key").Error; err != nil {
			// DROP COLUMN に失敗した場合でも平文を残さないよう NULL 化する
			if err2 := db.Exec("UPDATE api_keys SET raw_key = NULL").Error; err2 != nil {
				return nil, fmt.Errorf("failed to scrub legacy raw_key: %w", err2)
			}
			log.Printf("warning: could not drop api_keys.raw_key (values nulled): %v", err)
		}
	}

	DB = db
	log.Printf("SQLite initialized successfully at %s (WAL mode enabled)", dbPath)
	return db, nil
}

func applyPragmas(sqlDB *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, pragma := range pragmas {
		if _, err := sqlDB.Exec(pragma); err != nil {
			return fmt.Errorf("executing '%s': %w", pragma, err)
		}
	}
	return nil
}

