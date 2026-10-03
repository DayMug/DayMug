# Running DayMug in Docker

The repo-root `Dockerfile` builds the same embedded single binary the release
workflow ships and bundles the agent runtimes it drives:

- `@anthropic-ai/claude-agent-sdk` — the default transport for `claude` providers
- `@anthropic-ai/claude-code` — the `claude` CLI (CLI transport, `claude-compatible`)
- `@openai/codex` — Codex / `openai-compatible`; on by default, drop it with
  `--build-arg INSTALL_CODEX=false`

## Quick start

```bash
docker compose up -d --build
```

Open <http://localhost:8090>, create the first admin on the setup page (or
`docker compose exec daymug daymug user add admin --email admin@example.com --admin`), then add
providers under **Settings → Administration → Providers & models**.

The port is published on `127.0.0.1` only: on a fresh database whoever opens
the page first becomes admin. Expose it through a reverse proxy, or widen the
mapping to `8090:8090` once the admin account exists.

Plain `docker`:

```bash
docker build -t daymug:local --build-arg VERSION="$(git describe --tags --always)" .
docker run -d --name daymug -p 127.0.0.1:8090:8090 -v daymug-state:/var/lib/daymug \
  --stop-timeout 90 daymug:local
```

## Layout: everything stateful is on one volume

The server resolves its database, PID file and log next to its own binary
(`<binary_dir>/data`, `<binary_dir>/logs`) with no override, and its config
through `--config` → `DAYMUG_CONFIG` → `<binary_dir>/config.yaml`. The image
therefore keeps the binary read-only at `/opt/daymug/daymug` and points
everything else at `/var/lib/daymug`:

| Path in the volume | What | How it gets there |
|--------------------|------|-------------------|
| `config.yaml` | server config | `DAYMUG_CONFIG=/var/lib/daymug/config.yaml` |
| `data/` | `database.db`, PID file | `/opt/daymug/data` is a symlink here |
| `logs/` | `daymug.log` (also on stdout/stderr → `docker logs`) | `/opt/daymug/logs` is a symlink here |
| `users/` | users' work directories | `users.default_home_root` written by `init` |
| `home/` | `$HOME` of the `daymug` user — `~/.claude`, `~/.codex` logins | `HOME=/var/lib/daymug/home` |

On first start the entrypoint (`scripts/docker-entrypoint.sh`) finds no
config and runs `daymug init` in `/var/lib/daymug`. Later starts leave the
existing config alone, so edit `config.yaml` on the volume and restart the
container to change it (`docker compose exec daymug vi` isn't available —
use `docker compose cp` or a throwaway container mounting the volume).

The container runs as the unprivileged `daymug` user (uid/gid 10001). A
named volume inherits that ownership from the image; if you bind-mount a host
directory instead, `chown -R 10001:10001` it first.

## Environment the image sets

| Variable | Value | Why |
|----------|-------|-----|
| `DAYMUG_ADDR` | `0.0.0.0:8090` | `init` writes `127.0.0.1:8090`, unreachable through a published port. Changing it means changing the published port and the healthcheck too. |
| `DAYMUG_HONOR_SIGTERM` | `1` | `serve` ignores a SIGTERM it can't attribute to systemd (it guards against a stray `kill` from an agent's shell). `docker stop` only sends SIGTERM. |
| `DAYMUG_DISABLE_SELF_UPGRADE` | `1` | See below. |
| `DAYMUG_CONFIG`, `HOME` | under `/var/lib/daymug` | See the layout table. |

`tini` is PID 1, so orphaned agent subprocesses are reaped and signals reach
`serve`. A graceful stop drains HTTP and in-flight agent jobs for up to ~75 s;
the compose file sets `stop_grace_period: 90s` (use `--stop-timeout 90` with
plain `docker run`) so Docker doesn't SIGKILL mid-drain.

The image's `HEALTHCHECK` polls `/api/health` on port 8090, which answers 503
if the database is unreadable.

## Upgrading: pull or rebuild, don't self-upgrade

The in-app self-upgrade replaces the binary in place and has a watchdog
restart a systemd/launchd service — neither fits a container: the binary is
in a read-only image layer that the next recreate would bring back anyway,
and there is no service manager to restart. With
`DAYMUG_DISABLE_SELF_UPGRADE=1`, **Check for updates**, apply, rollback and
`daymug upgrade` all refuse with a message saying to
upgrade by pulling a new image.

To upgrade, rebuild (`git pull && docker compose up -d --build`) or pull a
newer image and recreate the container. Schema migrations run on start, as
they do for a binary install. The watchdog's automatic pre-upgrade snapshot
doesn't happen here, so back the volume up first, e.g.:

```bash
docker compose stop daymug
docker run --rm -v daymug_daymug-state:/state -v "$PWD":/backup debian:bookworm-slim \
  tar czf /backup/daymug-state.tgz -C /state .
docker compose start daymug
```

(Compose prefixes the volume with the project name — `docker volume ls`
shows the exact one.)

## Sandbox (`sandbox.type: bwrap`)

`init` writes `sandbox.type: noop`, which works everywhere: agents run as the
container's `daymug` user with the container as the isolation boundary.

`bwrap` is **not** installed in the image, and it would not work under
Docker's defaults anyway. Measured on Docker 29 with a non-root user:

| Docker options | `bwrap --unshare-all --proc /proc …` |
|----------------|---------------------------------------|
| defaults | fails: *No permissions to create new namespace* (default seccomp/AppArmor block user namespaces) |
| `--security-opt seccomp=unconfined --security-opt apparmor=unconfined` | fails: *Can't mount proc on /newroot/proc* (Docker masks `/proc`) |
| the above + `--security-opt systempaths=unconfined` | works |

So per-agent `bwrap` confinement inside a container needs a derived image
with the `bubblewrap` package plus all three relaxations — which loosen the
container's own isolation. For most deployments the container boundary with
`noop` is the better trade.

## Agent logins

Accounts that log in interactively (`claude /login`, `codex login`) store
their credentials under `$HOME` or the account's configured `config_dir`;
both should live on the volume so they survive a recreate. The admin
terminal (**Settings → Administration**) runs inside the container, so a
login performed there lands in the right place.
