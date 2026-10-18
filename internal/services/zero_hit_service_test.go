package services_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"mini-aigw/internal/models"
	"mini-aigw/internal/services"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:mem_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.ZeroHitQuery{}); err != nil {
		t.Fatalf("failed to migrate ZeroHitQuery: %v", err)
	}
	return db
}

func TestZeroHitService_RecordAndDeduplicate(t *testing.T) {
	db := setupTestDB(t)
	svc := services.NewZeroHitService(db)

	// 1. 最初の記録
	svc.RecordZeroHitAsync("有休 申請 方法", "hr", 0.20, "スコア不足", "test-key-1")
	time.Sleep(50 * time.Millisecond) // 非同期完了待ち

	var count int64
	db.Model(&models.ZeroHitQuery{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 record, got %d", count)
	}

	var first models.ZeroHitQuery
	db.First(&first)
	if first.HitCount != 1 {
		t.Errorf("expected HitCount 1, got %d", first.HitCount)
	}
	if first.MaxScore != 0.20 {
		t.Errorf("expected MaxScore 0.20, got %f", first.MaxScore)
	}
	if first.Status != "unresolved" {
		t.Errorf("expected status 'unresolved', got %s", first.Status)
	}

	// 2. 表記揺れ（大文字・複数スペース）の同一クエリ再検索
	svc.RecordZeroHitAsync("  有休   申請 方法  ", "hr", 0.25, "スコア不足", "test-key-2")
	time.Sleep(50 * time.Millisecond)

	db.Model(&models.ZeroHitQuery{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected still 1 record due to deduplication, got %d", count)
	}

	var updated models.ZeroHitQuery
	db.First(&updated)
	if updated.HitCount != 2 {
		t.Errorf("expected HitCount 2, got %d", updated.HitCount)
	}
	if updated.MaxScore != 0.25 {
		t.Errorf("expected MaxScore updated to 0.25, got %f", updated.MaxScore)
	}
	if updated.LastApiKeyName != "test-key-2" {
		t.Errorf("expected LastApiKeyName 'test-key-2', got %s", updated.LastApiKeyName)
	}
}

func TestZeroHitService_StatusTransitions(t *testing.T) {
	db := setupTestDB(t)
	svc := services.NewZeroHitService(db)

	svc.RecordZeroHitAsync("社内ポータル ログイン", "public", 0.0, "0件ヒット", "web-key")
	time.Sleep(50 * time.Millisecond)

	var item models.ZeroHitQuery
	db.First(&item)

	// 1. 解決済みに更新
	updated, err := svc.UpdateStatus(item.ID, "resolved")
	if err != nil {
		t.Fatalf("failed to update status: %v", err)
	}
	if updated.Status != "resolved" {
		t.Errorf("expected status 'resolved', got %s", updated.Status)
	}

	// 2. 解決済みクエリが再検索されたら自動で未解決に復帰
	svc.RecordZeroHitAsync("社内ポータル ログイン", "public", 0.1, "スコア不足", "web-key")
	time.Sleep(50 * time.Millisecond)

	db.First(&item, "id = ?", item.ID)
	if item.Status != "unresolved" {
		t.Errorf("expected status reopened to 'unresolved', got %s", item.Status)
	}
	if item.HitCount != 2 {
		t.Errorf("expected HitCount 2, got %d", item.HitCount)
	}

	// 3. 対象外に設定
	_, err = svc.UpdateStatus(item.ID, "ignored")
	if err != nil {
		t.Fatalf("failed to set status to ignored: %v", err)
	}

	// 4. 対象外クエリが再検索された場合は対象外を維持
	svc.RecordZeroHitAsync("社内ポータル ログイン", "public", 0.0, "0件ヒット", "web-key")
	time.Sleep(50 * time.Millisecond)

	db.First(&item, "id = ?", item.ID)
	if item.Status != "ignored" {
		t.Errorf("expected status to stay 'ignored', got %s", item.Status)
	}
	if item.HitCount != 3 {
		t.Errorf("expected HitCount 3, got %d", item.HitCount)
	}
}

func TestZeroHitService_ListAndExportCSV(t *testing.T) {
	db := setupTestDB(t)
	svc := services.NewZeroHitService(db)

	svc.RecordZeroHitAsync("HR質問1", "hr", 0.1, "0件ヒット", "key-hr")
	svc.RecordZeroHitAsync("Sales質問1", "sales", 0.2, "スコア不足", "key-sales")
	time.Sleep(50 * time.Millisecond)

	// グループフィルタ
	hrQueries, err := svc.ListQueries([]string{"hr"}, "all", "")
	if err != nil {
		t.Fatalf("ListQueries error: %v", err)
	}
	if len(hrQueries) != 1 || hrQueries[0].GroupName != "hr" {
		t.Fatalf("expected 1 hr query, got %d", len(hrQueries))
	}

	// CSVエクスポート
	var buf bytes.Buffer
	err = svc.ExportCSV([]string{"hr", "sales"}, "all", &buf)
	if err != nil {
		t.Fatalf("ExportCSV error: %v", err)
	}
	csvOutput := buf.String()
	if !strings.Contains(csvOutput, "HR質問1") || !strings.Contains(csvOutput, "Sales質問1") {
		t.Errorf("expected CSV to contain both queries, got:\n%s", csvOutput)
	}
}
