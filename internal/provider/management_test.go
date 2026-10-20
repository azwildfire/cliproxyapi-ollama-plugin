package provider

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRegisterManagementExposesSigninPage(t *testing.T) {
	s := New(nil)
	reg := s.RegisterManagement()
	if len(reg.Resources) != 1 || reg.Resources[0].Path != "signin" || reg.Resources[0].Menu == "" {
		t.Fatalf("unexpected registration %+v", reg)
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"resources":[{"path":"signin"`) {
		t.Fatalf("registration JSON %s", raw)
	}
}

func TestHandleManagementServesPage(t *testing.T) {
	s := New(nil)
	resp := s.HandleManagement(ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/cliproxyapi-ollama/signin"})
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Headers["Content-Type"][0], "text/html") {
		t.Fatalf("unexpected response %d %v", resp.StatusCode, resp.Headers)
	}
	body := string(resp.Body)
	for _, needle := range []string{"<!doctype html>", "ollama-cloud", "/v8/management/oauth/auth-url", "api_key", "private_key", "/quota"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("page is missing %q", needle)
		}
	}
	notFound := s.HandleManagement(ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/cliproxyapi-ollama/other"})
	if notFound.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown paths must 404, got %d", notFound.StatusCode)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"StatusCode":200`) {
		t.Fatalf("response must use the host's field names: %s", raw[:80])
	}
}
