# Hydra

A reverse proxy that exposes Google's Antigravity (Gemini) models through
OpenAI- and Anthropic-compatible APIs. Bind multiple Google accounts, let
Hydra handle OAuth refresh and quota-aware load balancing, and point any
OpenAI/Anthropic-compatible client at it.

## Features

- **OpenAI-compatible API** — `/v1/chat/completions`, `/v1/models`
- **Anthropic-compatible API** — `/v1/messages`
- **Multi-account pooling** — bind multiple Google accounts; Hydra rotates
  across them with cache-aware, balance, or performance scheduling
- **Quota protection** — automatically disables models on accounts that
  hit quota thresholds, re-enables when quota recovers
- **API key management** — per-key usage tracking, rotate/enable/disable
- **TUI dashboard** — accounts, logs, models, keys, and usage stats in a
  terminal UI
- **Request logging** — SQLite-backed log of every request with token
  breakdown and cost estimation
- **Prometheus metrics** — `/metrics` endpoint for monitoring
- **JSON mode** — OpenAI `response_format: {type: "json_object"}` support
- **Vision & audio** — image and audio input via OpenAI content parts

## Install

### Homebrew (macOS)

```bash
brew tap deungjaho/hydra
brew install hydra
```

### AUR (Arch Linux)

```bash
yay -S hydra-proxy
```

### Build from source

```bash
go build ./cmd/hydra
```

## Quick start

1. **Bind a Google account** (starts OAuth flow):
   ```bash
   hydra accounts add
   ```

2. **Start the proxy** (auto-creates a default API key on first run):
   ```bash
   hydra serve
   ```

3. **Point your client at it:**
   ```bash
   curl http://127.0.0.1:18045/v1/chat/completions \
     -H "Authorization: Bearer <your-api-key>" \
     -H "Content-Type: application/json" \
     -d '{"model":"gemini-3.6-flash-high","messages":[{"role":"user","content":"hi"}]}'
   ```

4. **Open the TUI dashboard:**
   ```bash
   hydra
   ```

## Commands

| Command | Description |
|---|---|
| `hydra` | Launch TUI dashboard |
| `hydra serve` | Start the proxy server |
| `hydra accounts add` | Bind a Google account via OAuth |
| `hydra accounts list` | List bound accounts |
| `hydra key add <label>` | Create a new API key |
| `hydra key list` | List API keys with usage |
| `hydra key rotate <id>` | Rotate an API key (old key invalidated) |
| `hydra key show <id>` | Print full key string |
| `hydra quota` | Refresh quota from Google's API |
| `hydra usage [-w day\|week\|month\|all]` | Show usage/cost stats |
| `hydra logs` | Show recent request logs |
| `hydra config` | Show or reset config |

## Configuration

Config lives at `~/.config/hydra/config.toml`. Environment variables
override config file values:

| Env var | Config key | Default |
|---|---|---|
| `HYDRA_PORT` | `proxy.port` | `18045` |
| `HYDRA_BIND` | `proxy.bind` | `127.0.0.1` |
| `HYDRA_UPSTREAM_PROXY` | `proxy.upstream_proxy` | (none) |
| `HYDRA_LOG_REQUESTS` | `proxy.log_requests` | `true` |
| `HYDRA_SCHEDULING_MODE` | `scheduling.mode` | `balance` |
| `HYDRA_OAUTH_CLIENT_ID` | — | Antigravity's client ID |
| `HYDRA_OAUTH_CLIENT_SECRET` | — | Antigravity's client secret |

Scheduling modes:
- **cache** — sticky to one account per session (maximizes cache hits)
- **balance** — sticky with switching on quota/degradation
- **performance** — power-of-two-choices random sampling

## TUI keybindings

| Key | Action |
|---|---|
| `Tab` / `1-5` | Switch tabs |
| `j` / `k` | Navigate rows |
| `Ctrl+d` / `Ctrl+u` | Scroll (Logs tab) |
| `Space` | Log detail overlay (Logs tab) |
| `a` | Add key (Keys tab) |
| `K` | Rotate key (Keys tab) |
| `s` | Show full key (Keys tab) |
| `D` | Delete row |
| `e` | Enable/disable |
| `r` | Refresh quota |
| `w` | Cycle time window (Status tab) |
| `m` | Cycle scheduling mode (Status tab) |
| `q` | Quit |

## OAuth credentials

Hydra reuses the Google OAuth client ID and secret from the Antigravity
desktop application. OAuth desktop clients use a "public" client type
where the secret is embedded in the client binary and is not truly
confidential. See `internal/account/oauth.go` for details.

## License

MIT
