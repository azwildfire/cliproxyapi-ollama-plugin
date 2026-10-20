package provider

import (
	"encoding/json"
	"math"
	"testing"
)

const sampleBalance = `{"included":{"balance_usd":57.54899,"allowance_usd":60,"period":{"from":"2026-09-29T06:58:24Z","until":"2026-10-29T06:58:24Z"}},"purchased":{"balance_usd":3.32019}}`

const sampleUsage = `{"range":"7d","scope":"self","granularity":"day","from":"2026-10-13T00:00:00Z","until":"2026-10-20T16:14:05Z","totals":{"request_count":50,"usage_usd":0.21362,"input_tokens":252709,"cached_input_tokens":130401,"output_tokens":4265},"buckets":[{"from":"2026-10-20T00:00:00Z","until":"2026-10-20T16:14:05Z","partial":true,"request_count":0,"usage_usd":0,"input_tokens":0,"cached_input_tokens":0,"output_tokens":0}]}`

const legacyBalance = `{"included":{"session":{"remaining_percent":89.7,"resets_at":"2026-10-20T20:00:00Z"},"weekly":{"remaining_percent":40,"resets_at":"2026-10-25T00:00:00Z"}},"purchased":{"balance_usd":0}}`

func TestBuildQuotaCurrentShape(t *testing.T) {
	var balance balanceResponse
	if err := json.Unmarshal([]byte(sampleBalance), &balance); err != nil {
		t.Fatalf("unmarshal balance: %v", err)
	}
	var usage usageResponse
	if err := json.Unmarshal([]byte(sampleUsage), &usage); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	quota := buildQuota("pro", &balance, &usage, "7d", 250)

	if quota.Subscription == nil || quota.Subscription.Plan != "pro" || quota.Subscription.TierName != "Pro" {
		t.Fatalf("unexpected subscription %+v", quota.Subscription)
	}
	if quota.ServerTimeOffsetMs != 250 {
		t.Fatalf("server offset not propagated: %d", quota.ServerTimeOffsetMs)
	}
	if len(quota.Groups) != 1 || quota.Groups[0].DisplayName != "Plan allowance" || len(quota.Groups[0].Buckets) != 1 {
		t.Fatalf("unexpected groups %+v", quota.Groups)
	}
	bucket := quota.Groups[0].Buckets[0]
	if math.Abs(bucket.RemainingFraction-57.54899/60) > 1e-9 {
		t.Fatalf("remaining fraction %f", bucket.RemainingFraction)
	}
	if bucket.ResetTime != "2026-10-29T06:58:24Z" {
		t.Fatalf("reset time %q", bucket.ResetTime)
	}
	metrics := map[string]float64{}
	for _, metric := range quota.Summary {
		metrics[metric.Key] = metric.Value
		if metric.Label == "" {
			t.Fatalf("metric %s has no label", metric.Key)
		}
	}
	for key, want := range map[string]float64{
		"included_remaining": 57.549,
		"included_allowance": 60,
		"purchased_credits":  3.3202,
		"usage_7d":           0.2136,
		"requests_7d":        50,
		"input_tokens_7d":    252709,
		"output_tokens_7d":   4265,
	} {
		if got, ok := metrics[key]; !ok || math.Abs(got-want) > 1e-6 {
			t.Fatalf("metric %s = %v, want %v (present=%v)", key, got, want, ok)
		}
	}
	for _, metric := range quota.Summary {
		if metric.Format == "currency" && metric.Currency != "USD" {
			t.Fatalf("currency metric %s lacks USD", metric.Key)
		}
	}
	label := ""
	for _, metric := range quota.Summary {
		if metric.Key == "included_allowance" {
			label = metric.Label
		}
	}
	if label != "Monthly allowance" {
		t.Fatalf("allowance label %q", label)
	}

	raw, errMarshal := json.Marshal(quota)
	if errMarshal != nil {
		t.Fatalf("marshal quota: %v", errMarshal)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if _, ok := decoded["groups"].([]any); !ok {
		t.Fatalf("groups missing from JSON: %s", raw)
	}
}

func TestBuildQuotaLegacyWindows(t *testing.T) {
	var balance balanceResponse
	if err := json.Unmarshal([]byte(legacyBalance), &balance); err != nil {
		t.Fatalf("unmarshal balance: %v", err)
	}
	quota := buildQuota("free", &balance, nil, "7d", 0)
	if len(quota.Groups) != 1 || quota.Groups[0].DisplayName != "Rate limits" || len(quota.Groups[0].Buckets) != 2 {
		t.Fatalf("unexpected groups %+v", quota.Groups)
	}
	if got := quota.Groups[0].Buckets[0]; got.Window != "session" || math.Abs(got.RemainingFraction-0.897) > 1e-9 {
		t.Fatalf("session bucket %+v", got)
	}
	if got := quota.Groups[0].Buckets[1]; got.Window != "weekly" || math.Abs(got.RemainingFraction-0.4) > 1e-9 || got.ResetTime != "2026-10-25T00:00:00Z" {
		t.Fatalf("weekly bucket %+v", got)
	}
}

func TestBuildQuotaWithoutData(t *testing.T) {
	quota := buildQuota("", &balanceResponse{}, nil, "7d", 0)
	if quota.Subscription != nil || len(quota.Groups) != 0 || len(quota.Summary) != 0 {
		t.Fatalf("expected empty quota, got %+v", quota)
	}
}
