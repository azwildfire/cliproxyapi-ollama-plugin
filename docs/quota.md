# Quota

The plugin registers as the quota provider for the `ollama-cloud` provider key, so the management
center shows a quota card for every Ollama Cloud credential and the generic quota endpoints work:

```text
POST   /v0/management/plugins/cliproxyapi-ollama/quota   {"auth_index": "..."}
GET    /v0/management/plugins/cliproxyapi-ollama/quota?auth_index=...
```

Reset is not supported (`DELETE` answers that the quota cannot be reset from the proxy); included
usage resets with the billing period on ollama.com's side.

## Sources

Three ollama.com endpoints are combined, all authenticated with the credential:

| Endpoint | Used for |
| --- | --- |
| `GET /api/balance` | Included usage remaining and the allowance of the current billing period, purchased credits. Required. |
| `GET /api/usage?range=<usage_range>` | Request count, dollar value and token totals of the last 24h, 7d or 30d. Best effort. |
| `POST /api/me` | Plan name. Best effort; falls back to the plan stored at login. |

`/api/usage` is rate-limited to ten requests per minute per account, shared by every key and
device. Results are cached per credential for `quota_cache_seconds` (default 30) so a
"refresh all" in the UI does not burn the budget.

## Normalized shape

The response follows CLIProxyAPI's `QuotaFetchResponse`:

```json
{
  "subscription": {"plan": "pro", "tierName": "Pro", "tierId": "pro"},
  "summary": [
    {"key": "included_remaining", "label": "Included remaining", "value": 57.54, "format": "currency", "currency": "USD"},
    {"key": "included_allowance", "label": "Monthly allowance", "value": 60, "format": "currency", "currency": "USD"},
    {"key": "purchased_credits", "label": "Purchased credits", "value": 3.32, "format": "currency", "currency": "USD"},
    {"key": "usage_7d", "label": "Usage (7d)", "value": 0.22, "format": "currency", "currency": "USD"},
    {"key": "requests_7d", "label": "Requests (7d)", "value": 76, "format": "number"},
    {"key": "input_tokens_7d", "label": "Input tokens (7d)", "value": 300281, "format": "number"},
    {"key": "output_tokens_7d", "label": "Output tokens (7d)", "value": 8660, "format": "number"}
  ],
  "serverTimeOffsetMs": -84,
  "groups": [
    {
      "displayName": "Plan allowance",
      "buckets": [
        {
          "window": "allowance",
          "remainingFraction": 0.959,
          "resetTime": "2026-10-29T06:58:24Z",
          "description": "$57.54 of $60.00 included usage remaining"
        }
      ]
    }
  ]
}
```

- The **Plan allowance** meter is `balance_usd / allowance_usd` of the included usage; its reset
  time is the end of the billing period.
- Purchased credits have no cap, so they appear only as a summary metric.
- If ollama.com serves the older balance shape with `session`, `weekly` or `monthly` windows
  (`remaining_percent` or a 0-1 `usage` fraction and `resets_at`), those become buckets in a
  **Rate limits** group.
- The allowance label is derived from the period length (Monthly, Weekly, Yearly, or N-day).

## Errors

A 401 or 403 from ollama.com is reported as an upstream authorization error (the key was
disconnected or the API key revoked). A 429 is reported as retryable with the upstream message;
wait a minute and refresh again.
