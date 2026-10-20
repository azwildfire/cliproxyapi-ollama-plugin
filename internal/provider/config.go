package provider

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults exposed to the registration metadata so the management UI can show them.
const (
	DefaultBaseURL              = "https://ollama.com"
	DefaultLoginTimeoutSeconds  = 900
	DefaultModelCacheTTLSeconds = 600
	DefaultQuotaCacheSeconds    = 30
	DefaultUsageRange           = "7d"
)

// Config is the plugin-owned section under plugins.configs.cliproxyapi-ollama.
type Config struct {
	Enabled              bool     `yaml:"enabled"`
	BaseURL              string   `yaml:"base_url"`
	DeviceName           string   `yaml:"device_name"`
	LoginTimeoutSeconds  int      `yaml:"login_timeout_seconds"`
	ModelCacheTTLSeconds int      `yaml:"model_cache_ttl_seconds"`
	ModelDetails         *bool    `yaml:"model_details"`
	QuotaCacheSeconds    int      `yaml:"quota_cache_seconds"`
	UsageRange           string   `yaml:"usage_range"`
	ExcludedModels       []string `yaml:"excluded_models"`
	AllowInsecureBaseURL bool     `yaml:"allow_insecure_base_url"`
}

// DefaultConfig returns the configuration used when keys are omitted.
func DefaultConfig() Config {
	enabled := true
	return Config{
		Enabled:              true,
		BaseURL:              DefaultBaseURL,
		LoginTimeoutSeconds:  DefaultLoginTimeoutSeconds,
		ModelCacheTTLSeconds: DefaultModelCacheTTLSeconds,
		ModelDetails:         &enabled,
		QuotaCacheSeconds:    DefaultQuotaCacheSeconds,
		UsageRange:           DefaultUsageRange,
	}
}

// ParseConfig decodes the plugin YAML block on top of the defaults and validates it.
func ParseConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return Config{}, fmt.Errorf("decode plugin config: %w", errUnmarshal)
		}
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.DeviceName = strings.TrimSpace(cfg.DeviceName)
	cfg.UsageRange = strings.ToLower(strings.TrimSpace(cfg.UsageRange))
	if cfg.UsageRange == "" {
		cfg.UsageRange = DefaultUsageRange
	}
	cfg.ExcludedModels = normalizePatterns(cfg.ExcludedModels)
	if cfg.ModelDetails == nil {
		enabled := true
		cfg.ModelDetails = &enabled
	}
	if errURL := validateBaseURL(cfg.BaseURL, cfg.AllowInsecureBaseURL); errURL != nil {
		return Config{}, fmt.Errorf("base_url: %w", errURL)
	}
	switch cfg.UsageRange {
	case "24h", "7d", "30d":
	default:
		return Config{}, fmt.Errorf("usage_range must be one of 24h, 7d, 30d")
	}
	if cfg.LoginTimeoutSeconds < 60 || cfg.LoginTimeoutSeconds > 3600 {
		return Config{}, fmt.Errorf("login_timeout_seconds must be between 60 and 3600")
	}
	if cfg.ModelCacheTTLSeconds < 30 || cfg.ModelCacheTTLSeconds > 86400 {
		return Config{}, fmt.Errorf("model_cache_ttl_seconds must be between 30 and 86400")
	}
	if cfg.QuotaCacheSeconds < 0 || cfg.QuotaCacheSeconds > 3600 {
		return Config{}, fmt.Errorf("quota_cache_seconds must be between 0 and 3600")
	}
	return cfg, nil
}

func normalizePatterns(patterns []string) []string {
	seen := make(map[string]struct{}, len(patterns))
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if _, exists := seen[pattern]; exists {
			continue
		}
		seen[pattern] = struct{}{}
		out = append(out, pattern)
	}
	return out
}

func validateBaseURL(raw string, allowInsecure bool) error {
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Hostname() == "" {
		return fmt.Errorf("invalid absolute URL")
	}
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return fmt.Errorf("URL must use HTTPS")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("base URL must not contain query or fragment")
	}
	return nil
}

func (c Config) loginTimeout() time.Duration {
	return time.Duration(c.LoginTimeoutSeconds) * time.Second
}

func (c Config) modelCacheTTL() time.Duration {
	return time.Duration(c.ModelCacheTTLSeconds) * time.Second
}

func (c Config) quotaCacheTTL() time.Duration {
	return time.Duration(c.QuotaCacheSeconds) * time.Second
}

func (c Config) modelDetails() bool {
	return c.ModelDetails == nil || *c.ModelDetails
}
