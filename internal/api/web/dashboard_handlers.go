package web

import (
	"net/http"

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

	// 5. Usage logs statistics (Admin only)
	var totalRequests, ragRequests, totalTokens int64
	var recentUsageLogs []models.ChatUsageLog

	if isAdmin {
		h.db.Model(&models.ChatUsageLog{}).Count(&totalRequests)
		h.db.Model(&models.ChatUsageLog{}).Where("rag_triggered = ?", true).Count(&ragRequests)

		type TokenSum struct {
			Sum int64
		}
		var ts TokenSum
		h.db.Model(&models.ChatUsageLog{}).Select("COALESCE(SUM(total_tokens), 0) as sum").Scan(&ts)
		totalTokens = ts.Sum

		h.db.Order("created_at desc").Limit(10).Find(&recentUsageLogs)
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
		"RecentUsageLogs":   recentUsageLogs,
		"Config":            h.cfg,
	}

	_ = h.renderer.Render(w, "dashboard.html", data)
}

