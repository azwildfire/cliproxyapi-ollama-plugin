package provider

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	loginPollInterval      = 5 * time.Second
	loginRateLimitInterval = 15 * time.Second
	maxImportedKeyBytes    = 16 * 1024
)

// loginSession is a pending sign-in started through the management API or the CLI.
type loginSession struct {
	method     string
	key        *deviceKey
	apiKey     string
	deviceName string
	baseURL    string
	expiresAt  time.Time
	nextPoll   time.Time
	polling    bool
}

// ParseAuth recognises Ollama auth files by their type field.
func (s *Service) ParseAuth(req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	var rawType struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(req.RawJSON, &rawType) != nil || !strings.EqualFold(strings.TrimSpace(rawType.Type), ProviderID) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	storage, errParse := parseStorage(req.RawJSON)
	if errParse != nil {
		return pluginapi.AuthParseResponse{}, errParse
	}
	data, errData := authData(storage, req.FileName, req.FileName, "", "", false, nil, nil, s.now())
	if errData != nil {
		return pluginapi.AuthParseResponse{}, errData
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: data}, nil
}

// StartLogin begins one of three flows, selected by the login metadata (management query
// parameters): api_key=<key> stores an API key, private_key / private_key_path imports an
// existing ollama device key, and the default generates a new device key that the user links
// by opening the returned ollama.com/connect URL.
func (s *Service) StartLogin(_ context.Context, _ string, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	cfg := s.Config()
	if !cfg.Enabled {
		return pluginapi.AuthLoginStartResponse{}, statusError("plugin_disabled", "Ollama Cloud plugin is disabled", http.StatusServiceUnavailable)
	}
	now := s.now()
	deviceName := firstNonEmpty(metaString(req.Metadata, "device_name"), metaString(req.Metadata, "name"), cfg.DeviceName, defaultDeviceName())
	session := &loginSession{
		deviceName: deviceName,
		baseURL:    cfg.BaseURL,
		expiresAt:  now.Add(cfg.loginTimeout()),
		nextPoll:   now,
	}
	responseMetadata := map[string]any{
		"device_name":   deviceName,
		"poll_interval": int(loginPollInterval.Seconds()),
	}

	var loginURL string
	if apiKey := metaString(req.Metadata, "api_key"); apiKey != "" {
		session.method = authMethodAPIKey
		session.apiKey = apiKey
		loginURL = cfg.BaseURL + "/settings/keys"
		responseMetadata["message"] = "Validating the Ollama API key"
	} else {
		pemText, errImport := importedPrivateKey(req.Metadata)
		if errImport != nil {
			return pluginapi.AuthLoginStartResponse{}, statusError("invalid_request", errImport.Error(), http.StatusBadRequest)
		}
		var key *deviceKey
		if pemText != "" {
			parsed, errParse := parseDeviceKey(pemText)
			if errParse != nil {
				return pluginapi.AuthLoginStartResponse{}, statusError("invalid_request", "imported private key is not a valid OpenSSH key: "+errParse.Error(), http.StatusBadRequest)
			}
			key = parsed
			responseMetadata["imported"] = true
		} else {
			generated, errGenerate := generateDeviceKey("cliproxyapi-ollama " + deviceName)
			if errGenerate != nil {
				return pluginapi.AuthLoginStartResponse{}, errGenerate
			}
			key = generated
		}
		session.method = authMethodDeviceKey
		session.key = key
		loginURL = key.connectURL(cfg.BaseURL, deviceName)
		responseMetadata["public_key"] = key.publicKey
		responseMetadata["message"] = "Open the URL, sign in to ollama.com, and approve the device"
	}
	responseMetadata["auth_method"] = session.method

	state, errState := randomIdentifier(24)
	if errState != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("generate login state: %w", errState)
	}
	s.loginMu.Lock()
	s.pruneLoginsLocked(now)
	s.logins[state] = session
	s.loginMu.Unlock()

	return pluginapi.AuthLoginStartResponse{
		Provider:  ProviderID,
		URL:       loginURL,
		State:     state,
		ExpiresAt: session.expiresAt.UTC(),
		Metadata:  responseMetadata,
	}, nil
}

