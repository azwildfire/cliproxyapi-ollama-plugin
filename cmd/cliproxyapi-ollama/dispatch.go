package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/provider"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// pluginVersion is replaced at build time with -ldflags "-X main.pluginVersion=<version>".
var pluginVersion = "0.2.1"

var pluginService = provider.New(hostTransport{})

const repositoryURL = "https://github.com/azwildfire/cliproxyapi-ollama-plugin"

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
	QuotaProvider         bool                         `json:"quota_provider"`
	CommandLinePlugin     bool                         `json:"command_line_plugin"`
	ManagementAPI         bool                         `json:"management_api"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcAuthLoginStartRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthLoginPollRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthModelRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func handleMethod(method string, request []byte) ([]byte, bool) {
	result, errHandle := dispatch(method, request)
	if errHandle != nil {
		var statusErr *provider.StatusError
		if errors.As(errHandle, &statusErr) {
			return errorEnvelope(statusErr.Code, statusErr.Message, statusErr.HTTPStatus, statusErr.Retryable), true
		}
		return errorEnvelope("plugin_error", errHandle.Error(), http.StatusInternalServerError, false), true
	}
	raw, errEnvelope := okEnvelope(result)
	if errEnvelope != nil {
		return errorEnvelope("encoding_error", errEnvelope.Error(), http.StatusInternalServerError, false), true
	}
	return raw, false
}

func dispatch(method string, request []byte) (any, error) {
	ctx := context.Background()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, errUnmarshal
			}
		}
		pluginService.SetVersion(pluginVersion)
		if errConfigure := pluginService.Configure(req.ConfigYAML); errConfigure != nil {
			return nil, errConfigure
		}
		return pluginRegistration(), nil
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return struct{}{}, nil
	case pluginabi.MethodModelStatic:
		return pluginService.StaticModels(), nil
	case pluginabi.MethodModelForAuth:
		var req rpcAuthModelRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.ModelsForAuth(ctx, req.HostCallbackID, req.AuthModelRequest)
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier, pluginabi.MethodQuotaIdentifier:
		return identifierResponse{Identifier: provider.ProviderID}, nil
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.ParseAuth(req)
	case pluginabi.MethodAuthLoginStart:
		var req rpcAuthLoginStartRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.StartLogin(ctx, req.HostCallbackID, req.AuthLoginStartRequest)
	case pluginabi.MethodAuthLoginPoll:
		var req rpcAuthLoginPollRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.PollLogin(ctx, req.HostCallbackID, req.AuthLoginPollRequest)
	case pluginabi.MethodAuthRefresh:
		var req rpcAuthRefreshRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.RefreshAuth(ctx, req.HostCallbackID, req.AuthRefreshRequest)
	case pluginabi.MethodExecutorExecute:
		var req provider.ExecuteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.Execute(ctx, req)
	case pluginabi.MethodExecutorExecuteStream:
		var req provider.ExecuteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		headers, errStream := pluginService.ExecuteStream(ctx, req)
		if errStream != nil {
			return nil, errStream
		}
		return map[string]any{"headers": headers}, nil
	case pluginabi.MethodExecutorCountTokens:
		var req provider.ExecuteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.CountTokens(req)
	case pluginabi.MethodExecutorHTTPRequest:
		var req provider.HTTPRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.HTTP(ctx, req)
	case pluginabi.MethodQuotaDescribe:
		return pluginService.DescribeQuota(), nil
	case pluginabi.MethodQuotaFetch:
		var req provider.QuotaFetchRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.FetchQuota(ctx, req)
	case pluginabi.MethodQuotaReset:
		var req provider.QuotaResetRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.ResetQuota(ctx, req)
	case pluginabi.MethodManagementRegister:
		return pluginService.RegisterManagement(), nil
	case pluginabi.MethodManagementHandle:
		var req provider.ManagementRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.HandleManagement(req), nil
	case pluginabi.MethodCommandLineRegister:
		return pluginService.RegisterCommandLine(), nil
	case pluginabi.MethodCommandLineExecute:
		var req pluginapi.CommandLineExecutionRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.ExecuteCommandLine(ctx, req)
	default:
		return nil, &provider.StatusError{
			Code:       "unknown_method",
			Message:    "unknown plugin method: " + method,
			HTTPStatus: http.StatusNotImplemented,
		}
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Ollama Cloud provider",
			Version:          pluginVersion,
			Author:           "azwildfire",
			GitHubRepository: repositoryURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "base_url", Type: pluginapi.ConfigFieldTypeString, Default: provider.DefaultBaseURL, Description: "Ollama Cloud base URL used for sign-in, model discovery, inference, and quota."},
				{Name: "device_name", Type: pluginapi.ConfigFieldTypeString, Description: "Device name shown on ollama.com when connecting a new device key. Defaults to CLIProxyAPI plus the host name."},
				{Name: "login_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Default: provider.DefaultLoginTimeoutSeconds, Description: "How long a pending device connection stays valid before the login must be restarted."},
				{Name: "model_cache_ttl_seconds", Type: pluginapi.ConfigFieldTypeInteger, Default: provider.DefaultModelCacheTTLSeconds, Description: "In-memory lifetime of the discovered Ollama Cloud model catalog."},
				{Name: "model_details", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Enrich each discovered model with context length and thinking levels from /api/show."},
				{Name: "quota_cache_seconds", Type: pluginapi.ConfigFieldTypeInteger, Default: provider.DefaultQuotaCacheSeconds, Description: "Minimum interval between quota fetches per credential; ollama.com rate-limits the usage endpoint to 10 requests per minute."},
				{Name: "usage_range", Type: pluginapi.ConfigFieldTypeEnum, Default: provider.DefaultUsageRange, EnumValues: []string{"24h", "7d", "30d"}, Description: "Reporting window for the usage metrics shown in the quota card."},
				{Name: "excluded_models", Type: pluginapi.ConfigFieldTypeArray, Description: "Model names or glob patterns (for example glm-*) to hide from discovery."},
				{Name: "allow_insecure_base_url", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Permit an http:// base URL for local testing against a mock server."},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  provider.SupportedFormats(),
			ExecutorOutputFormats: provider.SupportedFormats(),
			QuotaProvider:         true,
			CommandLinePlugin:     true,
			ManagementAPI:         true,
		},
	}
}

func okEnvelope(value any) ([]byte, error) {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string, status int, retryable bool) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
			Retryable:  retryable,
		},
	})
	return raw
}
