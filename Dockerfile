# syntax=docker/dockerfile:1
#
# DayMug in a container: the same embedded single binary the release builds,
# plus the agent runtimes it drives. All state lives under /var/lib/daymug
# (mount a volume there); see docs/guides/docker.md.

ARG NODE_IMAGE=node:24-bookworm-slim
ARG GO_IMAGE=golang:1.25-bookworm

# ---- frontend ---------------------------------------------------------------
FROM ${NODE_IMAGE} AS frontend
ARG VERSION=dev
WORKDIR /src/frontend
# corepack reads packageManager from package.json, so this is the exact pnpm
# the lockfile was written with.
RUN corepack enable
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/.npmrc ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN APP_VERSION="${VERSION}" pnpm build

# ---- backend ----------------------------------------------------------------
FROM ${GO_IMAGE} AS backend
ARG VERSION=dev
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend /src/frontend/dist ./cmd/server/dist
# Same as release.yml: JS/CSS embedded gzip-only (the static handler serves
# the .gz with Content-Encoding: gzip), pure-Go SQLite so CGO stays off. No
# DefaultManifestURL override — self-upgrade is disabled in the image anyway.
RUN find ./cmd/server/dist/assets -type f \( -name '*.js' -o -name '*.css' \) \
        -exec gzip -9 -n {} + \
    && CGO_ENABLED=0 go build -trimpath -tags embed_frontend \
        -ldflags "-s -w -X main.Version=${VERSION}" \
        -o /out/daymug ./cmd/server

# ---- runtime ----------------------------------------------------------------
FROM ${NODE_IMAGE}
# Codex is on by default; --build-arg INSTALL_CODEX=false drops it for a
# Claude-only image.
ARG INSTALL_CODEX=true

# git: agents commit and diff in their work dirs. tini: PID 1 that reaps the
# agent subprocesses' orphans and forwards SIGTERM. curl: the healthcheck.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git tini \
    && rm -rf /var/lib/apt/lists/*

# The Agent SDK is what the default `claude` transport runs on; the CLI backs
# claude-compatible providers and the CLI transport. Installed under the
# image's global prefix (/usr/local/lib/node_modules), which DayMug searches.
RUN npm install -g @anthropic-ai/claude-agent-sdk @anthropic-ai/claude-code \
    && if [ "${INSTALL_CODEX}" = "true" ]; then npm install -g @openai/codex; fi \
    && npm cache clean --force

# The DB, PID file and log are resolved next to the binary
# (<binary_dir>/data, <binary_dir>/logs) with no override, so the binary
# stays in the read-only image and those two directories are symlinks onto
# the state volume. Config, users' work dirs and $HOME (agent logins such as
# ~/.claude, ~/.codex) sit directly on the volume.
RUN groupadd --system --gid 10001 daymug \
    && useradd --system --uid 10001 --gid daymug --home-dir /var/lib/daymug/home \
        --no-create-home --shell /usr/sbin/nologin daymug \
    && mkdir -p /opt/daymug /var/lib/daymug/data /var/lib/daymug/logs /var/lib/daymug/home \
    && ln -s /var/lib/daymug/data /opt/daymug/data \
    && ln -s /var/lib/daymug/logs /opt/daymug/logs \
    && chown -R daymug:daymug /var/lib/daymug
COPY --from=backend /out/daymug /opt/daymug/daymug
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN ln -s /opt/daymug/daymug /usr/local/bin/daymug

# DAYMUG_ADDR overrides server.addr: init writes 127.0.0.1:8090, which is
# unreachable through a published port. DAYMUG_HONOR_SIGTERM: `docker stop`
# only sends SIGTERM, which serve otherwise ignores outside systemd.
# DAYMUG_DISABLE_SELF_UPGRADE: an image is upgraded by pulling a new one.
# GIN_MODE matches what the systemd/launchd units install.
ENV DAYMUG_CONFIG=/var/lib/daymug/config.yaml \
    DAYMUG_ADDR=0.0.0.0:8090 \
    DAYMUG_HONOR_SIGTERM=1 \
    DAYMUG_DISABLE_SELF_UPGRADE=1 \
    GIN_MODE=release \
    HOME=/var/lib/daymug/home

USER daymug
WORKDIR /var/lib/daymug
VOLUME ["/var/lib/daymug"]
EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8090/api/health >/dev/null || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/docker-entrypoint.sh"]
CMD ["serve"]
