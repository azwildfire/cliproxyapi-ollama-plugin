package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/redact"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	showCacheTTL       = time.Hour
	showEnrichBudget   = 15 * time.Second
	capabilityComplete = "completion"
	capabilityTools    = "tools"
	capabilityVision   = "vision"
	capabilityThinking = "thinking"
)

type tagsResponse struct {
	Models []tagModel `json:"models"`
}

type tagModel struct {
	Name         string       `json:"name"`
	Model        string       `json:"model"`
	ModifiedAt   string       `json:"modified_at"`
	Size         int64        `json:"size"`
	Digest       string       `json:"digest"`
	Details      modelDetails `json:"details"`
	Capabilities []string     `json:"capabilities"`
}

type modelDetails struct {
	Family            string `json:"family"`
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
	ContextLength     int64  `json:"context_length"`
}

type showResponse struct {
	Capabilities []string       `json:"capabilities"`
	Details      modelDetails   `json:"details"`
	ModelInfo    map[string]any `json:"model_info"`
	Thinking     *struct {
		// Values mixes strings with a boolean false ("thinking can be turned off"), for example
		// [false, "low", "high", "max"], so it is decoded loosely.
		Values  []any `json:"values"`
		Default any   `json:"default"`
	} `json:"thinking"`
}

// modelDetailInfo is what /api/show adds on top of /api/tags.
type modelDetailInfo struct {
	ContextLength  int64
	ThinkingLevels []string
	ThinkingDefault string
}

type modelCacheEntry struct {
	expiresAt time.Time
	identity  string
	models    []pluginapi.ModelInfo
}

type showCacheEntry struct {
	expiresAt time.Time
	info      modelDetailInfo
	ok        bool
}

// SupportedFormats lists the request protocols ollama.com serves natively; the host translates
// everything else (for example Gemini) into one of these.
func SupportedFormats() []string {
	return []string{"openai", "claude", "openai-response"}
}

// StaticModels is empty: Ollama Cloud models are discovered per credential.
func (s *Service) StaticModels() pluginapi.ModelResponse {
	return pluginapi.ModelResponse{Provider: ProviderID, Models: []pluginapi.ModelInfo{}}
}

// ModelsForAuth lists the cloud catalog visible to a credential.
func (s *Service) ModelsForAuth(ctx context.Context, callbackID string, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.ModelResponse{}, errParse
	}
	creds, errCreds := s.credentialsFor(storage)
	if errCreds != nil {
		return pluginapi.ModelResponse{}, errCreds
	}
	cfg := s.Config()
	excluded := append(append([]string(nil), cfg.ExcludedModels...), normalizePatterns(req.Host.ExcludedModels[ProviderID])...)
	models, errModels := s.models(ctx, callbackID, req.AuthID, creds, excluded)
	if errModels != nil {
		return pluginapi.ModelResponse{}, errModels
	}
	return pluginapi.ModelResponse{Provider: ProviderID, Models: models}, nil
}

func (s *Service) models(ctx context.Context, callbackID, authID string, creds credentials, excluded []string) ([]pluginapi.ModelInfo, error) {
	identity := creds.identity()
	cacheKey := firstNonEmpty(authID, identity)
	now := s.now()
	s.modelMu.Lock()
	cached, ok := s.modelEntries[cacheKey]
	s.modelMu.Unlock()
	if ok && cached.identity == identity && cached.expiresAt.After(now) {
		return cloneModelInfos(cached.models), nil
	}

	resp, errCall := s.call(ctx, s.host, callbackID, creds, apiCall{method: http.MethodGet, path: "/api/tags"})
	if errCall != nil {
		return nil, fmt.Errorf("discover Ollama Cloud models: %w", errCall)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, creds.secrets()...))
	}
	var tags tagsResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &tags); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Ollama Cloud model list: %w", errUnmarshal)
	}
	selected := selectModels(tags.Models, excluded)
	if len(selected) == 0 {
		return nil, fmt.Errorf("Ollama Cloud returned no usable chat models for this credential")
	}

	details := make(map[string]modelDetailInfo, len(selected))
	if s.Config().modelDetails() {
		details = s.enrichModels(ctx, callbackID, creds, selected)
	}
	infos := make([]pluginapi.ModelInfo, 0, len(selected))
	for _, model := range selected {
		infos = append(infos, modelInfo(model, details[model.Name]))
	}

	s.modelMu.Lock()
	s.modelEntries[cacheKey] = modelCacheEntry{
		expiresAt: now.Add(s.Config().modelCacheTTL()),
		identity:  identity,
		models:    cloneModelInfos(infos),
	}
	s.modelMu.Unlock()
	return infos, nil
}

// selectModels keeps chat-capable models, drops excluded names, dedupes, and sorts.
func selectModels(models []tagModel, excluded []string) []tagModel {
	seen := make(map[string]struct{}, len(models))
	out := make([]tagModel, 0, len(models))
	for _, model := range models {
		model.Name = strings.TrimSpace(firstNonEmpty(model.Name, model.Model))
		if model.Name == "" {
			continue
		}
		key := strings.ToLower(model.Name)
		if _, exists := seen[key]; exists {
			continue
		}
		if !hasCapability(model.Capabilities, capabilityComplete) {
			continue
		}
		if modelExcluded(key, excluded) {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func modelExcluded(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if pattern == name {
			return true
		}
		if strings.ContainsAny(pattern, "*?[") {
			if matched, errMatch := path.Match(pattern, name); errMatch == nil && matched {
				return true
			}
		}
	}
	return false
}

func hasCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), want) {
			return true
		}
	}
	return false
}

