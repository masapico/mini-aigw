package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// CreateBackup は指定されたSQLiteデータベースから、整合性のあるホットバックアップを生成します。
// SQLite の VACUUM INTO 構文を使用するため、サーバー稼働中であってもトランザクション整合性を
// 保った単一のデータベースファイルが出力されます。
func CreateBackup(srcDBPath, destDBPath string) error {
	// srcDBPath のプレフィックス除去
	cleanSrc := strings.TrimPrefix(srcDBPath, "sqlite:///")
	cleanSrc = strings.TrimPrefix(cleanSrc, "sqlite://")

	if _, err := os.Stat(cleanSrc); os.IsNotExist(err) {
		return fmt.Errorf("source database does not exist: %s", cleanSrc)
	}

	// 出力先ディレクトリの作成
	destDir := filepath.Dir(destDBPath)
	if destDir != "." && destDir != "" {
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return fmt.Errorf("failed to create destination directory: %w", err)
		}
	}

	// 既存バックアップファイルが存在する場合は削除（VACUUM INTO は出力先が存在すると失敗するため）
	if _, err := os.Stat(destDBPath); err == nil {
		if err := os.Remove(destDBPath); err != nil {
			return fmt.Errorf("failed to remove existing backup file: %w", err)
		}
	}

	// modernc.org/sqlite ドライバーで読み取り専用または WAL 対応モードで開く
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)", cleanSrc)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("failed to open database for backup: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get sql.DB: %w", err)
	}
	defer sqlDB.Close()

	// VACUUM INTO による整合性スナップショットの作成
	// SQLite では Windows のバックスラッシュをスラッシュに正規化して指定する
	normalizedDest := filepath.ToSlash(destDBPath)
	// シングルクォートをエスケープ
	escapedDest := strings.ReplaceAll(normalizedDest, "'", "''")
	vacuumSQL := fmt.Sprintf("VACUUM INTO '%s';", escapedDest)

	if _, err := sqlDB.Exec(vacuumSQL); err != nil {
		return fmt.Errorf("VACUUM INTO failed: %w", err)
	}

	return nil
}

