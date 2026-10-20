package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/redact"
	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/sse"
	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	formatOpenAI          = "openai"
	formatClaude          = "claude"
	formatOpenAIResponses = "openai-response"

	endpointChatCompletions = "/v1/chat/completions"
	endpointMessages        = "/v1/messages"
	endpointResponses       = "/v1/responses"
)

// ExecuteRequest is the executor RPC payload: the SDK request plus host stream identifiers.
type ExecuteRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// HTTPRequest is the generic executor HTTP RPC payload.
type HTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func normalizeFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "openai", "openai-chat", "chat", "chat-completions":
		return formatOpenAI
	case "claude", "anthropic", "messages":
		return formatClaude
	case "responses", "openai-response", "openai-responses":
		return formatOpenAIResponses
	default:
		return ""
	}
}

func endpointForFormat(format string) (string, bool) {
	switch format {
	case formatOpenAI:
		return endpointChatCompletions, true
	case formatClaude:
		return endpointMessages, true
	case formatOpenAIResponses:
		return endpointResponses, true
	default:
		return "", false
	}
}

func (s *Service) resolveExecution(req ExecuteRequest) (string, string, credentials, error) {
	format := normalizeFormat(firstNonEmpty(req.SourceFormat, req.Format))
	endpoint, ok := endpointForFormat(format)
	if !ok {
		return "", "", credentials{}, statusError("unsupported_format", fmt.Sprintf("unsupported request format %q", firstNonEmpty(req.SourceFormat, req.Format)), http.StatusUnprocessableEntity)
	}
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return "", "", credentials{}, statusError("invalid_credential", errParse.Error(), http.StatusUnauthorized)
	}
	creds, errCreds := s.credentialsFor(storage)
	if errCreds != nil {
		return "", "", credentials{}, errCreds
	}
	return format, endpoint, creds, nil
}

// Execute performs a buffered completion against the ollama.com endpoint matching the format.
func (s *Service) Execute(ctx context.Context, req ExecuteRequest) (pluginapi.ExecutorResponse, error) {
	format, endpoint, creds, errResolve := s.resolveExecution(req)
	if errResolve != nil {
		return pluginapi.ExecutorResponse{}, errResolve
	}
	body, errBody := prepareBody(req.Payload, req.Model, false, format)
	if errBody != nil {
		return pluginapi.ExecutorResponse{}, errBody
	}
	resp, errCall := s.call(ctx, s.host, req.HostCallbackID, creds, apiCall{method: http.MethodPost, path: endpoint, body: body})
	if errCall != nil {
		return pluginapi.ExecutorResponse{}, errCall
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.ExecutorResponse{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, creds.secrets()...))
	}
	return pluginapi.ExecutorResponse{
		Payload: resp.Body,
		Headers: filterResponseHeaders(resp.Headers),
		Metadata: map[string]any{
			"ollama_endpoint": endpoint,
		},
	}, nil
}

// ExecuteStream opens the upstream SSE stream and pumps frames to the host stream.
func (s *Service) ExecuteStream(ctx context.Context, req ExecuteRequest) (http.Header, error) {
	if strings.TrimSpace(req.StreamID) == "" {
		return nil, statusError("invalid_request", "stream_id is required", http.StatusBadRequest)
	}
	format, endpoint, creds, errResolve := s.resolveExecution(req)
	if errResolve != nil {
		return nil, errResolve
	}
	body, errBody := prepareBody(req.Payload, req.Model, true, format)
	if errBody != nil {
		return nil, errBody
	}
	uri, authorization, errAuth := creds.authorize(http.MethodPost, endpoint, nil, s.now)
	if errAuth != nil {
		return nil, errAuth
	}
	headers := s.baseHeaders("text/event-stream")
	headers.Set("Authorization", authorization)
	headers.Set("Content-Type", "application/json")
	upstream, errOpen := s.host.OpenStream(ctx, req.HostCallbackID, transport.Request{
		Method:  http.MethodPost,
		URL:     creds.baseURL + uri,
		Headers: headers,
		Body:    body,
	})
	if errOpen != nil {
		return nil, fmt.Errorf("open Ollama Cloud stream: %w", errOpen)
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		errorBody, errCollect := s.collectStreamError(ctx, upstream)
		if errCollect != nil {
			return nil, errCollect
		}
		return nil, upstreamStatusError(upstream.StatusCode, redact.ErrorBody(errorBody, creds.secrets()...))
	}
	go s.pumpStream(req.StreamID, format, upstream)
	out := filterResponseHeaders(upstream.Headers)
	out.Set("Content-Type", "text/event-stream")
	out.Set("Cache-Control", "no-cache")
	return out, nil
}

