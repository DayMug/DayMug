# Local development

For day-to-day backend work, run the server with hot reload via `air` from `backend/`. `air` builds `backend/bin/server` and starts it with `DAYMUG_CONFIG=../data/config.yaml` (i.e. `<repo>/data/config.yaml`; export `DAYMUG_CONFIG` to override). Create that config once by running `server init` from `<repo>/data/` — exact commands in [Installation §2.2](../guides/installation.md#22-local-development-hot-reload). The SQLite database follows the binary, so it lands in `backend/bin/data/database.db` and survives `air` restarts. Avoid `go run ./cmd/server serve`: the binary lives in a temp dir, so neither the next-to-binary config nor the database path is stable.

Frontend dev is `pnpm dev` from `frontend/` (port 5174), which proxies `/api` (including WebSockets) to `http://127.0.0.1:8090` — the `server.addr` that `init` writes, so a fresh dev config needs no edit. If your backend listens elsewhere, start Vite with `DAYMUG_DEV_BACKEND=http://127.0.0.1:<port> pnpm dev`.

The repo-root `Makefile` wraps these: `make dev-init` (one-time config), `make dev-backend` (air), `make dev-frontend` (Vite), `make test-fast`, and `make build` for an embedded release-style binary at `backend/bin/daymug`.

From the repository root, `./scripts/test.sh fast` runs the backend and the fast
frontend projects concurrently; `./scripts/test.sh` runs both complete suites
in parallel. For a frontend-only loop, use `pnpm test:fast`; it runs the Node
and UI projects while leaving the real XLSX round-trip tests to
`pnpm test:integration`. `pnpm test` remains the required complete frontend
suite. The UI harness serves deterministic in-memory responses for passive
model, server-info, and command lookups. Any other unmocked HTTP
request fails immediately instead of leaking into happy-dom teardown.

For deployment, the release artifact is a single static binary that embeds the frontend. Its runtime dependencies are the agent runtimes of the configured providers: Node.js 18+ with `@anthropic-ai/claude-agent-sdk` for `claude` on the default Agent SDK transport, the `claude` CLI for `claude-compatible` or the CLI transport, and a `codex` that supports `app-server` for the Codex family. `daymug bootstrap` lays out `~/.daymug/` and installs the binary as a user-level service: on Linux the systemd user unit is enabled and started (or restarted when already running, so a re-install serves the new binary) unless `--no-start` is passed, and on macOS the LaunchAgent starts after the installing user logs in. `--skip-service` skips service installation entirely. `scripts/install.sh` is the one-line installer that downloads the matching release binary, verifies the checksum, then runs `bootstrap`.
