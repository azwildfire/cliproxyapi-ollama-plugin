package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/redact"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// QuotaFetchRequest is the quota.fetch RPC payload.
type QuotaFetchRequest struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// QuotaResetRequest is the quota.reset RPC payload.
type QuotaResetRequest struct {
	pluginapi.QuotaResetRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// balanceResponse is GET /api/balance. ollama.com has served two shapes: the current one with a
// dollar allowance per billing period, and an older one with session/weekly windows.
type balanceResponse struct {
	Included  *includedBalance  `json:"included"`
	Purchased *purchasedBalance `json:"purchased"`
}

type includedBalance struct {
	BalanceUSD   *float64       `json:"balance_usd"`
	AllowanceUSD *float64       `json:"allowance_usd"`
	Period       *balancePeriod `json:"period"`
	Session      *legacyWindow  `json:"session"`
	Weekly       *legacyWindow  `json:"weekly"`
	Monthly      *legacyWindow  `json:"monthly"`
}

type balancePeriod struct {
	From  string `json:"from"`
	Until string `json:"until"`
}

type legacyWindow struct {
	RemainingPercent *float64 `json:"remaining_percent"`
	Usage            *float64 `json:"usage"`
	ResetsAt         string   `json:"resets_at"`
}

type purchasedBalance struct {
	BalanceUSD float64 `json:"balance_usd"`
}

// usageResponse is GET /api/usage: a request histogram with totals for the selected range.
type usageResponse struct {
	Range       string        `json:"range"`
	Scope       string        `json:"scope"`
	Granularity string        `json:"granularity"`
	From        string        `json:"from"`
	Until       string        `json:"until"`
	Totals      usageMetrics  `json:"totals"`
	Buckets     []usageBucket `json:"buckets"`
}

type usageMetrics struct {
	RequestCount      int64   `json:"request_count"`
	UsageUSD          float64 `json:"usage_usd"`
	InputTokens       int64   `json:"input_tokens"`
	CachedInputTokens int64   `json:"cached_input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
}

type usageBucket struct {
	usageMetrics
	From    string `json:"from"`
	Until   string `json:"until"`
	Partial bool   `json:"partial"`
}

type quotaCacheEntry struct {
	expiresAt time.Time
	identity  string
	response  pluginapi.QuotaFetchResponse
}

// DescribeQuota advertises this plugin as the quota source for the ollama provider.
func (s *Service) DescribeQuota() pluginapi.QuotaDescribeResponse {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{ProviderID},
		DisplayName:        "Ollama Cloud",
		SupportsReset:      false,
	}
}

// FetchQuota combines /api/balance, /api/usage, and /api/me into the normalized quota shape.
func (s *Service) FetchQuota(ctx context.Context, req QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	if len(req.StorageJSON) == 0 {
		return pluginapi.QuotaFetchResponse{}, statusError("invalid_request", "credential storage is not available for quota lookup", http.StatusBadRequest)
	}
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.QuotaFetchResponse{}, statusError("invalid_credential", errParse.Error(), http.StatusUnauthorized)
	}
	creds, errCreds := s.credentialsFor(storage)
	if errCreds != nil {
		return pluginapi.QuotaFetchResponse{}, errCreds
	}
	cfg := s.Config()
	identity := creds.identity()
	cacheKey := firstNonEmpty(req.AuthID, req.AuthIndex, identity)
	now := s.now()
	s.quotaMu.Lock()
	cached, ok := s.quotaEntries[cacheKey]
	s.quotaMu.Unlock()
	if ok && cached.identity == identity && cached.expiresAt.After(now) {
		return cached.response, nil
	}

	balanceResp, errBalance := s.call(ctx, s.host, req.HostCallbackID, creds, apiCall{method: http.MethodGet, path: "/api/balance"})
	if errBalance != nil {
		return pluginapi.QuotaFetchResponse{}, errBalance
	}
	if balanceResp.StatusCode < 200 || balanceResp.StatusCode >= 300 {
		return pluginapi.QuotaFetchResponse{}, upstreamStatusError(balanceResp.StatusCode, redact.ErrorBody(balanceResp.Body, creds.secrets()...))
	}
	var balance balanceResponse
	if errUnmarshal := json.Unmarshal(balanceResp.Body, &balance); errUnmarshal != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("decode Ollama Cloud balance: %w", errUnmarshal)
	}
	serverOffsetMs := serverTimeOffset(balanceResp.Headers, now)

	var usage *usageResponse
	usageResp, errUsage := s.call(ctx, s.host, req.HostCallbackID, creds, apiCall{method: http.MethodGet, path: "/api/usage", query: url.Values{"range": {cfg.UsageRange}}})
	if errUsage == nil && usageResp.StatusCode >= 200 && usageResp.StatusCode < 300 {
		var decoded usageResponse
		if json.Unmarshal(usageResp.Body, &decoded) == nil {
			usage = &decoded
		}
	}

	plan := storage.Plan
	if user, _, errWhoami := s.whoami(ctx, s.host, req.HostCallbackID, creds); errWhoami == nil && user.Plan != "" {
		plan = user.Plan
	}

	response := buildQuota(plan, &balance, usage, cfg.UsageRange, serverOffsetMs)
	if cfg.quotaCacheTTL() > 0 {
		s.quotaMu.Lock()
		s.quotaEntries[cacheKey] = quotaCacheEntry{expiresAt: now.Add(cfg.quotaCacheTTL()), identity: identity, response: response}
		s.quotaMu.Unlock()
	}
	return response, nil
}

// ResetQuota is not supported: Ollama Cloud allowances reset on their own schedule.
func (s *Service) ResetQuota(_ context.Context, _ QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	return pluginapi.QuotaResetResponse{Success: false, Message: "Ollama Cloud quota cannot be reset from the proxy"}, nil
}

func buildQuota(plan string, balance *balanceResponse, usage *usageResponse, usageRange string, serverOffsetMs int64) pluginapi.QuotaFetchResponse {
	out := pluginapi.QuotaFetchResponse{ServerTimeOffsetMs: serverOffsetMs}
	plan = strings.ToLower(strings.TrimSpace(plan))
	if plan != "" {
		out.Subscription = &pluginapi.QuotaSubscription{Plan: plan, TierName: titleCase(plan), TierID: plan}
	}

	var summary []pluginapi.QuotaMetric
	var groups []pluginapi.QuotaGroup
	if balance != nil && balance.Included != nil {
		included := balance.Included
		if included.BalanceUSD != nil {
			summary = append(summary, currencyMetric("included_remaining", "Included remaining", *included.BalanceUSD))
		}
		if included.AllowanceUSD != nil {
			summary = append(summary, currencyMetric("included_allowance", periodLabel(included.Period)+" allowance", *included.AllowanceUSD))
		}
		if included.BalanceUSD != nil && included.AllowanceUSD != nil && *included.AllowanceUSD > 0 {
			bucket := pluginapi.QuotaBucket{
				Window:            periodWindow(included.Period, "monthly"),
				RemainingFraction: clampFraction(*included.BalanceUSD / *included.AllowanceUSD),
				Description:       fmt.Sprintf("$%.2f of $%.2f included usage remaining", math.Max(*included.BalanceUSD, 0), *included.AllowanceUSD),
			}
			if included.Period != nil {
				bucket.ResetTime = strings.TrimSpace(included.Period.Until)
			}
			groups = append(groups, pluginapi.QuotaGroup{DisplayName: "Plan allowance", Buckets: []pluginapi.QuotaBucket{bucket}})
		}
		var legacy []pluginapi.QuotaBucket
		for _, window := range []struct {
			name  string
			value *legacyWindow
		}{{"session", included.Session}, {"weekly", included.Weekly}, {"monthly", included.Monthly}} {
			if bucket, ok := legacyBucket(window.name, window.value); ok {
				legacy = append(legacy, bucket)
			}
		}
		if len(legacy) > 0 {
			groups = append(groups, pluginapi.QuotaGroup{DisplayName: "Rate limits", Buckets: legacy})
		}
	}
	if balance != nil && balance.Purchased != nil {
		summary = append(summary, currencyMetric("purchased_credits", "Purchased credits", balance.Purchased.BalanceUSD))
	}
	if usage != nil {
		label := strings.TrimSpace(firstNonEmpty(usage.Range, usageRange))
		summary = append(summary,
			currencyMetric("usage_"+label, "Usage ("+label+")", usage.Totals.UsageUSD),
			numberMetric("requests_"+label, "Requests ("+label+")", float64(usage.Totals.RequestCount)),
			numberMetric("input_tokens_"+label, "Input tokens ("+label+")", float64(usage.Totals.InputTokens)),
			numberMetric("output_tokens_"+label, "Output tokens ("+label+")", float64(usage.Totals.OutputTokens)),
		)
	}
	out.Summary = summary
	out.Groups = groups
	return out
}

// periodWindow names a bucket window by its exact length ("30d", "168h") so the management
// center's timeline can draw it; the window string is the only field the host forwards for that.
func periodWindow(period *balancePeriod, fallback string) string {
	if period == nil {
		return fallback
	}
	from, errFrom := time.Parse(time.RFC3339, strings.TrimSpace(period.From))
	until, errUntil := time.Parse(time.RFC3339, strings.TrimSpace(period.Until))
	if errFrom != nil || errUntil != nil || !until.After(from) {
		return fallback
	}
	hours := until.Sub(from).Hours()
	if days := hours / 24; days >= 1 && math.Abs(days-math.Round(days)) < 0.01 {
		return fmt.Sprintf("%dd", int(math.Round(days)))
	}
	return fmt.Sprintf("%dh", int(math.Round(hours)))
}

func legacyBucket(name string, window *legacyWindow) (pluginapi.QuotaBucket, bool) {
	if window == nil {
		return pluginapi.QuotaBucket{}, false
	}
	var remaining float64
	switch {
	case window.RemainingPercent != nil:
		remaining = *window.RemainingPercent / 100
	case window.Usage != nil:
		remaining = 1 - *window.Usage
	default:
		return pluginapi.QuotaBucket{}, false
	}
	return pluginapi.QuotaBucket{
		Window:            legacyWindowName(name),
		RemainingFraction: clampFraction(remaining),
		ResetTime:         strings.TrimSpace(window.ResetsAt),
		Description:       titleCase(name) + " window",
	}, true
}

// legacyWindowName maps ollama.com's older window keys to lengths the timeline understands.
func legacyWindowName(name string) string {
	switch name {
	case "session":
		return "5h"
	case "weekly":
		return "weekly"
	case "monthly":
		return "30d"
	default:
		return name
	}
}

func currencyMetric(key, label string, value float64) pluginapi.QuotaMetric {
	return pluginapi.QuotaMetric{Key: key, Label: label, Value: roundTo(value, 4), Format: "currency", Currency: "USD"}
}

func numberMetric(key, label string, value float64) pluginapi.QuotaMetric {
	return pluginapi.QuotaMetric{Key: key, Label: label, Value: value, Format: "number"}
}

func periodLabel(period *balancePeriod) string {
	if period == nil {
		return "Plan"
	}
	from, errFrom := time.Parse(time.RFC3339, strings.TrimSpace(period.From))
	until, errUntil := time.Parse(time.RFC3339, strings.TrimSpace(period.Until))
	if errFrom != nil || errUntil != nil {
		return "Plan"
	}
	days := until.Sub(from).Hours() / 24
	switch {
	case days >= 27 && days <= 32:
		return "Monthly"
	case days >= 6 && days <= 8:
		return "Weekly"
	case days >= 360:
		return "Yearly"
	default:
		return fmt.Sprintf("%d-day", int(math.Round(days)))
	}
}

func clampFraction(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(0, math.Min(1, value))
}

func roundTo(value float64, decimals int) float64 {
	factor := math.Pow(10, float64(decimals))
	return math.Round(value*factor) / factor
}

func titleCase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func serverTimeOffset(headers http.Header, now time.Time) int64 {
	if headers == nil {
		return 0
	}
	dateHeader := headers.Get("Date")
	if dateHeader == "" {
		return 0
	}
	parsed, errParse := http.ParseTime(dateHeader)
	if errParse != nil {
		return 0
	}
	return parsed.Sub(now).Milliseconds()
}
