package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
	"mini-aigw/internal/prompts"
	"mini-aigw/internal/views"
)

type APIKeysHandler struct {
	db       *gorm.DB
	cfg      *config.Config
	renderer *views.Renderer
}

func NewAPIKeysHandler(db *gorm.DB, cfg *config.Config, renderer *views.Renderer) *APIKeysHandler {
	return &APIKeysHandler{
		db:       db,
		cfg:      cfg,
		renderer: renderer,
	}
}

func (h *APIKeysHandler) getAvailableGroupsAndModels() ([]string, []models.LLMModel) {
	var allGroups []models.Group
	h.db.Order("name asc").Find(&allGroups)
	groupMap := make(map[string]bool)
	groupMap["public"] = true
	availableGroups := []string{"public"}
	for _, g := range allGroups {
		if !groupMap[g.Name] {
			groupMap[g.Name] = true
			availableGroups = append(availableGroups, g.Name)
		}
	}

	var availableModels []models.LLMModel
	h.db.Where("is_active = ?", true).Order("name asc").Find(&availableModels)
	return availableGroups, availableModels
}

func (h *APIKeysHandler) ShowAPIKeys(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	var keys []models.ApiKey
	h.db.Order("created_at desc").Find(&keys)

	availableGroups, availableModels := h.getAvailableGroupsAndModels()

	data := map[string]interface{}{
		"User":                   user,
		"IsAdmin":                true,
		"CurrentPath":            "/api-keys",
		"Keys":                   keys,
		"AvailableGroups":        availableGroups,
		"AvailableModels":        availableModels,
		"BuiltinKnowledgePrompt": prompts.KnowledgeToolSystemPrompt,
		"BuiltinGroundingRules":  prompts.KnowledgeGroundingRulesSummary,
	}

	_ = h.renderer.Render(w, "api_keys.html", data)
}

func (h *APIKeysHandler) CreateKey(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil || !user.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	modelID := strings.TrimSpace(r.FormValue("model_id"))
	var modelIDPtr *string
	if modelID != "" {
		modelIDPtr = &modelID
	}

	systemPrompt := strings.TrimSpace(r.FormValue("system_prompt"))
	var systemPromptPtr *string
	if systemPrompt != "" {
		systemPromptPtr = &systemPrompt
	}

	rawAllowedGroups := r.Form["allowed_groups"]
	var cleanGroups []string
	for _, g := range rawAllowedGroups {
		trimmed := strings.TrimSpace(g)
		if trimmed != "" {
			cleanGroups = append(cleanGroups, trimmed)
		}
	}
	if cleanGroups == nil {
		cleanGroups = []string{}
	}

	// Generate sk-live-...
	randBytes := make([]byte, 24)
	_, _ = rand.Read(randBytes)
	secretPart := hex.EncodeToString(randBytes)
	rawKey := fmt.Sprintf("sk-live-%s", secretPart)
	keyHash := auth.HashAPIKey(rawKey)
	prefix := fmt.Sprintf("sk-live-%s...", secretPart[:6])

	key := models.ApiKey{
		UserID:       &user.ID,
		Name:         name,
		KeyHash:      keyHash,
		Prefix:       prefix,
		RawKey:       &rawKey,
		ModelID:      modelIDPtr,
		SystemPrompt: systemPromptPtr,
	}
	key.SetAllowedGroups(cleanGroups)

	if err := h.db.Create(&key).Error; err != nil {
		http.Error(w, fmt.Sprintf("Failed to create key: %v", err), http.StatusInternalServerError)
		return
	}

	var keys []models.ApiKey
	h.db.Order("created_at desc").Find(&keys)

	availableGroups, availableModels := h.getAvailableGroupsAndModels()

	data := map[string]interface{}{
		"User":                   user,
		"IsAdmin":                true,
		"Keys":                   keys,
		"AvailableGroups":        availableGroups,
		"AvailableModels":        availableModels,
		"NewRawKey":              rawKey,
		"NewKeyName":             name,
		"BuiltinKnowledgePrompt": prompts.KnowledgeToolSystemPrompt,
		"BuiltinGroundingRules":  prompts.KnowledgeGroundingRulesSummary,
	}
	_ = h.renderer.RenderPartial(w, "api_keys_section.html", data)
}

func (h *APIKeysHandler) EditKey(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil || !user.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	keyID := chi.URLParam(r, "id")
	var key models.ApiKey
	if err := h.db.First(&key, "id = ?", keyID).Error; err != nil {
		http.Error(w, "API Key not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	modelID := strings.TrimSpace(r.FormValue("model_id"))
	var modelIDPtr *string
	if modelID != "" {
		modelIDPtr = &modelID
	}

	systemPrompt := strings.TrimSpace(r.FormValue("system_prompt"))
	var systemPromptPtr *string
	if systemPrompt != "" {
		systemPromptPtr = &systemPrompt
	}

	rawAllowedGroups := r.Form["allowed_groups"]
	var cleanGroups []string
	for _, g := range rawAllowedGroups {
		trimmed := strings.TrimSpace(g)
		if trimmed != "" {
			cleanGroups = append(cleanGroups, trimmed)
		}
	}
	if cleanGroups == nil {
		cleanGroups = []string{}
	}

	key.Name = name
	key.ModelID = modelIDPtr
	key.SystemPrompt = systemPromptPtr
	key.SetAllowedGroups(cleanGroups)

	if err := h.db.Save(&key).Error; err != nil {
		http.Error(w, fmt.Sprintf("Failed to update key: %v", err), http.StatusInternalServerError)
		return
	}

	var keys []models.ApiKey
	h.db.Order("created_at desc").Find(&keys)

	availableGroups, availableModels := h.getAvailableGroupsAndModels()

	data := map[string]interface{}{
		"User":                   user,
		"IsAdmin":                true,
		"Keys":                   keys,
		"AvailableGroups":        availableGroups,
		"AvailableModels":        availableModels,
		"NewRawKey":              "",
		"BuiltinKnowledgePrompt": prompts.KnowledgeToolSystemPrompt,
		"BuiltinGroundingRules":  prompts.KnowledgeGroundingRulesSummary,
	}
	_ = h.renderer.RenderPartial(w, "api_keys_section.html", data)
}

func (h *APIKeysHandler) DeleteKey(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil || !user.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	keyID := chi.URLParam(r, "id")
	if err := h.db.Delete(&models.ApiKey{}, "id = ?", keyID).Error; err != nil {
		http.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
