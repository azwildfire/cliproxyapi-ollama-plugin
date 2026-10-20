package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	flagLogin      = "ollama-login"
	flagAPIKey     = "ollama-api-key"
	flagDeviceName = "ollama-device-name"

	cliPollInterval = 3 * time.Second
)

// RegisterCommandLine declares the plugin-owned flags shown in CLIProxyAPI -help.
func (s *Service) RegisterCommandLine() pluginapi.CommandLineRegistrationResponse {
	return pluginapi.CommandLineRegistrationResponse{
		Flags: []pluginapi.CommandLineFlag{
			{Name: flagLogin, Usage: "Login to Ollama Cloud by connecting a new device key (prints an ollama.com/connect URL)", Type: "bool", DefaultValue: "false"},
			{Name: flagAPIKey, Usage: "Save an Ollama Cloud API key (https://ollama.com/settings/keys) as a credential", Type: "string", DefaultValue: ""},
			{Name: flagDeviceName, Usage: "Device name shown on ollama.com for -ollama-login", Type: "string", DefaultValue: ""},
		},
	}
}

// ExecuteCommandLine runs the login flows from a terminal. It talks to ollama.com directly
// (honouring the configured proxy URL) because no host HTTP callback exists in CLI mode.
func (s *Service) ExecuteCommandLine(ctx context.Context, req pluginapi.CommandLineExecutionRequest) (pluginapi.CommandLineExecutionResponse, error) {
	cfg := s.Config()
	if !cfg.Enabled {
		return pluginapi.CommandLineExecutionResponse{Stderr: []byte("Ollama Cloud plugin is disabled in plugins.configs\n"), ExitCode: 1}, nil
	}
	client, errClient := directRequesterFor(req.Host.ProxyURL)
	if errClient != nil {
		return pluginapi.CommandLineExecutionResponse{Stderr: []byte(errClient.Error() + "\n"), ExitCode: 1}, nil
	}
	deviceName := firstNonEmpty(flagValue(req, flagDeviceName), cfg.DeviceName, defaultDeviceName())
	now := s.now()

	if apiKey := flagValue(req, flagAPIKey); apiKey != "" {
		creds := credentials{apiKey: apiKey, baseURL: cfg.BaseURL}
		user, _, errWhoami := s.whoami(ctx, client, "", creds)
		if errWhoami != nil {
			return pluginapi.CommandLineExecutionResponse{Stderr: []byte("Ollama Cloud rejected the API key: " + errWhoami.Error() + "\n"), ExitCode: 1}, nil
		}
		session := loginSession{method: authMethodAPIKey, apiKey: apiKey, baseURL: cfg.BaseURL}
		data, errData := authData(s.storageForSession(session, user, now), "", "", "", "", false, nil, nil, now)
		if errData != nil {
			return pluginapi.CommandLineExecutionResponse{}, errData
		}
		return pluginapi.CommandLineExecutionResponse{
			Stdout: []byte(fmt.Sprintf("Saved Ollama Cloud API key for %s (%s plan)\n", firstNonEmpty(user.Name, user.Email), user.Plan)),
			Auths:  []pluginapi.AuthData{data},
		}, nil
	}

	if !flagEnabled(req, flagLogin) {
		return pluginapi.CommandLineExecutionResponse{Stdout: []byte("No Ollama Cloud action requested; use -ollama-login or -ollama-api-key <key>\n")}, nil
	}

	key, errGenerate := generateDeviceKey("cliproxyapi-ollama " + deviceName)
	if errGenerate != nil {
		return pluginapi.CommandLineExecutionResponse{}, errGenerate
	}
	connectURL := key.connectURL(cfg.BaseURL, deviceName)
	fmt.Fprintf(os.Stdout, "Open this URL in a browser, sign in to ollama.com, and approve the device %q:\n\n    %s\n\nWaiting for the device to be connected (timeout %s, Ctrl+C to abort)...\n", deviceName, connectURL, cfg.loginTimeout())

	creds := credentials{key: key, baseURL: cfg.BaseURL}
	deadline := now.Add(cfg.loginTimeout())
	for {
		user, status, errWhoami := s.whoami(ctx, client, "", creds)
		if errWhoami == nil {
			session := loginSession{method: authMethodDeviceKey, key: key, deviceName: deviceName, baseURL: cfg.BaseURL}
			data, errData := authData(s.storageForSession(session, user, s.now()), "", "", "", "", false, nil, nil, s.now())
			if errData != nil {
				return pluginapi.CommandLineExecutionResponse{}, errData
			}
			return pluginapi.CommandLineExecutionResponse{
				Stdout: []byte(fmt.Sprintf("Signed in to Ollama Cloud as %s (%s plan)\n", firstNonEmpty(user.Name, user.Email), user.Plan)),
				Auths:  []pluginapi.AuthData{data},
			}, nil
		}
		if status != 0 && status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusTooManyRequests {
			return pluginapi.CommandLineExecutionResponse{Stderr: []byte("Ollama Cloud login failed: " + errWhoami.Error() + "\n"), ExitCode: 1}, nil
		}
		if !s.now().Before(deadline) {
			return pluginapi.CommandLineExecutionResponse{Stderr: []byte("Timed out waiting for the device to be connected on ollama.com\n"), ExitCode: 1}, nil
		}
		select {
		case <-ctx.Done():
			return pluginapi.CommandLineExecutionResponse{Stderr: []byte("Login cancelled\n"), ExitCode: 1}, nil
		case <-time.After(cliPollInterval):
		}
	}
}

func flagValue(req pluginapi.CommandLineExecutionRequest, name string) string {
	if value, ok := req.TriggeredFlags[name]; ok && value.Set {
		return strings.TrimSpace(value.Value)
	}
	if value, ok := req.Flags[name]; ok && value.Set {
		return strings.TrimSpace(value.Value)
	}
	return ""
}

func flagEnabled(req pluginapi.CommandLineExecutionRequest, name string) bool {
	value := strings.ToLower(flagValue(req, name))
	return value == "true" || value == "1" || value == "yes"
}

// directRequester performs buffered HTTP requests without the host (CLI mode only).
type directRequester struct {
	client *http.Client
}

func directRequesterFor(proxyURL string) (directRequester, error) {
	transportCopy, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return directRequester{client: &http.Client{Timeout: 60 * time.Second}}, nil
	}
	httpTransport := transportCopy.Clone()
	if proxyURL = strings.TrimSpace(proxyURL); proxyURL != "" {
		parsed, errParse := url.Parse(proxyURL)
		if errParse != nil {
			return directRequester{}, fmt.Errorf("invalid proxy URL %q: %w", proxyURL, errParse)
		}
		httpTransport.Proxy = http.ProxyURL(parsed)
	}
	return directRequester{client: &http.Client{Transport: httpTransport, Timeout: 60 * time.Second}}, nil
}

func (d directRequester) Do(ctx context.Context, _ string, req transport.Request) (transport.Response, error) {
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, errReq := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if errReq != nil {
		return transport.Response{}, errReq
	}
	for key, values := range req.Headers {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}
	resp, errDo := d.client.Do(httpReq)
	if errDo != nil {
		return transport.Response{}, errDo
	}
	defer resp.Body.Close()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		return transport.Response{}, errRead
	}
	return transport.Response{StatusCode: resp.StatusCode, Headers: resp.Header.Clone(), Body: raw}, nil
}
