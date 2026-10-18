package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
	"mini-aigw/internal/views"
)

type DashboardHandler struct {
	db       *gorm.DB
	cfg      *config.Config
	renderer *views.Renderer
}

func NewDashboardHandler(db *gorm.DB, cfg *config.Config, renderer *views.Renderer) *DashboardHandler {
	return &DashboardHandler{
		db:       db,
		cfg:      cfg,
		renderer: renderer,
	}
}

func (h *DashboardHandler) ShowDashboard(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	isAdmin := user.HasRole("admin")

	// 1. Doc count (Filtered by user groups if not admin)
	var docCount int64
	docQuery := h.db.Model(&models.KnowledgeDocument{})
	if !isAdmin {
		userGroups := append(user.GetGroups(), "public")
		docQuery = docQuery.Where("group_name IN ?", userGroups)
	}
	docQuery.Count(&docCount)

	// 2. Key count
	var keyCount int64
	h.db.Model(&models.ApiKey{}).Count(&keyCount)

	// 3. Models count & default model
	var activeModelsCount int64
	h.db.Model(&models.LLMModel{}).Where("is_active = ?", true).Count(&activeModelsCount)

	defaultModelName := h.cfg.DefaultModelID
	var defModel models.LLMModel
	if err := h.db.First(&defModel, "is_default = ?", true).Error; err == nil {
		defaultModelName = defModel.Name
	}

	// 4. Recent docs
	var recentDocs []models.KnowledgeDocument
	recentQuery := h.db.Order("created_at desc").Limit(5)
	if !isAdmin {
		userGroups := append(user.GetGroups(), "public")
		recentQuery = recentQuery.Where("group_name IN ?", userGroups)
	}
	recentQuery.Find(&recentDocs)

	// 5. Usage logs statistics & FinOps (Admin only)
	var totalRequests, ragRequests, totalTokens int64
	var totalCostUSD float64
	var recentUsageLogs []models.ChatUsageLog
	var apiKeyStats []ApiKeyUsageStat
	var modelStats []ModelUsageStat
	var groupStats []GroupUsageStat
	var dailyStats []DailyUsageStat
	var dailyStatsJSON string = "[]"

	if isAdmin {
		h.db.Model(&models.ChatUsageLog{}).Count(&totalRequests)
		h.db.Model(&models.ChatUsageLog{}).Where("rag_triggered = ?", true).Count(&ragRequests)

		type Sums struct {
			Tokens int64
			Cost   float64
		}
		var sums Sums
		h.db.Model(&models.ChatUsageLog{}).Select("COALESCE(SUM(total_tokens), 0) as tokens, COALESCE(SUM(estimated_cost), 0.0) as cost").Scan(&sums)
		totalTokens = sums.Tokens
		totalCostUSD = sums.Cost

		// Recent logs
		h.db.Order("created_at desc").Limit(10).Find(&recentUsageLogs)

		// 5.1 API Key usage stats
		h.db.Model(&models.ChatUsageLog{}).
			Select("api_key_name, COUNT(*) as request_count, COALESCE(SUM(total_tokens), 0) as total_tokens, COALESCE(SUM(prompt_tokens), 0) as prompt_tokens, COALESCE(SUM(completion_tokens), 0) as completion_tokens, COALESCE(SUM(estimated_cost), 0.0) as total_cost, COALESCE(strftime('%Y-%m-%d %H:%M:%S', MAX(created_at)), '-') as last_used_at").
			Group("api_key_name").
			Order("total_cost desc, total_tokens desc").
			Scan(&apiKeyStats)

		// 5.2 Model usage stats
		h.db.Model(&models.ChatUsageLog{}).
			Select("model_id, COUNT(*) as request_count, COALESCE(SUM(total_tokens), 0) as total_tokens, COALESCE(SUM(estimated_cost), 0.0) as total_cost").
			Group("model_id").
			Order("total_cost desc").
			Scan(&modelStats)

		// 5.3 Group stats (from knowledge_documents and cited groups)
		type GCount struct {
			GroupName string
			Count     int64
		}
		var gCounts []GCount
		h.db.Model(&models.KnowledgeDocument{}).
			Select("group_name, count(*) as count").
			Group("group_name").
			Scan(&gCounts)

		for _, gc := range gCounts {
			groupStats = append(groupStats, GroupUsageStat{
				GroupName: gc.GroupName,
				DocCount:  gc.Count,
			})
		}

		// 5.4 Daily stats for the past 14 days (Chart.js)
		now := time.Now()
		fourteenDaysAgo := now.AddDate(0, 0, -13)
		startDateStr := fourteenDaysAgo.Format("2006-01-02")

		type DailyRow struct {
			Day          string
			RequestCount int64
			Tokens       int64
			Cost         float64
		}
		var dailyRows []DailyRow
		h.db.Model(&models.ChatUsageLog{}).
			Select("strftime('%Y-%m-%d', created_at) as day, count(*) as request_count, COALESCE(SUM(total_tokens), 0) as tokens, COALESCE(SUM(estimated_cost), 0.0) as cost").
			Where("created_at >= ?", startDateStr+" 00:00:00").
			Group("strftime('%Y-%m-%d', created_at)").
			Order("day asc").
			Scan(&dailyRows)

		dayMap := make(map[string]DailyRow)
		for _, r := range dailyRows {
			dayMap[r.Day] = r
		}

		for i := 13; i >= 0; i-- {
			d := now.AddDate(0, 0, -i).Format("2006-01-02")
			displayDate := now.AddDate(0, 0, -i).Format("01/02")
			if row, exists := dayMap[d]; exists {
				dailyStats = append(dailyStats, DailyUsageStat{
					Date:         displayDate,
					RequestCount: row.RequestCount,
					TotalTokens:  row.Tokens,
					TotalCost:    row.Cost,
				})
			} else {
				dailyStats = append(dailyStats, DailyUsageStat{
					Date:         displayDate,
					RequestCount: 0,
					TotalTokens:  0,
					TotalCost:    0.0,
				})
			}
		}

		if b, err := json.Marshal(dailyStats); err == nil {
			dailyStatsJSON = string(b)
		}
	}

	data := map[string]interface{}{
		"User":              user,
		"IsAdmin":           isAdmin,
		"CurrentPath":       "/",
		"DocCount":          docCount,
		"KeyCount":          keyCount,
		"ActiveModelsCount": activeModelsCount,
		"DefaultModelName":  defaultModelName,
		"RecentDocs":        recentDocs,
		"TotalRequests":     totalRequests,
		"RAGRequests":       ragRequests,
		"TotalTokens":       totalTokens,
		"TotalCostUSD":      totalCostUSD,
		"RecentUsageLogs":   recentUsageLogs,
		"ApiKeyStats":       apiKeyStats,
		"ModelStats":        modelStats,
		"GroupStats":        groupStats,
		"DailyStatsJSON":    dailyStatsJSON,
		"Config":            h.cfg,
	}

	_ = h.renderer.Render(w, "dashboard.html", data)
}

