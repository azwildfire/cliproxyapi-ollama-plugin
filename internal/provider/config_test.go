package provider

import "testing"

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig(nil): %v", err)
	}
	if cfg.BaseURL != DefaultBaseURL || cfg.UsageRange != "7d" || !cfg.Enabled || !cfg.modelDetails() {
		t.Fatalf("unexpected defaults %+v", cfg)
	}
}

func TestParseConfigIgnoresHostKeys(t *testing.T) {
	raw := []byte("enabled: true\npriority: 100\nstore:\n  id: cliproxyapi-ollama\n  name: x\nusage_range: 30d\nexcluded_models:\n  - \"GLM-*\"\n  - \"\"\nmodel_details: false\n")
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.UsageRange != "30d" || len(cfg.ExcludedModels) != 1 || cfg.ExcludedModels[0] != "glm-*" || cfg.modelDetails() {
		t.Fatalf("unexpected config %+v", cfg)
	}
}

func TestParseConfigValidation(t *testing.T) {
	for name, raw := range map[string]string{
		"range":    "usage_range: 1d\n",
		"http":     "base_url: http://ollama.local\n",
		"timeout":  "login_timeout_seconds: 10\n",
		"modelttl": "model_cache_ttl_seconds: 1\n",
	} {
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	if _, err := ParseConfig([]byte("base_url: http://127.0.0.1:11434\nallow_insecure_base_url: true\n")); err != nil {
		t.Fatalf("insecure base URL should be allowed when opted in: %v", err)
	}
}