func (s *Service) collectStreamError(ctx context.Context, stream transport.Stream) ([]byte, error) {
	defer func() { _ = s.host.CloseStream(context.Background(), stream.ID) }()
	var body []byte
	for {
		chunk, errRead := s.host.ReadStream(ctx, stream.ID)
		if errRead != nil {
			return nil, fmt.Errorf("read Ollama Cloud error stream: %w", errRead)
		}
		body = append(body, chunk.Payload...)
		if chunk.Error != "" {
			return nil, fmt.Errorf("read Ollama Cloud error stream: %s", redact.Text(chunk.Error))
		}
		if chunk.Done {
			return body, nil
		}
	}
}

// pumpStream forwards upstream SSE frames to the host stream. ollama.com already speaks the
// client's protocol, so frames are not translated, only re-framed to what each CLIProxyAPI
// handler expects (see outputChunks).
func (s *Service) pumpStream(outputID, format string, upstream transport.Stream) {
	ctx := context.Background()
	var terminalErr error
	defer func() {
		_ = s.host.CloseStream(ctx, upstream.ID)
		message := ""
		if terminalErr != nil {
			message = redact.Text(terminalErr.Error())
		}
		s.host.CloseOutput(ctx, outputID, message)
	}()

	decoder := &sse.Decoder{}
	emit := func(frame []byte) error {
		for _, chunk := range outputChunks(format, frame) {
			if errEmit := s.host.Emit(ctx, outputID, chunk); errEmit != nil {
				return errEmit
			}
		}
		return nil
	}
	for {
		chunk, errRead := s.host.ReadStream(ctx, upstream.ID)
		if errRead != nil {
			terminalErr = fmt.Errorf("read Ollama Cloud stream: %w", errRead)
			return
		}
		if chunk.Error != "" {
			terminalErr = fmt.Errorf("Ollama Cloud stream error: %s", redact.Text(chunk.Error))
			return
		}
		for _, frame := range decoder.Feed(chunk.Payload) {
			if errEmit := emit(frame); errEmit != nil {
				terminalErr = fmt.Errorf("forward Ollama Cloud stream: %w", errEmit)
				return
			}
		}
		if chunk.Done {
			if trailing := decoder.Flush(); len(trailing) > 0 {
				if errEmit := emit(trailing); errEmit != nil {
					terminalErr = fmt.Errorf("forward final Ollama Cloud frame: %w", errEmit)
				}
			}
			return
		}
	}
}

// outputChunks converts one upstream SSE frame into the chunk shape the host handler for the
// format expects. The chat completions handler prefixes every chunk with "data: " and appends
// its own [DONE], so it receives bare JSON payloads; the Claude and Responses handlers re-frame
// SSE themselves and receive the frame as-is.
func outputChunks(format string, frame []byte) [][]byte {
	switch format {
	case formatOpenAI:
		payload := sseDataPayload(frame)
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return nil
		}
		return [][]byte{payload}
	default:
		if len(bytes.TrimSpace(frame)) == 0 {
			return nil
		}
		return [][]byte{frame}
	}
}

// sseDataPayload joins the data lines of one SSE frame, dropping comments and event names.
func sseDataPayload(frame []byte) []byte {
	var payload []byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		value := bytes.TrimSpace(line[len("data:"):])
		if len(payload) > 0 {
			payload = append(payload, '\n')
		}
		payload = append(payload, value...)
	}
	return bytes.TrimSpace(payload)
}

// prepareBody pins the upstream model name and stream flag on the client payload.
func prepareBody(payload []byte, model string, stream bool, format string) ([]byte, error) {
	if len(bytes.TrimSpace(payload)) == 0 || !json.Valid(payload) {
		return nil, statusError("invalid_request", "request body must be a JSON object", http.StatusBadRequest)
	}
	out := payload
	if name := baseModelName(model); name != "" {
		updated, errSet := sjson.SetBytes(out, "model", name)
		if errSet != nil {
			return nil, statusError("invalid_request", "request body must be a JSON object", http.StatusBadRequest)
		}
		out = updated
	}
	updated, errStream := sjson.SetBytes(out, "stream", stream)
	if errStream != nil {
		return nil, statusError("invalid_request", "request body must be a JSON object", http.StatusBadRequest)
	}
	out = updated
	if stream && format == formatOpenAI && !gjson.GetBytes(out, "stream_options").Exists() {
		if withUsage, errUsage := sjson.SetBytes(out, "stream_options.include_usage", true); errUsage == nil {
			out = withUsage
		}
	}
	return out, nil
}

