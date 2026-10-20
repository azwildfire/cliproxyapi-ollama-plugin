package provider

import (
	"encoding/json"
	"testing"
)

const sampleTags = `{"models":[
 {"name":"glm-5.3-flash","model":"glm-5.3-flash","modified_at":"2026-08-26T07:00:00-07:00","size":0,"digest":"abc","details":{"family":"glm5_next"},"capabilities":["completion","thinking","tools","vision"]},
 {"name":"kimi-k3","model":"kimi-k3","modified_at":"2026-07-01T00:00:00Z","size":0,"digest":"def","details":{},"capabilities":["vision","thinking","completion","tools"]},
 {"name":"embeddinggemma","model":"embeddinggemma","modified_at":"2026-01-01T00:00:00Z","size":0,"digest":"ghi","details":{},"capabilities":["embedding"]},
 {"name":"GLM-5.3-FLASH","model":"GLM-5.3-FLASH","modified_at":"2026-08-26T07:00:00-07:00","size":0,"digest":"abc","details":{},"capabilities":["completion"]},
 {"name":"gpt-oss:20b","model":"gpt-oss:20b","modified_at":"2025-08-05T00:00:00Z","size":0,"digest":"jkl","details":{},"capabilities":["completion","tools","thinking"]}
]}`

func TestSelectModelsFiltersAndSorts(t *testing.T) {
	var tags tagsResponse
	if err := json.Unmarshal([]byte(sampleTags), &tags); err != nil {
		t.Fatalf("unmarshal tags: %v", err)
	}
	selected := selectModels(tags.Models, []string{"gpt-oss:*"})
	if len(selected) != 2 {
		t.Fatalf("expected glm and kimi, got %+v", selected)
	}
	if selected[0].Name != "glm-5.3-flash" || selected[1].Name != "kimi-k3" {
		t.Fatalf("unexpected order %q, %q", selected[0].Name, selected[1].Name)
	}
}

func TestModelExcluded(t *testing.T) {
	cases := map[string]struct {
		patterns []string
		want     bool
	}{
		"exact":        {[]string{"glm-5.3-flash"}, true},
		"glob":         {[]string{"glm-*"}, true},
		"other":        {[]string{"kimi-*"}, false},
		"colon-glob":   {[]string{"*:cloud"}, false},
		"question":     {[]string{"glm-5.?-flash"}, true},
		"empty-ignore": {[]string{""}, false},
	}
	for name, tc := range cases {
		if got := modelExcluded("glm-5.3-flash", tc.patterns); got != tc.want {
			t.Errorf("%s: modelExcluded = %v, want %v", name, got, tc.want)
		}
	}
}

func TestModelInfoMapping(t *testing.T) {
	var tags tagsResponse
	if err := json.Unmarshal([]byte(sampleTags), &tags); err != nil {
		t.Fatalf("unmarshal tags: %v", err)
	}
	info := modelInfo(tags.Models[0], modelDetailInfo{ContextLength: 1048576, ThinkingLevels: []string{"low", "high", "max"}})
	if info.ID != "glm-5.3-flash" || info.OwnedBy != "ollama" || info.ContextLength != 1048576 {
		t.Fatalf("unexpected info %+v", info)
	}
	if info.Thinking == nil || len(info.Thinking.Levels) != 3 || !info.Thinking.ZeroAllowed {
		t.Fatalf("thinking support not mapped: %+v", info.Thinking)
	}
	if len(info.SupportedInputModalities) != 2 {
		t.Fatalf("vision model should accept images: %v", info.SupportedInputModalities)
	}
	if info.Created == 0 {
		t.Fatalf("modified_at with offset should parse")
	}
	foundTools := false
	for _, parameter := range info.SupportedParameters {
		if parameter == "tools" {
			foundTools = true
		}
	}
	if !foundTools {
		t.Fatalf("tools capability should map to a supported parameter: %v", info.SupportedParameters)
	}
}

func TestShowResponseContextLength(t *testing.T) {
	raw := `{"capabilities":["completion","thinking"],"details":{"parent_model":"kimi-k3","family":"kimi-k3"},"model_info":{"general.architecture":"kimi-k3","kimi-k3.context_length":1048576,"kimi-k3.embedding_length":7168},"thinking":{"values":[false,"low","high","max"],"default":"max"}}`
	var show showResponse
	if err := json.Unmarshal([]byte(raw), &show); err != nil {
		t.Fatalf("unmarshal show (mixed thinking values must decode): %v", err)
	}
	if show.Thinking == nil || len(show.Thinking.Values) != 4 {
		t.Fatalf("thinking block not decoded: %+v", show.Thinking)
	}
	if show.ModelInfo["kimi-k3.context_length"].(float64) != 1048576 {
		t.Fatalf("context length not decoded")
	}
}