// ExportCSV exports usage logs as a downloadable CSV for internal billing/accounting
func (h *DashboardHandler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil || !user.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var logs []models.ChatUsageLog
	h.db.Order("created_at desc").Limit(5000).Find(&logs)

	filename := fmt.Sprintf("mini_aigw_usage_%s.csv", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	// UTF-8 BOM for Microsoft Excel
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Header
	_ = writer.Write([]string{
		"LogID",
		"Timestamp",
		"ApiKeyName",
		"ModelID",
		"IsStream",
		"RAGTriggered",
		"PromptTokens",
		"CompletionTokens",
		"TotalTokens",
		"EstimatedCostUSD",
		"LatencyMs",
		"TimeToFirstTokenMs",
		"Status",
	})

	for _, l := range logs {
		_ = writer.Write([]string{
			l.ID,
			l.CreatedAt.Format("2006-01-02 15:04:05"),
			l.ApiKeyName,
			l.ModelID,
			fmt.Sprintf("%v", l.IsStream),
			fmt.Sprintf("%v", l.RAGTriggered),
			fmt.Sprintf("%d", l.PromptTokens),
			fmt.Sprintf("%d", l.CompletionTokens),
			fmt.Sprintf("%d", l.TotalTokens),
			fmt.Sprintf("%.6f", l.EstimatedCost),
			fmt.Sprintf("%d", l.LatencyMs),
			fmt.Sprintf("%d", l.TimeToFirstTokenMs),
			l.Status,
		})
	}
}

type ApiKeyUsageStat struct {
	ApiKeyName       string
	RequestCount     int64
	TotalTokens      int64
	PromptTokens     int64
	CompletionTokens int64
	TotalCost        float64
	LastUsedAt       string
}

type ModelUsageStat struct {
	ModelID      string
	RequestCount int64
	TotalTokens  int64
	TotalCost    float64
}

type GroupUsageStat struct {
	GroupName string
	DocCount  int64
}

type DailyUsageStat struct {
	Date         string  `json:"date"`
	RequestCount int64   `json:"requests"`
	TotalTokens  int64   `json:"tokens"`
	TotalCost    float64 `json:"cost"`
}

