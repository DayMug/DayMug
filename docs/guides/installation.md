# Installation (Linux / macOS)

This guide covers the platforms the release binaries support: **Linux x86_64 / arm64** and **macOS arm64**. It has three parts:

1. [One-line install (production / trial)](#1-one-line-install-production--trial) — download a released binary, then install and start a user-level service (Linux systemd --user / macOS LaunchAgent).
2. [Building from source](#2-building-from-source) — compile the single static binary from source, or develop locally.
3. [Public access](#4-public-access) — expose the service externally (your own reverse proxy / LAN).

For a field-by-field config reference see the [configuration reference](configuration.md); a copyable template is in [config.sample.yaml](config.sample.yaml).

---

## 0. Prerequisites

**Common (required for every path)**

- **Node.js ≥ 18**, with the Claude Agent SDK installed globally:

  ```bash
  npm install -g @anthropic-ai/claude-agent-sdk
  ```

  The Agent SDK is the default transport for the `claude` provider, so this package is a hard dependency in that case (admins can switch to the CLI transport on the "Providers & models" page). The one-line installer offers to install it when Node.js and npm are present (it never installs Node.js itself); `daymug bootstrap` checks for it up front and prints the install command; `serve` strictly validates against the providers actually configured and their selected transports.
- At least one code CLI installed and logged in:
  - **Claude**: API credentials can be supplied via the provider's `env`. The local `claude` CLI is required when you log in interactively from the admin panel (`claude auth login`), switch the `claude` type to the CLI transport, or configure a `claude-compatible` account (which only runs over the CLI).
  - **Codex** (required when a Codex provider is configured): install and log in to `codex`; the version must support `codex app-server`. Check and install with `codex app-server --help` and `npm install -g @openai/codex`.
- Run as a regular user. **Do not run the install script as root** (the installer refuses).

**Additionally required for the one-line install**

- `curl`, `sha256sum` (or `shasum`).

**Additionally required for building from source**

- [Go](https://go.dev/) ≥ 1.25
- [Node.js](https://nodejs.org/) ≥ 20, [pnpm](https://pnpm.io/) ≥ 10
- Optional: [golangci-lint](https://golangci-lint.run/) (lint), [air](https://github.com/air-verse/air) (hot reload)

---

## 1. One-line install (production / trial)

### 1.1 Install

Latest release:

```bash
curl -fsSL https://github.com/DayMug/DayMug/releases/latest/download/install.sh | bash
```

Options are passed after `bash -s --` (or directly when running a downloaded `install.sh`):

```bash
curl -fsSL https://github.com/DayMug/DayMug/releases/latest/download/install.sh | bash -s -- v1.3.81          # pin a tag
curl -fsSL https://github.com/DayMug/DayMug/releases/latest/download/install.sh | bash -s -- --skip-service   # binary only, no service
curl -fsSL https://github.com/DayMug/DayMug/releases/latest/download/install.sh | bash -s -- --no-start       # register the service, don't start it
```

| Installer option | Effect |
|------|------|
| `vX.Y.Z` | Install this tag instead of the latest release |
| `--skip-service` / `--no-start` | Forwarded to `daymug bootstrap` (see below) |
| `-y`, `--yes` | Non-interactive: install the Claude Agent SDK with `npm install -g` if it is missing |
| `--no-deps` | Never install dependencies, only report them |

`DAYMUG_RELEASES_URL` points the installer at another repository's GitHub releases page (default `https://github.com/DayMug/DayMug/releases`), e.g. a fork's.

The install script:

1. Detects OS/architecture and fetches the release manifest.
2. Downloads the binary for your platform and verifies its SHA-256.
3. If the Claude Agent SDK is missing, asks whether to install it globally with npm (`--yes` installs without asking, `--no-deps` skips; without a terminal it only reports). It never uses sudo; on an `EACCES` error it suggests a user-level npm prefix.
4. Runs `daymug bootstrap`: prints a prerequisite report (Node.js, `@anthropic-ai/claude-agent-sdk`, Codex app-server, service manager), creates the `~/.daymug/` directory layout, and installs a user-level service. Missing items come with install hints; `serve` only enforces the runtimes the current config actually uses, so a Claude-only deployment does not need Codex and a Codex-only deployment does not need the Agent SDK. On Linux the systemd user unit is `enable`d and started immediately; on macOS the LaunchAgent is loaded immediately and starts automatically each time that user logs in. Finally it prints a short summary: what was installed, the service status, the access URL built from the actual port in `config.yaml`, and `daymug doctor` as the troubleshooting entry point.

`bootstrap` flags:

| Flag | Effect |
|------|------|
| `--skip-service` | Only create the directory layout, without registering a service (then run `~/.daymug/daymug serve` manually) |
| `--no-start` | Linux: write and load the unit, but do not `enable` or start it; no effect on macOS |
| `--force-config` | Overwrite an existing `config.yaml` |

Safe to re-run: the binary is overwritten and an existing `config.yaml` is kept; if the service is already running it is `restart`ed onto the new binary.

When `systemctl --user` is unavailable (containers, sessions entered via `su`, and other environments without a user session bus), `bootstrap` does not fail; it writes the unit and prints the commands to run later from a normal login session.

Layout after install:

```
~/.daymug/daymug            binary
~/.daymug/config.yaml       YAML config (auto-resolved)
~/.daymug/data/database.db  SQLite (auto-resolved)
~/.daymug/logs/daymug.log   log written by the service itself (rotated at 32 MiB, 5 files kept)
~/.daymug/users/            default home root for new human users
```

### 1.2 Edit the config

Unless you passed `--no-start` or `--skip-service`, the service is already running with the default config (listening on `127.0.0.1:8090`). Edit `~/.daymug/config.yaml` as needed, then restart the service (see 1.3). At least check:

- `admin.bootstrap_usernames` — usernames listed here are promoted to admin on every start (a safety net against locking yourself out of admin).

Provider accounts are not part of the config file. Once the service is running and an admin exists, add accounts under "Admin settings → Providers & models" (account menu; on a phone, Settings → Administration → Admin Panel). Claude-family accounts (`claude` / `claude-compatible`) may leave the credentials directory empty (falls back to `~/.claude`); Codex-family accounts (`codex` / `openai-compatible`) must set a `CODEX_HOME` directory.

See the [configuration reference](configuration.md) for all fields.

### 1.3 Service management

Linux (`bootstrap` has already enabled and started it; if you installed with `--no-start`, first run `systemctl --user enable --now daymug.service`):

```bash
systemctl --user status daymug.service
# Restart after changing the config
systemctl --user restart daymug.service
```

**Enable linger on unattended servers or servers you reach over SSH.** systemd user services hang off that user's user manager: without linger, the service is stopped once the last session (including SSH) exits, and it does not start at boot. `bootstrap` and `daymug doctor` warn when `loginctl show-user $USER -p Linger` is not `Linger=yes`; you need to run this once yourself (DayMug does not sudo on your behalf):

```bash
sudo loginctl enable-linger "$USER"
```

macOS:

```bash
# Already running after install; after each boot, log in as this user and the LaunchAgent starts it
# Check status / restart after changing the config:
launchctl print "gui/$(id -u)/com.daymug.daymug"
launchctl kickstart -k "gui/$(id -u)/com.daymug.daymug"
```

> A user-level LaunchAgent on macOS only runs after its owning user logs in; it does not start while the machine sits at the login screen. This is an OS limitation and the expected behaviour of this install method.

If you used `--skip-service`, run it manually:

```bash
~/.daymug/daymug serve
```

**Stopping**: press Ctrl-C in the foreground, or run `~/.daymug/daymug stop` in another terminal (it reads `data/daymug.pid` and only stops the instance started by this binary), or use `kill -INT <pid>`. **A bare `kill <pid>` / `pkill` (SIGTERM) is ignored and logged**: agent child processes run as the same OS user as the service, so a stray `pkill -f "daymug serve"` from an agent's shell (or one meant for a test instance) would otherwise take down the live service and every running session. `systemctl stop/restart` under systemd is unaffected (the service verifies the unit is actually `deactivating` and that it is the MainPID); the macOS LaunchAgent responds to SIGTERM as usual. With supervisors that only send SIGTERM, such as supervisord / pm2 / containers, set `DAYMUG_HONOR_SIGTERM=1` so that SIGTERM stops the service.

### 1.4 Uninstall

```bash
~/.daymug/daymug uninstall            # stop and remove the service (systemd unit / LaunchAgent), keep ~/.daymug
~/.daymug/daymug uninstall --purge    # also delete ~/.daymug (config, database, user home directories); asks for confirmation first
~/.daymug/daymug uninstall --purge --yes   # skip confirmation
```

`~/.daymug` is kept by default, so reinstalling picks up where you left off. `--purge` only deletes the directory containing the running binary, and only if that directory contains `config.yaml`, is not `$HOME` or one of its ancestors, and is not a git repository; it refuses while a `serve` process is still using the directory. When stdin is not a terminal (e.g. when piped), the confirmation is read from `/dev/tty`.

### 1.5 Create the first user / admin

Open the service URL in a browser; an empty database walks you through creating the first admin. Or use the CLI:

```bash
~/.daymug/daymug user add <username> --email you@example.com --admin
# Forgot password:
~/.daymug/daymug user passwd <username>
```

### 1.6 Self-upgrade

An admin clicks "Check for updates" under **Admin settings → Global** in the frontend: the backend fetches the manifest → verifies SHA-256 → atomically swaps the binary → a detached watchdog restarts the service and runs a health check → on failure it rolls back to `<bin>.bak` automatically. Or via the CLI:

```bash
daymug upgrade                 # upgrade to the latest release
daymug upgrade --version v0.9.0  # pin a tag (downgrades allowed)
daymug upgrade --rollback        # roll back to the previous binary
```

On macOS the web upgrade button works too: the LaunchAgent's `KeepAlive` lets the watchdog bring up the new version after the binary is replaced, keeping the health check and automatic rollback. The systemd user unit on Linux works the same way. If you installed with `--skip-service` and run `daymug serve` directly, the binary can still be downloaded and replaced, but with no service manager to handle the restart, the web upgrade is not guaranteed to complete.

---

## 2. Building from source

### 2.1 Production binary (single file with embedded frontend)

`make build` from the repo root does this with the release pipeline's flags (JS/CSS gzip-precompressed, `-X main.Version`) and writes `backend/bin/daymug`. The manual equivalent:

```bash
# 1. Build the frontend
cd frontend && pnpm install && pnpm build     # output -> frontend/dist/

# 2. Copy the frontend output into the backend and build with the embed tag
cd ..
rm -rf backend/cmd/server/dist
cp -r frontend/dist backend/cmd/server/dist
cd backend && go build -tags embed_frontend -o daymug ./cmd/server
```

The resulting `backend/daymug` is a static binary with no frontend runtime dependency. First deployment:

```bash
./daymug bootstrap        # create the ~/.daymug/ layout and register a user-level system service
# Or for a non-standard layout:
./daymug init             # generate config.yaml + data/ + users/ in the current directory
```

### 2.2 Local development (hot reload)

The repo-root `Makefile` wraps the steps below: `make dev-init` (writes `data/config.yaml` once, skips it if present), `make dev-backend` (air), `make dev-frontend` (Vite).

Backend (`backend/`):

```bash
# First time: generate config.yaml under data/ at the repo root (air reads ../data/config.yaml by default)
go build -o bin/server ./cmd/server
mkdir -p ../data && (cd ../data && ../backend/bin/server init)
air                        # watches .go changes, rebuilds and restarts; DAYMUG_CONFIG overrides the config path
# Without air:
DAYMUG_CONFIG=../data/config.yaml ./bin/server serve
```

`init` writes `server.addr` as `127.0.0.1:8090`, which is where the frontend dev server proxies by default, so no config edit is needed; if the backend listens elsewhere, start Vite with `DAYMUG_DEV_BACKEND=http://127.0.0.1:<port> pnpm dev`. SQLite follows the binary and lands in `backend/bin/data/database.db`. Do not use `go run ./cmd/server serve`: its binary lives in a temp directory, so it cannot find a `config.yaml` alongside it and the database is written into the temp directory too.

Frontend (`frontend/`):

```bash
pnpm install
pnpm dev                   # :5174, /api/* proxied to the backend at 127.0.0.1:8090 (including WS)
```

### 2.3 Verification pipeline (must pass after every change)

```bash
# backend/
go build ./...
go test ./...
golangci-lint run

# frontend/
pnpm build     # vue-tsc + production build
pnpm test      # Vitest
pnpm lint      # ESLint --fix
pnpm api-contract:check  # TS interfaces vs the Go contract snapshot
```

For debugging see the root [README.md](../../README.md#debugging) (Delve / VS Code launch.json).

---

## 3. Config file location

The service reads `--config <path>`, then `DAYMUG_CONFIG`, then `config.yaml` next to the binary, and refuses to start if none exists. The config is read once at startup, so restart the service after editing it. Full resolution rules and environment overrides: [Loading and precedence](configuration.md#loading-and-precedence).

---

## 4. Public access

By default DayMug only listens for plain HTTP on `127.0.0.1:<port>`. For external access you need to put your own public entry point in front of it (reverse proxy, tunnel, VPN, etc.). Two common setups follow.

### Path A — your own domain + nginx + your own certificate

1. Point DNS A/AAAA records at this machine's public IP.
2. Obtain a certificate (Let's Encrypt example):
   ```bash
   sudo apt install -y certbot python3-certbot-nginx
   sudo certbot --nginx -d chat.example.com
   ```
3. `/etc/nginx/sites-available/daymug` (replace the port with the one in `server.addr`):
   ```nginx
   server {
       listen 443 ssl http2;
       server_name chat.example.com;

       ssl_certificate     /etc/letsencrypt/live/chat.example.com/fullchain.pem;
       ssl_certificate_key /etc/letsencrypt/live/chat.example.com/privkey.pem;

       # nginx defaults to 1m, which rejects most uploads with 413. DayMug
       # accepts 25 MiB per chat attachment and 200 MiB per file-browser upload.
       client_max_body_size 200m;

       location / {
           proxy_pass http://127.0.0.1:8090;
           proxy_http_version 1.1;
           proxy_set_header Host              $host;
           proxy_set_header X-Real-IP         $remote_addr;
           proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
           proxy_set_header X-Forwarded-Proto $scheme;
           proxy_set_header Upgrade           $http_upgrade;
           proxy_set_header Connection        "upgrade";
           proxy_read_timeout 3600s;
           proxy_send_timeout 3600s;
       }
   }
   server {
       listen 80;
       server_name chat.example.com;
       return 301 https://$host$request_uri;
   }
   ```
4. On Linux run `sudo nginx -t && sudo systemctl reload nginx`. On macOS, if you manage TLS/reverse proxy yourself, reload according to your nginx/acme setup.

> ⚠️ Raise `client_max_body_size` (200m above). nginx's default is 1 MB, so without it attachments and file-browser uploads fail with `413 Request Entity Too Large` — other proxies need an equivalent body-size limit of at least 200 MB.

> ⚠️ Chat runs over WebSocket, so the `Upgrade`/`Connection` headers and a long `proxy_read_timeout` (≥3600s) are mandatory. The `proxy_set_header X-Forwarded-Proto $scheme` line above also decides whether the session cookie carries `Secure`: when forwarded, it is set automatically; only if your proxy does not forward it do you need to set `auth.cookie_secure` to `true` explicitly.

> Self-hosted tunnels (e.g. frp, Tailscale Funnel, your own cloudflared) work too, with the same requirements: keep the WebSocket upgrade, the long read timeout, and a body-size limit of at least 200 MB. Set `server.public_url` in `config.yaml` to the public address so deep links in push notifications (Bark) open correctly.

### Path B — local machine / LAN only

Open `http://127.0.0.1:<port>` directly in a browser. To allow other machines on the same LAN, change `server.addr` to `0.0.0.0:<port>` and restart (still unreachable from outside the LAN).

---

## 5. Troubleshooting

Start with a health check:

```bash
~/.daymug/daymug doctor
```

It reuses the `bootstrap` prerequisite report, then checks each item in turn: agent runtime (at least one of Claude Agent SDK / Codex app-server available), whether the config parses, whether the service is installed / enabled / active (Linux) or loaded (macOS), linger (Linux), and whether `/api/health` on `server.addr` responds. Each item is marked `✓` / `!` (warning, still runs) / `✗` (must fix), and failures come with a fix command; the exit code is non-zero if any `✗` is present.

| Symptom | What to check |
|------|----------|
| No response at `http://127.0.0.1:8090` | Run `daymug doctor`; if you installed with `--no-start`, run `systemctl --user enable --now daymug.service`; the actual port is `server.addr` in `config.yaml` |
| Service stopped after SSH disconnect / did not come up after reboot (Linux) | Linger is not enabled: `sudo loginctl enable-linger "$USER"`, then `systemctl --user start daymug.service` |
| `bootstrap` reports `systemctl --user is unavailable` | The current session has no user bus (container, `su`). Run the commands listed in the summary from a normal login session for that user (direct SSH login) |
| Startup fails with "a claude-compatible provider requires the Claude CLI" or "claude is set to the CLI transport, which requires the Claude CLI" | Both `claude-compatible` and the CLI transport depend on a local `claude`; install or upgrade to a version that supports `CLAUDE_CONFIG_DIR` |
| Startup reports the Claude Agent SDK is missing | Install it globally with `npm install -g @anthropic-ai/claude-agent-sdk`; if it lives in a non-standard path, point `DAYMUG_CLAUDE_AGENT_SDK_MODULE` at `sdk.mjs` |
| Startup reports Codex or app-server is missing | Run `npm install -g @openai/codex`, then confirm `codex app-server --help` works |
| Startup fails to read the config | Check the `--config` / `DAYMUG_CONFIG` / `<bin_dir>/config.yaml` resolution chain |
| New conversation says no provider is available | Add an account under "Admin settings → Providers & models", and bind it to the user on the user management page |
| Saving a Codex account reports config_dir required | Codex providers must set a credentials directory (`CODEX_HOME`) |
| Unreachable after an upgrade | The watchdog rolls back automatically; you can also run `daymug upgrade --rollback` manually |
| All admins were demoted by mistake | Add your username to `admin.bootstrap_usernames` and restart |
| Long-lived WebSocket connections drop | Check that the reverse proxy's `proxy_read_timeout` is ≥ 3600s |
| Service not running after a reboot (macOS) | Log in as the user who installed DayMug first, then check with `launchctl print "gui/$(id -u)/com.daymug.daymug"` |
| Viewing logs | Prefer `tail -f ~/.daymug/logs/daymug.log` (written by the service itself, independent of the system log). System side: Linux `journalctl --user -u daymug.service -f`; macOS `log stream --predicate 'process == "daymug"'` |
| `journalctl` shows no recent logs | journald may have stopped persisting (writing only to the volatile `/run/log/journal`, lost on reboot). Rely on `~/.daymug/logs/daymug.log`; fixing journald requires root |
