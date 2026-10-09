# DayMug documentation

DayMug is a **multi-user web workspace** for code-CLI agents: human users log in and manage one or more Agent personas, and each Agent runs code-CLI child processes through a pluggable provider account (`claude` / `codex` / `claude-compatible` / `openai-compatible`). The Go backend (Gin + SQLite, single static binary) handles auth, conversations, the account pool, and child-process supervision; the Vue 3 SPA drives conversations in real time over REST + WebSocket.

```
docs/
├── guides/         install, configure, and use DayMug
├── architecture/   how it is built
└── development/    contributing, releasing, known issues
```

## Guides

| I want to… | Read |
|-------|--------|
| Install and run it on Linux / macOS | [guides/installation.md](guides/installation.md) |
| Run it with Docker / docker compose | [guides/docker.md](guides/docker.md) |
| Understand every field in `config.yaml` | [guides/configuration.md](guides/configuration.md) |
| Start from a fully commented config template | [guides/config.sample.yaml](guides/config.sample.yaml) |
| Connect Slack / Feishu (Lark) / Telegram bots | [guides/im-bots.md](guides/im-bots.md) |
| Connect a WeChat iLink bot | [guides/wechat.md](guides/wechat.md) |
| How slash-prefixed text and `/compact` behave | [guides/slash-commands.md](guides/slash-commands.md) |

## Architecture

| Topic | Doc |
|------|------|
| Global architecture / request lifecycle / directory layout | [architecture/overview.md](architecture/overview.md) |
| Go backend layers (handler / service / store / agent) | [architecture/backend.md](architecture/backend.md) |
| Vue 3 frontend structure (pages / components / composables) | [architecture/frontend.md](architecture/frontend.md) |
| Agent backend abstraction (claude / codex / the two compatible-endpoint adapters) | [architecture/agent-backends.md](architecture/agent-backends.md) |
| Multi-user model (human users / Agent personas / provider bindings / sandbox) | [architecture/multi-user.md](architecture/multi-user.md) |
| Working directory / cwd and session resume | [architecture/working-directory.md](architecture/working-directory.md) |
| Data model (SQLite schema and migrations) | [architecture/data-model.md](architecture/data-model.md) |
| HTTP / WebSocket API reference | [architecture/api-reference.md](architecture/api-reference.md) |

## Development

| Topic | Doc |
|------|------|
| Local development, hot reload, test scripts | [development/local-development.md](development/local-development.md) |
| Internationalization (i18n) | [development/i18n.md](development/i18n.md) |
| Releases and self-upgrade (mechanism; tagging steps in [RELEASING.md](../RELEASING.md)) | [development/releases.md](development/releases.md) |
| Known issues | [development/known-issues.md](development/known-issues.md) |

## Tech stack at a glance

- **Backend**: Go ≥ 1.25, Gin, `modernc/sqlite` (pure Go, no CGO), single static binary, optionally embedding the frontend build (build tag `embed_frontend`).
- **Frontend**: Vue 3 + TypeScript + Vite + Tailwind + shadcn-vue, managed with pnpm.
- **Transport**: REST (CRUD) + WebSocket (real-time streaming conversations).
- **Agent backends**: pluggable `agent.Backend` implementations, currently four provider types — two first-party logins (`claude` / `codex`) and two that reuse them against third-party compatible endpoints (`claude-compatible` / `openai-compatible`).
