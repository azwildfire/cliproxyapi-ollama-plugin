# Install into an existing CLIProxyAPI deployment

This guide adds the Ollama Cloud plugin to a running CLIProxyAPI without touching its other
providers, keys or credentials. The plugin targets CLIProxyAPI v8 (built and tested against
v8.0.34), plugin ABI version 1, on Linux amd64.

## 1. Build the plugin

```sh
git clone https://github.com/azwildfire/cliproxyapi-ollama-plugin.git
cd cliproxyapi-ollama-plugin
make build
```

The library is written to:

```text
build/plugins/linux/amd64/cliproxyapi-ollama.so
```

CLIProxyAPI derives the plugin ID from the file name: `cliproxyapi-ollama.so` and
`cliproxyapi-ollama-v0.1.0.so` both register as `cliproxyapi-ollama`. The versioned name is what
the plugin store uses; use it when you install by hand so updates replace the right file.

## 2. Install the library

CLIProxyAPI scans `<plugins.dir>/linux/amd64` and `<plugins.dir>`.

### Docker or Docker Compose

Keep the plugin directory on the host and bind-mount it over the image's `plugins` directory.
If a plugin was already installed from the store (for example the GitHub Copilot plugin), copy
it out of the container first, because the mount hides the container's own copy:

```sh
PLUGINS=/path/to/cliproxyapi/plugins
install -d -m 0755 "$PLUGINS/linux/amd64"
docker cp cli-proxy-api:/CLIProxyAPI/plugins/linux/amd64/. "$PLUGINS/linux/amd64/" 2>/dev/null || true
install -m 0644 build/plugins/linux/amd64/cliproxyapi-ollama.so \
  "$PLUGINS/linux/amd64/cliproxyapi-ollama-v0.1.0.so"
```

Add the mount to the service and recreate it:

```yaml
services:
  cli-proxy-api:
    volumes:
      - /path/to/cliproxyapi/plugins:/CLIProxyAPI/plugins
```

```sh
docker compose up -d --no-deps cli-proxy-api
```

### Native CLIProxyAPI

```sh
sudo install -d -m 0755 /opt/cliproxyapi/plugins/linux/amd64
sudo install -m 0644 build/plugins/linux/amd64/cliproxyapi-ollama.so \
  /opt/cliproxyapi/plugins/linux/amd64/cliproxyapi-ollama-v0.1.0.so
```

and point `plugins.dir` at `/opt/cliproxyapi/plugins`.

## 3. Enable the plugin

A discovered plugin stays disabled until it has a configuration entry. Either merge this block
into `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cliproxyapi-ollama:
      enabled: true
```

or create the entry through the management API, which also triggers a reload:

```sh
curl -X PUT -H "Authorization: Bearer $MANAGEMENT_KEY" -H 'Content-Type: application/json' \
  -d '{"enabled":true}' \
  http://127.0.0.1:8317/v8/management/config/plugins/configs/cliproxyapi-ollama
```

Global `plugins.enabled` and `cliproxyapi-ollama.enabled` must both be true. The full list of
optional keys is in [../config/plugin-config.example.yaml](../config/plugin-config.example.yaml).

The startup log then contains:

```text
pluginhost: plugin loaded plugin_id=cliproxyapi-ollama version=0.1.0 ...
pluginhost: plugin registered plugin_id=cliproxyapi-ollama plugin_name=Ollama Cloud provider ...
```

## 4. Sign in

Open the management center, choose the **Ollama Cloud** login, open the URL, sign in to
ollama.com and approve the device. See [authentication.md](authentication.md) for the API key
and key-import variants and the terminal flags.

## 5. Verify

```sh
curl -fsS -H "Authorization: Bearer $MANAGEMENT_KEY" http://127.0.0.1:8317/v0/management/plugins
```

should list `cliproxyapi-ollama` with `registered: true`, `oauth_provider: "ollama-cloud"` and
`supports_quota: true`. After signing in:

```sh
curl -fsS -H "Authorization: Bearer $API_KEY" http://127.0.0.1:8317/v1/models
```

includes the account's cloud models (owner `ollama`), and the credential list shows a quota card
for the `ollama-cloud-<account>.json` credential.

## Updating or removing

To update, replace the `.so` with the new versioned file and restart CLIProxyAPI, or install the
new release from the plugin store once it is listed there.

To disable without deleting credentials:

```yaml
plugins:
  configs:
    cliproxyapi-ollama:
      enabled: false
```

To remove completely: stop CLIProxyAPI, delete the library, remove the `cliproxyapi-ollama`
configuration entry, delete the `ollama-cloud-*.json` auth files if the credentials should go
too, and disconnect the device at <https://ollama.com/settings> if it was created by the plugin.
