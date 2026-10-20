package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	authMethodDeviceKey = "device_key"
	authMethodAPIKey    = "api_key"
)

// authStorage is the provider-owned part of an auth file. The host merges its own metadata
// (type, priority, email, ...) into the same JSON object when it persists the credential.
type authStorage struct {
	Type       string `json:"type"`
	AuthMethod string `json:"auth_method,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	PublicKey  string `json:"public_key,omitempty"`
	APIKey     string `json:"api_key,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	UserName   string `json:"user_name,omitempty"`
	Email      string `json:"email,omitempty"`
	Plan       string `json:"plan,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

func parseStorage(raw []byte) (authStorage, error) {
	var storage authStorage
	if len(raw) == 0 {
		return storage, fmt.Errorf("Ollama auth storage is empty")
	}
	if errUnmarshal := json.Unmarshal(raw, &storage); errUnmarshal != nil {
		return storage, fmt.Errorf("decode Ollama auth storage: %w", errUnmarshal)
	}
	if !strings.EqualFold(strings.TrimSpace(storage.Type), ProviderID) {
		return storage, fmt.Errorf("auth type is not %s", ProviderID)
	}
	storage.Type = ProviderID
	storage.PrivateKey = strings.TrimSpace(storage.PrivateKey)
	storage.PublicKey = strings.TrimSpace(storage.PublicKey)
	storage.APIKey = strings.TrimSpace(storage.APIKey)
	storage.DeviceName = strings.TrimSpace(storage.DeviceName)
	storage.BaseURL = strings.TrimRight(strings.TrimSpace(storage.BaseURL), "/")
	storage.UserName = strings.TrimSpace(storage.UserName)
	storage.Email = strings.TrimSpace(storage.Email)
	storage.Plan = strings.ToLower(strings.TrimSpace(storage.Plan))
	switch {
	case storage.PrivateKey != "":
		storage.AuthMethod = authMethodDeviceKey
	case storage.APIKey != "":
		storage.AuthMethod = authMethodAPIKey
	default:
		return storage, fmt.Errorf("Ollama auth storage has neither a device private key nor an API key")
	}
	return storage, nil
}

func (s authStorage) label() string {
	name := firstNonEmpty(s.UserName, s.Email, "ollama")
	if s.Plan != "" {
		return name + " (" + s.Plan + ")"
	}
	return name
}

func (s authStorage) accountName() string {
	if s.UserName != "" {
		return s.UserName
	}
	if at := strings.Index(s.Email, "@"); at > 0 {
		return s.Email[:at]
	}
	return "account"
}

func defaultMetadata(storage authStorage) map[string]any {
	metadata := map[string]any{
		"type":        ProviderID,
		"auth_method": storage.AuthMethod,
	}
	if storage.Email != "" {
		metadata["email"] = storage.Email
	}
	if storage.UserName != "" {
		metadata["account"] = storage.UserName
	}
	if storage.Plan != "" {
		metadata["account_type"] = storage.Plan
	}
	if storage.DeviceName != "" {
		metadata["device_name"] = storage.DeviceName
	}
	return metadata
}

// refreshMetadata copies existing host metadata and updates the account fields we own.
func refreshMetadata(existing map[string]any, storage authStorage) map[string]any {
	if len(existing) == 0 {
		return defaultMetadata(storage)
	}
	out := make(map[string]any, len(existing)+4)
	for key, value := range existing {
		out[key] = value
	}
	out["type"] = ProviderID
	out["auth_method"] = storage.AuthMethod
	if storage.Email != "" {
		out["email"] = storage.Email
	}
	if storage.UserName != "" {
		out["account"] = storage.UserName
	}
	if storage.Plan != "" {
		out["account_type"] = storage.Plan
	}
	return out
}

func authData(storage authStorage, id, fileName, prefix, proxyURL string, disabled bool, metadata map[string]any, attributes map[string]string, now time.Time) (pluginapi.AuthData, error) {
	raw, errMarshal := json.Marshal(storage)
	if errMarshal != nil {
		return pluginapi.AuthData{}, fmt.Errorf("encode Ollama auth storage: %w", errMarshal)
	}
	if fileName == "" {
		fileName = credentialFileName(storage)
	}
	if id == "" {
		id = fileName
	}
	if len(metadata) == 0 {
		metadata = defaultMetadata(storage)
	}
	if len(attributes) == 0 {
		attributes = map[string]string{"auth_kind": "oauth"}
	}
	return pluginapi.AuthData{
		Provider:         ProviderID,
		ID:               id,
		FileName:         fileName,
		Label:            storage.label(),
		Prefix:           prefix,
		ProxyURL:         proxyURL,
		Disabled:         disabled,
		StorageJSON:      raw,
		Metadata:         metadata,
		Attributes:       attributes,
		NextRefreshAfter: now.Add(24 * time.Hour),
	}, nil
}

func credentialFileName(storage authStorage) string {
	name := sanitizeFileToken(storage.accountName())
	if storage.AuthMethod == authMethodAPIKey {
		return "ollama-cloud-" + name + "-apikey.json"
	}
	return "ollama-cloud-" + name + ".json"
}

func sanitizeFileToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var clean strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			clean.WriteRune(r)
		case r == '-', r == '_', r == '.':
			clean.WriteRune(r)
		default:
			clean.WriteByte('-')
		}
	}
	out := strings.Trim(clean.String(), "-.")
	if out == "" {
		out = "account"
	}
	return out
}
