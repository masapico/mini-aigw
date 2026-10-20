package models

import (
	"time"
)

type LLMModel struct {
	ID                  string    `gorm:"primaryKey;size:100" json:"id"`
	Name                string    `gorm:"size:100;not null" json:"name"`
	Description         *string   `gorm:"size:255" json:"description"`
	Provider            string    `gorm:"size:50;not null;default:'bedrock'" json:"provider"`
	InputPricePer1M     float64   `gorm:"column:input_price_per_1m;not null;default:0.0" json:"input_price_per_1m"`
	OutputPricePer1M    float64   `gorm:"column:output_price_per_1m;not null;default:0.0" json:"output_price_per_1m"`
	SupportsTemperature *bool     `gorm:"column:supports_temperature;not null;default:true" json:"supports_temperature"`
	IsActive            bool      `gorm:"not null;default:true" json:"is_active"`
	IsDefault           bool      `gorm:"not null;default:false" json:"is_default"`
	CreatedAt           time.Time `json:"created_at"`
}

func (LLMModel) TableName() string {
	return "llm_models"
}

// IsTemperatureSupported returns true if sampling parameters (temperature, top_p) should be passed.
func (m LLMModel) IsTemperatureSupported() bool {
	if m.SupportsTemperature == nil {
		return true
	}
	return *m.SupportsTemperature
}

// BoolPtr is a helper to return a pointer to a bool.
func BoolPtr(b bool) *bool {
	return &b
}
