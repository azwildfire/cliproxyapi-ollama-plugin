package provider

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed webui/signin.html
var signinPage []byte

const (
	managementMenuLabel  = "Ollama Cloud"
	managementSigninPath = "signin"
)

// ManagementRegistrationResponse answers management.register with the plugin's browser pages.
type ManagementRegistrationResponse struct {
	Routes    []ManagementRoute `json:"routes"`
	Resources []ResourceRoute   `json:"resources"`
}

// ManagementRoute is an authenticated Management API route (none are registered today).
type ManagementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

// ResourceRoute is a browser-navigable page under /v0/resource/plugins/<plugin-id>/.
type ResourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

// ManagementRequest is the management.handle RPC payload.
type ManagementRequest struct {
	Method         string              `json:"method"`
	Path           string              `json:"path"`
	Headers        map[string][]string `json:"headers"`
	Query          map[string][]string `json:"query"`
	Body           []byte              `json:"body"`
	HostCallbackID string              `json:"host_callback_id,omitempty"`
}

// ManagementResponse is the management.handle RPC result; field names follow pluginapi.ManagementResponse.
type ManagementResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers,omitempty"`
	Body       []byte              `json:"Body,omitempty"`
}

// RegisterManagement exposes the sign-in page in the management center's plugin menu. The page
// itself is static and unauthenticated; every action it performs goes through the host's own
// management endpoints with the management key.
func (s *Service) RegisterManagement() ManagementRegistrationResponse {
	return ManagementRegistrationResponse{
		Routes: []ManagementRoute{},
		Resources: []ResourceRoute{
			{
				Path:        managementSigninPath,
				Menu:        managementMenuLabel,
				Description: "Sign in to Ollama Cloud with ollama.com or an API key, and see each account's quota.",
			},
		},
	}
}

// HandleManagement serves the embedded page for GET <resource base>/signin.
func (s *Service) HandleManagement(req ManagementRequest) ManagementResponse {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	if method == http.MethodGet && strings.HasSuffix(path, "/"+managementSigninPath) {
		return ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type":  {"text/html; charset=utf-8"},
				"Cache-Control": {"no-store"},
			},
			Body: signinPage,
		}
	}
	return ManagementResponse{
		StatusCode: http.StatusNotFound,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"error":"not found"}`),
	}
}
