package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestParseStorage(t *testing.T) {
	key, errGenerate := generateDeviceKey("test")
	if errGenerate != nil {
		t.Fatalf("generateDeviceKey: %v", errGenerate)
	}
	raw := `{"type":"ollama-cloud","private_key":` + jsonString(key.privatePEM) + `,"user_name":"azwildfire","email":"a@example.com","plan":"PRO","priority":5}`
	storage, errParse := parseStorage([]byte(raw))
	if errParse != nil {
		t.Fatalf("parseStorage: %v", errParse)
	}
	if storage.AuthMethod != authMethodDeviceKey || storage.Plan != "pro" {
		t.Fatalf("unexpected storage %+v", storage)
	}
	if storage.label() != "azwildfire (pro)" {
		t.Fatalf("label %q", storage.label())
	}

	apiKey, errAPI := parseStorage([]byte(`{"type":"ollama-cloud","api_key":"abc123"}`))
	if errAPI != nil || apiKey.AuthMethod != authMethodAPIKey {
		t.Fatalf("api key storage: %+v %v", apiKey, errAPI)
	}
	if _, err := parseStorage([]byte(`{"type":"copilot","api_key":"x"}`)); err == nil {
		t.Fatalf("foreign type must be rejected")
	}
	if _, err := parseStorage([]byte(`{"type":"ollama"}`)); err == nil {
		t.Fatalf("storage without credentials must be rejected")
	}
}

func TestAuthDataAndFileName(t *testing.T) {
	storage := authStorage{Type: ProviderID, AuthMethod: authMethodDeviceKey, PrivateKey: "pem", UserName: "AZ Wildfire!", Email: "a@example.com", Plan: "pro", DeviceName: "box"}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	data, errData := authData(storage, "", "", "", "", false, nil, nil, now)
	if errData != nil {
		t.Fatalf("authData: %v", errData)
	}
	if data.FileName != "ollama-cloud-az-wildfire.json" || data.ID != data.FileName {
		t.Fatalf("file name %q id %q", data.FileName, data.ID)
	}
	if data.Provider != ProviderID || data.Attributes["auth_kind"] != "oauth" {
		t.Fatalf("unexpected auth data %+v", data)
	}
	if data.Metadata["email"] != "a@example.com" || data.Metadata["account_type"] != "pro" || data.Metadata["account"] != "AZ Wildfire!" {
		t.Fatalf("metadata %+v", data.Metadata)
	}
	if !data.NextRefreshAfter.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("refresh %v", data.NextRefreshAfter)
	}
	if !strings.Contains(string(data.StorageJSON), `"type":"ollama-cloud"`) {
		t.Fatalf("storage JSON %s", data.StorageJSON)
	}

	storage.AuthMethod = authMethodAPIKey
	storage.PrivateKey = ""
	storage.APIKey = "k"
	if credentialFileName(storage) != "ollama-cloud-az-wildfire-apikey.json" {
		t.Fatalf("api key file name %q", credentialFileName(storage))
	}
	existing := map[string]any{"priority": 7, "account_type": "free"}
	refreshed := refreshMetadata(existing, storage)
	if refreshed["priority"] != 7 || refreshed["account_type"] != "pro" {
		t.Fatalf("refreshMetadata %+v", refreshed)
	}
	var _ pluginapi.AuthData = data
}

func jsonString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + replacer.Replace(value) + `"`
}
