package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/redact"
	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/transport"
)

// requester is the buffered-HTTP half of transport.Host; the CLI path swaps in a direct client.
type requester interface {
	Do(ctx context.Context, callbackID string, req transport.Request) (transport.Response, error)
}

// credentials is what one Ollama Cloud credential needs to authenticate a request.
type credentials struct {
	key     *deviceKey
	apiKey  string
	baseURL string
}

func (s *Service) credentialsFor(storage authStorage) (credentials, error) {
	baseURL := storage.BaseURL
	if baseURL == "" {
		baseURL = s.Config().BaseURL
	}
	creds := credentials{baseURL: strings.TrimRight(baseURL, "/")}
	if storage.PrivateKey == "" {
		if storage.APIKey == "" {
			return credentials{}, statusError("invalid_credential", "Ollama credential has no device key or API key", http.StatusUnauthorized)
		}
		creds.apiKey = storage.APIKey
		return creds, nil
	}
	key, errKey := s.deviceKeyFor(storage.PrivateKey)
	if errKey != nil {
		return credentials{}, errKey
	}
	creds.key = key
	return creds, nil
}

func (s *Service) deviceKeyFor(pemText string) (*deviceKey, error) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(pemText)))
	cacheKey := hex.EncodeToString(sum[:])
	s.keyMu.Lock()
	cached, ok := s.keys[cacheKey]
	s.keyMu.Unlock()
	if ok {
		return cached, nil
	}
	key, errParse := parseDeviceKey(pemText)
	if errParse != nil {
		return nil, statusError("invalid_credential", "Ollama device key is invalid: "+errParse.Error(), http.StatusUnauthorized)
	}
	s.keyMu.Lock()
	s.keys[cacheKey] = key
	s.keyMu.Unlock()
	return key, nil
}

// authorize returns the request URI (path plus query) and the Authorization header value.
func (c credentials) authorize(method, path string, query url.Values, now func() time.Time) (string, string, error) {
	if c.key == nil {
		uri := path
		if len(query) > 0 {
			uri += "?" + query.Encode()
		}
		return uri, "Bearer " + c.apiKey, nil
	}
	uri := signedRequestURI(path, query, now())
	token, errSign := c.key.sign(method, uri)
	if errSign != nil {
		return "", "", errSign
	}
	return uri, token, nil
}

func (c credentials) secrets() []string {
	if c.apiKey != "" {
		return []string{c.apiKey}
	}
	return nil
}

func (c credentials) identity() string {
	if c.key != nil {
		return c.key.fingerprint()
	}
	sum := sha256.Sum256([]byte(c.apiKey))
	return "apikey:" + hex.EncodeToString(sum[:8])
}

type apiCall struct {
	method string
	path   string
	query  url.Values
	body   []byte
	accept string
}

// call performs one authenticated buffered request against the credential's base URL.
func (s *Service) call(ctx context.Context, r requester, callbackID string, creds credentials, call apiCall) (transport.Response, error) {
	method := strings.ToUpper(firstNonEmpty(call.method, http.MethodGet))
	uri, authorization, errAuth := creds.authorize(method, call.path, call.query, s.now)
	if errAuth != nil {
		return transport.Response{}, errAuth
	}
	headers := s.baseHeaders(call.accept)
	headers.Set("Authorization", authorization)
	if len(call.body) > 0 {
		headers.Set("Content-Type", "application/json")
	}
	resp, errDo := r.Do(ctx, callbackID, transport.Request{
		Method:  method,
		URL:     creds.baseURL + uri,
		Headers: headers,
		Body:    call.body,
	})
	if errDo != nil {
		return transport.Response{}, fmt.Errorf("call Ollama Cloud %s: %w", call.path, errDo)
	}
	return resp, nil
}

func (s *Service) baseHeaders(accept string) http.Header {
	headers := http.Header{}
	headers.Set("Accept", firstNonEmpty(accept, "application/json"))
	headers.Set("User-Agent", s.userAgent())
	return headers
}

// userInfo is the ollama.com account behind a credential (POST /api/me).
type userInfo struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	FirstName string `json:"firstname"`
	LastName  string `json:"lastname"`
	Plan      string `json:"plan"`
}

// whoami validates a credential and returns the account it belongs to. The HTTP status is
// returned alongside the error so callers can distinguish "not connected yet" from failures.
func (s *Service) whoami(ctx context.Context, r requester, callbackID string, creds credentials) (userInfo, int, error) {
	resp, errCall := s.call(ctx, r, callbackID, creds, apiCall{method: http.MethodPost, path: "/api/me"})
	if errCall != nil {
		return userInfo{}, 0, errCall
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return userInfo{}, resp.StatusCode, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, creds.secrets()...))
	}
	var user userInfo
	if errUnmarshal := json.Unmarshal(resp.Body, &user); errUnmarshal != nil {
		return userInfo{}, resp.StatusCode, fmt.Errorf("decode Ollama account response: %w", errUnmarshal)
	}
	user.Name = strings.TrimSpace(user.Name)
	user.Email = strings.TrimSpace(user.Email)
	user.Plan = strings.ToLower(strings.TrimSpace(user.Plan))
	// ollama.com answers 200 with an empty account for a key that nobody has connected yet;
	// the ollama server treats a missing name as "not signed in", and so do we.
	if user.Name == "" && user.Email == "" {
		return userInfo{}, http.StatusUnauthorized, statusError("not_connected", "this device key is not connected to an ollama.com account", http.StatusUnauthorized)
	}
	if user.Plan == "" {
		user.Plan = "free"
	}
	return user, resp.StatusCode, nil
}

func filterResponseHeaders(headers http.Header) http.Header {
	out := http.Header{}
	for key, values := range headers {
		switch lower := strings.ToLower(key); {
		case lower == "content-type", lower == "cache-control", lower == "retry-after", lower == "x-request-id":
			out[key] = append([]string(nil), values...)
		case strings.HasPrefix(lower, "x-ratelimit"):
			out[key] = append([]string(nil), values...)
		}
	}
	return out
}

func cloneHeader(headers http.Header) http.Header {
	if len(headers) == 0 {
		return http.Header{}
	}
	return headers.Clone()
}