// PollLogin checks whether ollama.com accepts the pending credential yet.
func (s *Service) PollLogin(ctx context.Context, callbackID string, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	state := strings.TrimSpace(req.State)
	now := s.now()
	s.loginMu.Lock()
	session := s.logins[state]
	if session == nil {
		s.loginMu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "unknown or expired Ollama login session"}, nil
	}
	if !now.Before(session.expiresAt) {
		delete(s.logins, state)
		s.loginMu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Ollama login expired before the device was connected; start the login again"}, nil
	}
	if session.polling || now.Before(session.nextPoll) {
		s.loginMu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: pendingMessage(session.method)}, nil
	}
	session.polling = true
	snapshot := *session
	s.loginMu.Unlock()

	creds := credentials{key: snapshot.key, apiKey: snapshot.apiKey, baseURL: snapshot.baseURL}
	user, status, errWhoami := s.whoami(ctx, s.host, callbackID, creds)
	if errWhoami != nil {
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			if snapshot.method == authMethodAPIKey {
				s.finishPoll(state, loginPollInterval, true)
				return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "ollama.com rejected the API key"}, nil
			}
			s.finishPoll(state, loginPollInterval, false)
			return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: pendingMessage(snapshot.method)}, nil
		case status == http.StatusTooManyRequests:
			s.finishPoll(state, loginRateLimitInterval, false)
			return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "ollama.com rate limited the login check; retrying shortly"}, nil
		case status >= 400 && status < 500:
			s.finishPoll(state, loginPollInterval, true)
			return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: errWhoami.Error()}, nil
		default:
			s.finishPoll(state, loginPollInterval, false)
			return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "ollama.com is not reachable yet: " + errWhoami.Error()}, nil
		}
	}

	storage := s.storageForSession(snapshot, user, now)
	data, errData := authData(storage, "", "", "", "", false, nil, nil, now)
	if errData != nil {
		s.finishPoll(state, loginPollInterval, true)
		return pluginapi.AuthLoginPollResponse{}, errData
	}
	s.finishPoll(state, loginPollInterval, true)
	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: fmt.Sprintf("Signed in to Ollama Cloud as %s (%s plan)", firstNonEmpty(user.Name, user.Email), user.Plan),
		Auth:    data,
	}, nil
}

func (s *Service) storageForSession(session loginSession, user userInfo, now time.Time) authStorage {
	timestamp := now.UTC().Format(time.RFC3339)
	storage := authStorage{
		Type:       ProviderID,
		AuthMethod: session.method,
		DeviceName: session.deviceName,
		UserID:     user.ID,
		UserName:   user.Name,
		Email:      user.Email,
		Plan:       user.Plan,
		CreatedAt:  timestamp,
		UpdatedAt:  timestamp,
	}
	if session.baseURL != "" && session.baseURL != DefaultBaseURL {
		storage.BaseURL = session.baseURL
	}
	if session.key != nil {
		storage.PrivateKey = session.key.privatePEM
		storage.PublicKey = session.key.publicKey
	} else {
		storage.APIKey = session.apiKey
		storage.DeviceName = ""
	}
	return storage
}

func (s *Service) finishPoll(state string, interval time.Duration, terminal bool) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	session := s.logins[state]
	if session == nil {
		return
	}
	if terminal {
		delete(s.logins, state)
		return
	}
	session.polling = false
	session.nextPoll = s.now().Add(interval)
}

func (s *Service) pruneLoginsLocked(now time.Time) {
	for state, session := range s.logins {
		if session == nil || !now.Before(session.expiresAt) {
			delete(s.logins, state)
		}
	}
}

