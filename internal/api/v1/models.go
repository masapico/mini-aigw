package v1

import (
	"encoding/json"
	"net/http"
	"time"

	"gorm.io/gorm"

	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

type ModelsHandler struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewModelsHandler(db *gorm.DB, cfg *config.Config) *ModelsHandler {
	return &ModelsHandler{db: db, cfg: cfg}
}

type ModelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelListResponse struct {
	Object string      `json:"object"`
	Data   []ModelItem `json:"data"`
}

func (h *ModelsHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()

	var dbModels []models.LLMModel
	h.db.Where("is_active = ?", true).Find(&dbModels)

	var data []ModelItem
	if len(dbModels) > 0 {
		for _, m := range dbModels {
			data = append(data, ModelItem{
				ID:      m.ID,
				Object:  "model",
				Created: m.CreatedAt.Unix(),
				OwnedBy: m.Provider,
			})
		}
	} else {
		for _, modelID := range h.cfg.AvailableModels {
			data = append(data, ModelItem{
				ID:      modelID,
				Object:  "model",
				Created: now,
				OwnedBy: "bedrock",
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ModelListResponse{
		Object: "list",
		Data:   data,
	})
}

