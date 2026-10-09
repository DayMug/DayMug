<p align="center">
  <img src="frontend/public/favicon.svg" alt="DayMug" width="72" />
</p>

<h1 align="center">DayMug</h1>

<p align="center">
  An open-source, self-hosted web workspace for <b>Claude</b> and <b>Codex</b> agents that your whole team shares —<br/>
  from a laptop, a phone, or straight inside Slack, Feishu (Lark), Telegram and WeChat.
</p>

<p align="center">
  <a href="https://daymug.com">Website</a> ·
  <a href="docs/guides/installation.md">Install guide</a> ·
  <a href="FEATURES.md">Features</a> ·
  <a href="docs/README.md">Docs</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

---

DayMug runs the official agent runtimes (Claude Agent SDK / Claude Code CLI, Codex app-server / CLI) on **your** machine and puts a multi-user web UI in front of them. It ships as a single static binary with the frontend embedded; state lives in one SQLite file.

- **Shared workspace** — many people, many agent personas, each with its own persona, MCP config and working directory. Conversations persist and resume across devices.
- **Watch, steer, hand off** — live streaming of tool calls and thinking, mid-turn messages, stop/resume, and a built-in file browser for the agent's workspace.
- **Your accounts, your rules** — bring Claude / Codex subscriptions or any Anthropic- or OpenAI-compatible endpoint; pool several accounts, bind them per user, cap concurrency, and sandbox agents per user (bwrap on Linux).
- **Chat apps** — the same agents answer in Slack, Feishu (Lark), Telegram and WeChat threads.
- **Low ops** — one binary, a user-level service, in-app self-upgrade with automatic rollback.

## Quick start

On **Linux (x86_64 / arm64)** or **macOS (Apple Silicon)**, as a regular (non-root) user:

```bash
curl -fsSL https://github.com/DayMug/DayMug/releases/latest/download/install.sh | bash
```

The installer downloads the release binary, verifies its SHA-256, offers to install the Claude Agent SDK, sets up `~/.daymug/`, and starts a user-level service (systemd `--user` on Linux, a LaunchAgent on macOS). Then:

1. Open **http://127.0.0.1:8090** and create the first admin account.
2. Add an AI account under account menu → **Admin settings → Providers & models**.
3. Start chatting.

Something not working? Run `~/.daymug/daymug doctor` — it checks runtimes, config, the service and the listen port, and prints a fix for each failure.

Useful installer options: `bash install.sh v1.5.106` (pin a version), `--no-start`, `--skip-service`, `--yes` / `--no-deps` (dependency prompt), `DAYMUG_RELEASES_URL=…` (install from a fork). See [docs/guides/installation.md](docs/guides/installation.md) for service management, uninstalling, upgrades, and putting DayMug behind a reverse proxy.

### Requirements

- **Node.js ≥ 18** with `@anthropic-ai/claude-agent-sdk` installed globally (the default Claude transport; the installer offers to install it).
- At least one agent CLI, logged in: [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`) and/or [Codex](https://github.com/openai/codex) (`codex`, a version with `app-server`).
- `curl` and `sha256sum` / `shasum`.

DayMug listens on `127.0.0.1` over plain HTTP. To reach it from other machines, put a reverse proxy or tunnel in front — see [Public access](docs/guides/installation.md#4-public-access).

## Docker

```bash
docker compose up -d
```

Builds the image from this repository and serves DayMug on http://127.0.0.1:8090 (published on loopback only) with all state on a named volume. Details, limits (sandboxing, self-upgrade) and configuration: [docs/guides/docker.md](docs/guides/docker.md).

## Build from source

Requires Go ≥ 1.25, Node.js ≥ 20 and pnpm ≥ 10.

```bash
git clone https://github.com/DayMug/DayMug.git
cd DayMug
make build                       # frontend + embedded single binary -> backend/bin/daymug
./backend/bin/daymug bootstrap   # lay out ~/.daymug/ and start the user service
```

For hot-reload development (`make dev-init`, `make dev-backend`, `make dev-frontend`), debugging, tests and the verification pipeline, see [CONTRIBUTING.md](CONTRIBUTING.md).

## Documentation

| | |
|---|---|
| Install, service management, upgrades, reverse proxy | [docs/guides/installation.md](docs/guides/installation.md) |
| Every `config.yaml` field | [docs/guides/configuration.md](docs/guides/configuration.md) |
| Full feature list | [FEATURES.md](FEATURES.md) |
| Multi-user model, account pool, sandbox | [docs/architecture/multi-user.md](docs/architecture/multi-user.md) |
| IM bots (Slack / Feishu / Telegram) and WeChat | [docs/guides/im-bots.md](docs/guides/im-bots.md), [docs/guides/wechat.md](docs/guides/wechat.md) |
| Architecture and API reference | [docs/README.md](docs/README.md) |

## Contributing

Issues and pull requests are welcome at [github.com/DayMug/DayMug](https://github.com/DayMug/DayMug/issues). Start with [CONTRIBUTING.md](CONTRIBUTING.md) for the dev setup and the checks a change must pass.

## License

DayMug is released under a modified Apache License 2.0 — see [LICENSE](LICENSE). Commercial use is permitted. The one additional condition: deployments must keep the "Powered by DayMug" attribution and project link visible in the web interface (login page and Settings → About). Bundled third-party components are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
