package config

import "testing"

func TestCostBudgetRequiresFallbackModelPrices(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.Review.MaxRoundCost = 1
	cfg.LLM.Prices = map[string]ModelPrice{}
	for _, name := range cfg.LLM.Models {
		cfg.LLM.Prices[name] = ModelPrice{InputPerMillion: 1, OutputPerMillion: 1}
	}
	cfg.LLM.Fallback = &FallbackConfig{Enabled: true, APIKey: "mock", BaseURL: "http://localhost", Models: map[string]string{"small": "fallback", "medium": "fallback", "large": "fallback"}}
	if cfg.ValidateRuntime() == nil {
		t.Fatal("enabled fallback has no price but accepted cost budget")
	}
}