// RefreshAuth re-validates the credential and refreshes the account label and plan. Device keys
// and API keys do not expire, so this mostly detects keys that were disconnected on ollama.com.
func (s *Service) RefreshAuth(ctx context.Context, callbackID string, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.AuthRefreshResponse{}, errParse
	}
	creds, errCreds := s.credentialsFor(storage)
	if errCreds != nil {
		return pluginapi.AuthRefreshResponse{}, errCreds
	}
	now := s.now()
	user, status, errWhoami := s.whoami(ctx, s.host, callbackID, creds)
	if errWhoami != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return pluginapi.AuthRefreshResponse{}, statusError("auth_revoked", "ollama.com no longer accepts this credential; disconnect it and sign in again", http.StatusUnauthorized)
		}
		// Transient failure: keep the credential and retry sooner.
		data, errData := authData(storage, req.AuthID, "", "", "", false, req.Metadata, req.Attributes, now)
		if errData != nil {
			return pluginapi.AuthRefreshResponse{}, errData
		}
		data.NextRefreshAfter = now.Add(time.Hour)
		return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: data.NextRefreshAfter}, nil
	}
	storage.UserID = firstNonEmpty(user.ID, storage.UserID)
	storage.UserName = firstNonEmpty(user.Name, storage.UserName)
	storage.Email = firstNonEmpty(user.Email, storage.Email)
	storage.Plan = firstNonEmpty(user.Plan, storage.Plan)
	storage.UpdatedAt = now.UTC().Format(time.RFC3339)
	data, errData := authData(storage, req.AuthID, "", "", "", false, refreshMetadata(req.Metadata, storage), req.Attributes, now)
	if errData != nil {
		return pluginapi.AuthRefreshResponse{}, errData
	}
	return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: data.NextRefreshAfter}, nil
}

func pendingMessage(method string) string {
	if method == authMethodAPIKey {
		return "validating the Ollama API key"
	}
	return "waiting for the device to be connected at ollama.com"
}

func metaString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []string:
		if len(value) > 0 {
			return strings.TrimSpace(value[0])
		}
	case []any:
		if len(value) > 0 {
			if text, ok := value[0].(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

// importedPrivateKey reads an existing ollama device key from the login metadata. The key may be
// passed inline (PEM text, or base64 of the PEM) or as a path readable by the proxy process.
func importedPrivateKey(metadata map[string]any) (string, error) {
	if inline := metaString(metadata, "private_key"); inline != "" {
		return decodeInlineKey(inline)
	}
	path := metaString(metadata, "private_key_path")
	if path == "" {
		return "", nil
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		return "", fmt.Errorf("private_key_path: %w", errStat)
	}
	if info.IsDir() || info.Size() > maxImportedKeyBytes {
		return "", fmt.Errorf("private_key_path must point to a small private key file")
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return "", fmt.Errorf("private_key_path: %w", errRead)
	}
	return string(raw), nil
}

func decodeInlineKey(value string) (string, error) {
	if strings.Contains(value, "-----BEGIN") {
		// Query strings may have folded newlines into spaces; restore the PEM layout.
		return normalizePEM(value), nil
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, errDecode := encoding.DecodeString(value)
		if errDecode == nil && strings.Contains(string(decoded), "-----BEGIN") {
			return normalizePEM(string(decoded)), nil
		}
	}
	return "", fmt.Errorf("private_key must be OpenSSH PEM text or its base64 encoding")
}

var pemLayoutPattern = regexp.MustCompile(`(?s)^(-----BEGIN [A-Z0-9 ]+-----)\s*(.*?)\s*(-----END [A-Z0-9 ]+-----)$`)

// normalizePEM restores the line layout of a PEM block whose newlines were flattened to spaces
// (for example when the key was pasted into a query string).
func normalizePEM(value string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "\n") {
		return value + "\n"
	}
	match := pemLayoutPattern.FindStringSubmatch(value)
	if match == nil {
		return value + "\n"
	}
	body := strings.Join(strings.Fields(match[2]), "")
	var lines []string
	for len(body) > 70 {
		lines = append(lines, body[:70])
		body = body[70:]
	}
	if body != "" {
		lines = append(lines, body)
	}
	return match[1] + "\n" + strings.Join(lines, "\n") + "\n" + match[3] + "\n"
}

func defaultDeviceName() string {
	host, errHost := os.Hostname()
	if errHost != nil || strings.TrimSpace(host) == "" {
		return "CLIProxyAPI"
	}
	return "CLIProxyAPI-" + strings.TrimSpace(host)
}

func randomIdentifier(bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, errRead := rand.Read(raw); errRead != nil {
		return "", errRead
	}
	return hex.EncodeToString(raw), nil
}
