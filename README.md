# CLIProxyAPI Ollama Cloud plugin

Licensed under the [MIT License](LICENSE).

An [Ollama Cloud](https://ollama.com/cloud) provider for
[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) v8, built against the
official `sdk/pluginabi` and `sdk/pluginapi` contracts (ABI version 1, registration schema 6).
It does for an Ollama account what the built-in Codex, Antigravity and Copilot integrations do
for theirs:

- **Sign in** with the same device-key flow as `ollama signin`: the plugin creates an ed25519 key,
  you approve it once at `https://ollama.com/connect`, and every request is signed with it. API
  keys from <https://ollama.com/settings/keys> and existing `~/.ollama/id_ed25519` keys work too.
- **Discover** the cloud model catalog of the signed-in account (`/api/tags`, enriched with context
  length and thinking levels from `/api/show`).
- **Serve** OpenAI chat completions, Claude Messages and OpenAI Responses requests straight to
  ollama.com's native endpoints, buffered and streaming. Gemini and any other format CLIProxyAPI
  knows are translated by the host.
- **Show quota** in the management center: plan, remaining included usage of the billing period
  with its reset time, purchased credits, and the request, token and dollar totals of the last
  24 hours, 7 days or 30 days.

See [docs/install-existing-deployment.md](docs/install-existing-deployment.md) to add it to a
running CLIProxyAPI, [docs/authentication.md](docs/authentication.md) for the three sign-in
paths, and [docs/quota.md](docs/quota.md) for what the quota card shows and where it comes from.

## Provider key

Credentials, models and the login flow use the provider key **`ollama-cloud`**. The key is
deliberately not `ollama`: CLIProxyAPI derives the provider key of an `openai-compatibility`
entry from its `name`, and "Ollama" is the obvious name for an entry that points at a local
Ollama server. The two would otherwise collide and requests would be routed to the wrong
executor.

Auth files are named `ollama-cloud-<account>.json` and carry `"type": "ollama-cloud"`.

## Architecture

`cmd/cliproxyapi-ollama` is the native entry point: the C ABI shim, the host callback transport,
and the JSON method dispatcher. It registers:

- `AuthProvider`: device-key, API-key and key-import logins; host-owned credential storage
- `ModelProvider`: per-credential discovery of the cloud catalog
- `ProviderExecutor`: passthrough execution for `openai`, `claude` and `openai-response`
- `QuotaProvider`: normalized quota from `/api/balance`, `/api/usage` and `/api/me`
- `ManagementAPI`: the **Ollama Cloud** sign-in and quota page in the management center menu
- `CommandLinePlugin`: `-ollama-login`, `-ollama-api-key`, `-ollama-device-name`

`internal/provider` holds the provider logic (`keys.go` signing, `login.go` flows, `models.go`
discovery, `executor.go` execution, `quota.go` quota, `cli.go` terminal login). `internal/sse`,
`internal/redact` and `internal/transport` are small support packages.

### Request signing

ollama.com authenticates a device the way the `ollama` binary does. For every request the plugin
adds `ts=<unix seconds>` to the query string, signs the string `METHOD,/path?query` with the
ed25519 key, and sends `<public-key>:<base64 signature>` in the `Authorization` header. The query
is encoded the way Go's `url.Values.Encode` does (sorted keys), because the server verifies the
signature over exactly that request URI. API keys are sent as `Authorization: Bearer <key>`.

All upstream traffic goes through CLIProxyAPI's host HTTP callbacks, so per-credential proxies
and request logging apply. The private key and the API key never leave the auth file; error
bodies are bounded and redacted.

## Build and test

Requirements: Docker. The production build runs inside `golang:1.26-bookworm`, the image the
official CLIProxyAPI binary is built with, so the shared library loads in the official
`debian:bookworm` runtime.

```sh
make test-docker   # go vet + go test inside the container
make build         # build/plugins/linux/amd64/cliproxyapi-ollama.so
make package VERSION=0.1.0   # dist/cliproxyapi-ollama_0.1.0_linux_amd64.zip + checksums.txt
```

`make test` runs the unit tests with a local Go 1.26 toolchain; `make build-local` builds without
Docker (a library built against a newer glibc may not load in the Bookworm image).

## Configuration

The plugin reads its block under `plugins.configs.cliproxyapi-ollama`. Every key except
`enabled` is optional; defaults are shown in
[config/plugin-config.example.yaml](config/plugin-config.example.yaml).

| Key | Default | Meaning |
| --- | --- | --- |
| `base_url` | `https://ollama.com` | Ollama Cloud endpoint for sign-in, models, inference and quota. |
| `device_name` | `CLIProxyAPI-<hostname>` | Device name shown on ollama.com when a key is connected. |
| `login_timeout_seconds` | `900` | Lifetime of a pending connect URL. |
| `model_cache_ttl_seconds` | `600` | In-memory lifetime of the discovered catalog. |
| `model_details` | `true` | Fetch context length and thinking levels from `/api/show`. |
| `quota_cache_seconds` | `30` | Minimum interval between quota fetches per credential. |
| `usage_range` | `7d` | Usage window for the quota card: `24h`, `7d` or `30d`. |
| `excluded_models` | `[]` | Model names or globs to hide, for example `gpt-oss:*`. |
| `allow_insecure_base_url` | `false` | Permit `http://` for a local mock server. |

`oauth.excluded-models.ollama-cloud` in the main configuration is honoured as well.

## Sign in

The management center gets an **Ollama Cloud** page in its plugin menu (served by the plugin at
`/v0/resource/plugins/cliproxyapi-ollama/signin`). It lists the connected accounts with their
quota and offers three ways to add one:

- **Sign in with ollama.com**: the web/device flow. Get a link, approve the device on
  ollama.com, done.
- **Use an API key**: paste a key from <https://ollama.com/settings/keys>.
- **Import an existing device key**: paste the `~/.ollama/id_ed25519` of a machine that already
  ran `ollama signin`.

The page reuses the management center login when "remember" was ticked at sign-in; otherwise it
asks for the management key once per tab. The generic **Ollama Cloud** login card on the Auth
Files page starts the web flow as well.

Equivalent API flow:

```sh
curl -H "Authorization: Bearer $MANAGEMENT_KEY" \
  "http://127.0.0.1:8317/v8/management/oauth/auth-url?provider=ollama-cloud"
# open the returned URL, then poll:
curl -H "Authorization: Bearer $MANAGEMENT_KEY" \
  "http://127.0.0.1:8317/v8/management/oauth/status?state=RETURNED_STATE"
```

Add `&api_key=...` to store an API key instead, or `&private_key=<base64 of the PEM>` to reuse
a key that was already connected with `ollama signin`. Details and the terminal flags are in
[docs/authentication.md](docs/authentication.md).

## Models and requests

After signing in, the catalog appears in every model listing CLIProxyAPI serves:

```sh
curl -H "Authorization: Bearer $API_KEY" http://127.0.0.1:8317/v1/models
```

Model IDs are the ollama.com names, for example `glm-5.3-flash`, `kimi-k3` or
`deepseek-v4.1-flash:1017`. Requests work in all three native formats and in anything the host
can translate:

```sh
curl http://127.0.0.1:8317/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","messages":[{"role":"user","content":"Reply with ok."}]}'

curl http://127.0.0.1:8317/v1/messages -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","max_tokens":64,"messages":[{"role":"user","content":"Reply with ok."}]}'

curl http://127.0.0.1:8317/v1/responses -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","input":"Reply with ok."}'
```

Reasoning controls pass through unchanged (`reasoning_effort` for chat completions,
`reasoning.effort` for Responses, `thinking` for Messages); the levels a model accepts are listed
in its description. `/v1/messages/count_tokens` returns a local estimate because ollama.com has
no counting endpoint.

## Quota

The management center's credential list and quota page show the normalized quota for every
`ollama-cloud` credential. The same data is available from the API:

```sh
curl -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" -H 'Content-Type: application/json' \
  -d '{"auth_index":"<auth_index from /v8/management/credentials>"}' \
  http://127.0.0.1:8317/v0/management/plugins/cliproxyapi-ollama/quota
```

ollama.com allows ten `/api/usage` calls per minute per account; the plugin caches quota per
credential for `quota_cache_seconds` to stay under that limit when the UI refreshes everything.

## Releases and the plugin store

Tag a dotted numeric version to publish a marketplace-compatible package:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The release workflow builds `cliproxyapi-ollama_0.1.0_linux_amd64.zip` (containing
`cliproxyapi-ollama.so` at its root) and `checksums.txt`, which is the layout the official
CLIProxyAPI Plugins Store installs from. The registry entry for a store submission is in
[docs/store-registry-entry.json](docs/store-registry-entry.json).

## Threat model and trust boundary

- A Go shared library is trusted, in-process code. Build it yourself or verify the release
  checksum before mounting it.
- The device private key is stored in CLIProxyAPI's auth directory with the same protection as
  every other credential there. Anyone who can read that directory can act as the connected
  device until it is disconnected at <https://ollama.com/settings>.
- The generic executor HTTP method rejects destinations outside the configured Ollama Cloud
  origin. Error bodies are bounded to 512 bytes and strip authorization values and API keys.
- `private_key_path` on the login endpoint reads a file on the proxy host. The management API is
  already an administrative surface, but keep the management key private.

## Limitations

- Embedding-only models are not published; only models with the `completion` capability are.
- Token counting is an estimate (about four characters per token plus a per-message overhead).
- Quota cannot be reset from the proxy; included usage resets with the billing period.
- Linux amd64 only, matching the official Docker image.