// baseModelName strips a CLIProxyAPI thinking suffix such as "glm-5.3(high)".
func baseModelName(model string) string {
	model = strings.TrimSpace(model)
	if open := strings.Index(model, "("); open > 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open])
	}
	return model
}

// CountTokens returns a local estimate; ollama.com has no token counting endpoint.
func (s *Service) CountTokens(req ExecuteRequest) (pluginapi.ExecutorResponse, error) {
	format := normalizeFormat(firstNonEmpty(req.SourceFormat, req.Format))
	count := estimateTokens(req.Payload)
	var payload []byte
	switch format {
	case formatClaude:
		payload = []byte(fmt.Sprintf(`{"input_tokens":%d}`, count))
	default:
		payload = []byte(fmt.Sprintf(`{"input_tokens":%d,"total_tokens":%d}`, count, count))
	}
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	return pluginapi.ExecutorResponse{Payload: payload, Headers: headers, Metadata: map[string]any{"estimated": true}}, nil
}

// estimateTokens approximates prompt tokens as one token per four characters of string content
// plus a small per-message overhead.
func estimateTokens(payload []byte) int64 {
	if !json.Valid(payload) {
		return 0
	}
	var characters int64
	var messages int64
	var walk func(result gjson.Result)
	walk = func(result gjson.Result) {
		switch {
		case result.IsObject():
			result.ForEach(func(key, value gjson.Result) bool {
				if key.String() == "role" {
					messages++
				}
				walk(value)
				return true
			})
		case result.IsArray():
			result.ForEach(func(_, value gjson.Result) bool {
				walk(value)
				return true
			})
		case result.Type == gjson.String:
			characters += int64(utf8.RuneCountInString(result.String()))
		}
	}
	walk(gjson.ParseBytes(payload))
	tokens := characters/4 + messages*4
	if tokens == 0 && characters > 0 {
		tokens = 1
	}
	return tokens
}

// HTTP performs an arbitrary authenticated request inside the credential's origin.
func (s *Service) HTTP(ctx context.Context, req HTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.ExecutorHTTPResponse{}, statusError("invalid_credential", errParse.Error(), http.StatusUnauthorized)
	}
	creds, errCreds := s.credentialsFor(storage)
	if errCreds != nil {
		return pluginapi.ExecutorHTTPResponse{}, errCreds
	}
	target, errURL := url.Parse(strings.TrimSpace(req.URL))
	if errURL != nil || target.Hostname() == "" {
		return pluginapi.ExecutorHTTPResponse{}, statusError("invalid_request", "executor HTTP URL is invalid", http.StatusBadRequest)
	}
	base, _ := url.Parse(creds.baseURL)
	if base == nil || !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
		return pluginapi.ExecutorHTTPResponse{}, statusError("forbidden_url", "executor HTTP URL is outside the Ollama Cloud origin", http.StatusForbidden)
	}
	method := strings.ToUpper(firstNonEmpty(req.Method, http.MethodGet))
	uri, authorization, errAuth := creds.authorize(method, target.Path, target.Query(), s.now)
	if errAuth != nil {
		return pluginapi.ExecutorHTTPResponse{}, errAuth
	}
	headers := cloneHeader(req.Headers)
	headers.Del("Authorization")
	headers.Set("Authorization", authorization)
	headers.Set("User-Agent", s.userAgent())
	if headers.Get("Accept") == "" {
		headers.Set("Accept", "application/json")
	}
	resp, errDo := s.host.Do(ctx, req.HostCallbackID, transport.Request{
		Method:  method,
		URL:     creds.baseURL + uri,
		Headers: headers,
		Body:    append([]byte(nil), req.Body...),
	})
	if errDo != nil {
		return pluginapi.ExecutorHTTPResponse{}, errDo
	}
	return pluginapi.ExecutorHTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    filterResponseHeaders(resp.Headers),
		Body:       resp.Body,
	}, nil
}