// enrichModels calls /api/show sequentially (the host callback is used one request at a time)
// with a time budget, caching results so refreshes stay cheap.
func (s *Service) enrichModels(ctx context.Context, callbackID string, creds credentials, models []tagModel) map[string]modelDetailInfo {
	out := make(map[string]modelDetailInfo, len(models))
	deadline := s.now().Add(showEnrichBudget)
	for _, model := range models {
		cacheKey := model.Name + "@" + model.Digest
		s.modelMu.Lock()
		cached, ok := s.showEntries[cacheKey]
		s.modelMu.Unlock()
		if ok && cached.expiresAt.After(s.now()) {
			if cached.ok {
				out[model.Name] = cached.info
			}
			continue
		}
		if !s.now().Before(deadline) || ctx.Err() != nil {
			continue
		}
		info, errShow := s.showModel(ctx, callbackID, creds, model.Name)
		s.modelMu.Lock()
		s.showEntries[cacheKey] = showCacheEntry{expiresAt: s.now().Add(showCacheTTL), info: info, ok: errShow == nil}
		s.modelMu.Unlock()
		if errShow == nil {
			out[model.Name] = info
		}
	}
	return out
}

func (s *Service) showModel(ctx context.Context, callbackID string, creds credentials, name string) (modelDetailInfo, error) {
	body, _ := json.Marshal(map[string]string{"model": name})
	resp, errCall := s.call(ctx, s.host, callbackID, creds, apiCall{method: http.MethodPost, path: "/api/show", body: body})
	if errCall != nil {
		return modelDetailInfo{}, errCall
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modelDetailInfo{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, creds.secrets()...))
	}
	var show showResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &show); errUnmarshal != nil {
		return modelDetailInfo{}, fmt.Errorf("decode /api/show response: %w", errUnmarshal)
	}
	info := modelDetailInfo{ContextLength: show.Details.ContextLength}
	for key, value := range show.ModelInfo {
		if !strings.HasSuffix(key, ".context_length") {
			continue
		}
		if number, ok := value.(float64); ok && number > 0 {
			info.ContextLength = int64(number)
		}
	}
	if show.Thinking != nil {
		for _, value := range show.Thinking.Values {
			if level, ok := value.(string); ok && strings.TrimSpace(level) != "" {
				info.ThinkingLevels = append(info.ThinkingLevels, strings.TrimSpace(level))
			}
		}
		if level, ok := show.Thinking.Default.(string); ok {
			info.ThinkingDefault = strings.TrimSpace(level)
		}
	}
	return info, nil
}

func modelInfo(model tagModel, detail modelDetailInfo) pluginapi.ModelInfo {
	contextLength := detail.ContextLength
	if contextLength == 0 {
		contextLength = model.Details.ContextLength
	}
	parameters := []string{"stream", "temperature", "top_p", "max_tokens"}
	if hasCapability(model.Capabilities, capabilityTools) {
		parameters = append(parameters, "tools", "tool_choice")
	}
	inputModalities := []string{"TEXT"}
	if hasCapability(model.Capabilities, capabilityVision) {
		inputModalities = append(inputModalities, "IMAGE")
	}
	var thinking *pluginapi.ThinkingSupport
	if hasCapability(model.Capabilities, capabilityThinking) {
		parameters = append(parameters, "reasoning_effort")
		thinking = &pluginapi.ThinkingSupport{
			ZeroAllowed:    true,
			DynamicAllowed: false,
			Levels:         append([]string(nil), detail.ThinkingLevels...),
		}
	}
	return pluginapi.ModelInfo{
		ID:                         model.Name,
		Object:                     "model",
		Created:                    modifiedUnix(model.ModifiedAt),
		OwnedBy:                    "ollama",
		Type:                       "chat",
		DisplayName:                model.Name,
		Name:                       model.Name,
		Description:                modelDescription(model, detail),
		InputTokenLimit:            contextLength,
		SupportedGenerationMethods: []string{"chat.completions", "messages", "responses"},
		ContextLength:              contextLength,
		SupportedParameters:        parameters,
		SupportedInputModalities:   inputModalities,
		SupportedOutputModalities:  []string{"TEXT"},
		Thinking:                   thinking,
	}
}

func modelDescription(model tagModel, detail modelDetailInfo) string {
	parts := []string{"Ollama Cloud model"}
	if family := strings.TrimSpace(model.Details.Family); family != "" {
		parts = append(parts, "family "+family)
	}
	if len(model.Capabilities) > 0 {
		parts = append(parts, "capabilities "+strings.Join(model.Capabilities, ", "))
	}
	if len(detail.ThinkingLevels) > 0 {
		parts = append(parts, "thinking levels "+strings.Join(detail.ThinkingLevels, "/"))
	}
	return strings.Join(parts, "; ")
}

func modifiedUnix(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, errParse := time.Parse(layout, value); errParse == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func cloneModelInfos(in []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, len(in))
	copy(out, in)
	for index := range out {
		out[index].SupportedGenerationMethods = append([]string(nil), in[index].SupportedGenerationMethods...)
		out[index].SupportedParameters = append([]string(nil), in[index].SupportedParameters...)
		out[index].SupportedInputModalities = append([]string(nil), in[index].SupportedInputModalities...)
		out[index].SupportedOutputModalities = append([]string(nil), in[index].SupportedOutputModalities...)
		if in[index].Thinking != nil {
			thinking := *in[index].Thinking
			thinking.Levels = append([]string(nil), in[index].Thinking.Levels...)
			out[index].Thinking = &thinking
		}
	}
	return out
}
