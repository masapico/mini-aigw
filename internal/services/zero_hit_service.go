package services

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"

	"gorm.io/gorm"
	"mini-aigw/internal/models"
)

type ZeroHitService struct {
	db *gorm.DB
}

func NewZeroHitService(db *gorm.DB) *ZeroHitService {
	return &ZeroHitService{db: db}
}

// RecordZeroHitAsync は RAG 検索でヒット 0 件またはスコア閾値未満となった質問を非同期で集約・保存します
func (s *ZeroHitService) RecordZeroHitAsync(query, groupName string, maxScore float64, reason, apiKeyName string) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return
	}
	if groupName == "" {
		groupName = "public"
	}
	if apiKeyName == "" {
		apiKeyName = "unknown"
	}

	go func() {
		hash := models.ComputeQueryHash(groupName, trimmed)
		now := time.Now()

		var existing models.ZeroHitQuery
		err := s.db.Where("normalized_query_hash = ?", hash).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			newRecord := models.ZeroHitQuery{
				Query:               trimmed,
				NormalizedQueryHash: hash,
				GroupName:           groupName,
				HitCount:            1,
				MaxScore:            maxScore,
				Reason:              reason,
				Status:              "unresolved",
				FirstOccurredAt:     now,
				LastOccurredAt:      now,
				LastApiKeyName:      apiKeyName,
			}
			_ = s.db.Create(&newRecord).Error
			return
		} else if err != nil {
			return
		}

		updates := map[string]interface{}{
			"hit_count":         gorm.Expr("hit_count + 1"),
			"last_occurred_at":  now,
			"last_api_key_name": apiKeyName,
			"reason":            reason,
		}
		// 解決済み（resolved）だったクエリが再度ヒット0件になった場合は未解決（unresolved）に復帰
		if existing.Status == "resolved" {
			updates["status"] = "unresolved"
		}
		// 対象外（ignored）の場合はステータス維持

		if maxScore > existing.MaxScore {
			updates["max_score"] = maxScore
		}

		_ = s.db.Model(&existing).Updates(updates).Error
	}()
}

// UpdateStatus はクエリのステータス（unresolved / resolved / ignored）を更新します
func (s *ZeroHitService) UpdateStatus(id string, status string) (*models.ZeroHitQuery, error) {
	if status != "unresolved" && status != "resolved" && status != "ignored" {
		return nil, fmt.Errorf("invalid status: %s", status)
	}

	var item models.ZeroHitQuery
	if err := s.db.First(&item, "id = ?", id).Error; err != nil {
		return nil, err
	}

	item.Status = status
	if err := s.db.Save(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// Delete はクエリを物理削除します
func (s *ZeroHitService) Delete(id string) error {
	return s.db.Delete(&models.ZeroHitQuery{}, "id = ?", id).Error
}

// ListQueries はグループ権限およびステータス条件に応じたクエリ一覧を返します
func (s *ZeroHitService) ListQueries(allowedGroups []string, statusFilter string, searchKeyword string) ([]models.ZeroHitQuery, error) {
	q := s.db.Model(&models.ZeroHitQuery{})
	if len(allowedGroups) > 0 {
		q = q.Where("group_name IN ?", allowedGroups)
	}

	if statusFilter != "" && statusFilter != "all" {
		q = q.Where("status = ?", statusFilter)
	} else if statusFilter == "" {
		// デフォルトは未解決のみ表示
		q = q.Where("status = ?", "unresolved")
	}

	if searchKeyword != "" {
		q = q.Where("query LIKE ?", "%"+searchKeyword+"%")
	}

	var results []models.ZeroHitQuery
	err := q.Order("hit_count desc, last_occurred_at desc").Find(&results).Error
	return results, err
}

// GetTopQueries はダッシュボード表示用の未解決クエリ上位リストを返します
func (s *ZeroHitService) GetTopQueries(allowedGroups []string, limit int) ([]models.ZeroHitQuery, error) {
	q := s.db.Where("status = ?", "unresolved")
	if len(allowedGroups) > 0 {
		q = q.Where("group_name IN ?", allowedGroups)
	}
	var results []models.ZeroHitQuery
	err := q.Order("hit_count desc, last_occurred_at desc").Limit(limit).Find(&results).Error
	return results, err
}

// GetUnresolvedCount は未解決クエリの総件数を返します
func (s *ZeroHitService) GetUnresolvedCount(allowedGroups []string) (int64, error) {
	q := s.db.Model(&models.ZeroHitQuery{}).Where("status = ?", "unresolved")
	if len(allowedGroups) > 0 {
		q = q.Where("group_name IN ?", allowedGroups)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

// ExportCSV はクエリ一覧を CSV 形式で出力します
func (s *ZeroHitService) ExportCSV(allowedGroups []string, statusFilter string, w io.Writer) error {
	queries, err := s.ListQueries(allowedGroups, statusFilter, "")
	if err != nil {
		return err
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()

	// ヘッダー行
	_ = cw.Write([]string{"ID", "質問クエリ", "グループ", "発生回数", "最高類似度スコア", "検知理由", "ステータス", "初回発生日時", "最終発生日時", "最終利用APIキー"})
	for _, q := range queries {
		statusLabel := "未解決"
		switch q.Status {
		case "resolved":
			statusLabel = "解決済"
		case "ignored":
			statusLabel = "対象外"
		}

		_ = cw.Write([]string{
			q.ID,
			q.Query,
			q.GroupName,
			fmt.Sprintf("%d", q.HitCount),
			fmt.Sprintf("%.2f", q.MaxScore),
			q.Reason,
			statusLabel,
			q.FirstOccurredAt.In(time.Local).Format("2006-01-02 15:04:05"),
			q.LastOccurredAt.In(time.Local).Format("2006-01-02 15:04:05"),
			q.LastApiKeyName,
		})
	}
	return nil
}

