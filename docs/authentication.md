# Authentication

Ollama Cloud recognises two kinds of credentials, and the plugin supports both.

## The management center page

Open **Ollama Cloud** in the management center's plugin menu. The page shows every connected
account with its quota and has three tabs: **Sign in with ollama.com** (web/device flow),
**Use an API key**, and **Import an existing device key**. Each tab drives the same host login
endpoints described below and keeps polling until the credential is saved, so no terminal is
needed.

The page is served by the plugin at `/v0/resource/plugins/cliproxyapi-ollama/signin`. It is
static and holds no secrets; every call it makes carries the management key. When the
management center was opened with "remember" ticked, the page reuses that saved login (the
center stores it, lightly obfuscated, in the browser's local storage on the same origin);
otherwise it asks for the management key once and keeps it in the tab's session storage.

## Device keys (the `ollama signin` flow)

This is what the `ollama` CLI and desktop app use. A device owns an ed25519 key; its public half
is linked to an account by visiting `https://ollama.com/connect?name=<device>&key=<public key>`
while signed in to ollama.com. From then on the device signs every request:

1. Append `ts=<unix seconds>` to the query string.
2. Sign `METHOD,/path?query` with the private key (the query is encoded with sorted keys).
3. Send `Authorization: <base64 public key>:<base64 signature>`.

The plugin runs the same flow through CLIProxyAPI's login machinery:

- **Start**: `GET /v8/management/oauth/auth-url?provider=ollama-cloud` (optionally
  `&device_name=...`) generates a new key, stores a pending session in memory and returns the
  connect URL plus a `state`.
- **Approve**: open the URL, sign in to ollama.com, confirm the device.
- **Poll**: `GET /v8/management/oauth/status?state=...` answers `wait` until the key is connected.
  The plugin checks `POST /api/me` at most every five seconds; ollama.com answers 200 with an
  empty account for an unconnected key, which is treated as pending. Once the account is
  returned the credential is saved as `ollama-cloud-<account>.json` and the status becomes `ok`.
- **Cancel**: `DELETE /v8/management/oauth/session?state=...`. Pending sessions also expire after
  `login_timeout_seconds` (default 15 minutes).

The management center shows the same flow as a login card; it opens the URL and polls for you.

The device appears on <https://ollama.com/settings> under the configured `device_name`.
Disconnecting it there revokes the credential; the plugin's periodic refresh notices the
rejection and marks the credential with an error.

### Reusing an existing key

If a machine already ran `ollama signin`, its key at `~/.ollama/id_ed25519` (or
`/root/.ollama/id_ed25519` inside the official Ollama container) can be imported instead of
creating a new device:

```sh
KEY_B64=$(base64 -w0 < ~/.ollama/id_ed25519)
curl -G -H "Authorization: Bearer $MANAGEMENT_KEY" \
  --data-urlencode "provider=ollama-cloud" \
  --data-urlencode "private_key=$KEY_B64" \
  http://127.0.0.1:8317/v8/management/oauth/auth-url
```

`private_key` accepts the PEM text itself or its base64 encoding. `private_key_path` reads a
file on the proxy host instead. The returned URL is the connect page for that key, which is only
needed if the key was never approved; an already-connected key completes on the first poll.

## API keys

Create a key at <https://ollama.com/settings/keys> and store it with:

```sh
curl -G -H "Authorization: Bearer $MANAGEMENT_KEY" \
  --data-urlencode "provider=ollama-cloud" \
  --data-urlencode "api_key=$OLLAMA_API_KEY" \
  http://127.0.0.1:8317/v8/management/oauth/auth-url
```

Polling the returned state validates the key against `POST /api/me` and saves
`ollama-cloud-<account>-apikey.json`. API keys are sent as `Authorization: Bearer <key>`.

A credential file can also be written by hand (or uploaded through the management center):

```json
{
  "type": "ollama-cloud",
  "api_key": "..."
}
```

## Terminal flags

The plugin registers command-line flags on the CLIProxyAPI binary:

```sh
./CLIProxyAPI -ollama-login                      # connect a new device key; prints the URL and waits
./CLIProxyAPI -ollama-login -ollama-device-name my-box
./CLIProxyAPI -ollama-api-key "$OLLAMA_API_KEY"  # store an API key
```

The terminal flows talk to ollama.com directly (honouring `proxy-url` from the configuration)
because no host HTTP callback exists outside the server, and they persist the credential through
the host like every other login command.

## What is stored

An auth file contains the provider-owned fields plus the metadata CLIProxyAPI merges in:

| Field | Meaning |
| --- | --- |
| `type` | Always `ollama-cloud`. |
| `auth_method` | `device_key` or `api_key`. |
| `private_key`, `public_key` | The device key in OpenSSH PEM form and its authorized_keys line. |
| `api_key` | The API key, for API-key credentials. |
| `device_name` | Name shown on ollama.com. |
| `user_id`, `user_name`, `email`, `plan` | Account details from `/api/me`, refreshed daily. |
| `base_url` | Only present when a non-default `base_url` was configured at login time. |

Treat the file like a password: it is sufficient to use the account's cloud models and spend
its included usage and credits.
