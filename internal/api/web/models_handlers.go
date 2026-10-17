package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/models"
	"mini-aigw/internal/views"
)

type WebModelsHandler struct {
	db       *gorm.DB
	renderer *views.Renderer
}

func NewWebModelsHandler(db *gorm.DB, renderer *views.Renderer) *WebModelsHandler {
	return &WebModelsHandler{db: db, renderer: renderer}
}

func (h *WebModelsHandler) ShowModels(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	var allModels []models.LLMModel
	h.db.Order("created_at asc").Find(&allModels)

	data := map[string]interface{}{
		"User":        user,
		"IsAdmin":     true,
		"CurrentPath": "/models",
		"Models":      allModels,
	}

	_ = h.renderer.Render(w, "models.html", data)
}

func (h *WebModelsHandler) CreateModel(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil || !user.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	modelID := strings.TrimSpace(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	provider := strings.TrimSpace(r.FormValue("provider"))
	if provider == "" {
		provider = "bedrock"
	}
	desc := strings.TrimSpace(r.FormValue("description"))
	var descPtr *string
	if desc != "" {
		descPtr = &desc
	}

	if modelID == "" || name == "" {
		h.renderSectionWithError(w, "モデルIDと表示名は必須です")
		return
	}

	var count int64
	h.db.Model(&models.LLMModel{}).Where("id = ?", modelID).Count(&count)
	if count > 0 {
		h.renderSectionWithError(w, fmt.Sprintf("モデルID '%s' は既に登録されています", modelID))
		return
	}

	m := models.LLMModel{
		ID:          modelID,
		Name:        name,
		Provider:    provider,
		Description: descPtr,
		IsActive:    true,
	}
	if err := h.db.Create(&m).Error; err != nil {
		h.renderSectionWithError(w, fmt.Sprintf("登録失敗: %v", err))
		return
	}

	h.renderSectionWithSuccess(w, fmt.Sprintf("モデル「%s」を登録しました", name))
}

func (h *WebModelsHandler) ToggleModel(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "id")
	var m models.LLMModel
	if err := h.db.First(&m, "id = ?", modelID).Error; err != nil {
		h.renderSectionWithError(w, "モデルが見つかりません")
		return
	}

	m.IsActive = !m.IsActive
	h.db.Save(&m)

	statusStr := "有効化"
	if !m.IsActive {
		statusStr = "無効化"
	}
	h.renderSectionWithSuccess(w, fmt.Sprintf("モデル「%s」を%sしました", m.Name, statusStr))
}

func (h *WebModelsHandler) SetDefaultModel(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "id")

	// Reset all models to not default
	h.db.Model(&models.LLMModel{}).Where("1 = 1").Update("is_default", false)

	// Set selected model as default and active
	var m models.LLMModel
	if err := h.db.First(&m, "id = ?", modelID).Error; err != nil {
		h.renderSectionWithError(w, "モデルが見つかりません")
		return
	}

	m.IsDefault = true
	m.IsActive = true
	h.db.Save(&m)

	h.renderSectionWithSuccess(w, fmt.Sprintf("モデル「%s」を既定モデルに設定しました", m.Name))
}

func (h *WebModelsHandler) DeleteModel(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "id")
	if err := h.db.Delete(&models.LLMModel{}, "id = ?", modelID).Error; err != nil {
		http.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *WebModelsHandler) renderSectionWithError(w http.ResponseWriter, errMsg string) {
	var allModels []models.LLMModel
	h.db.Order("created_at asc").Find(&allModels)
	data := map[string]interface{}{
		"Models": allModels,
		"Error":  errMsg,
	}
	_ = h.renderer.RenderPartial(w, "models_section.html", data)
}

func (h *WebModelsHandler) renderSectionWithSuccess(w http.ResponseWriter, successMsg string) {
	var allModels []models.LLMModel
	h.db.Order("created_at asc").Find(&allModels)
	data := map[string]interface{}{
		"Models":  allModels,
		"Success": successMsg,
	}
	_ = h.renderer.RenderPartial(w, "models_section.html", data)
}

