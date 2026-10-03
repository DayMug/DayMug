#!/usr/bin/env bash
# DayMug installer.
#
# Detects the host OS/arch, downloads the matching release binary from the
# release manifest published with every GitHub Release of DayMug/DayMug,
# verifies its SHA-256, offers to install the Claude Agent SDK when it is
# missing, then runs `daymug bootstrap` — which prints a prerequisite
# report, lays out ~/.daymug/, and installs and starts a user service.
#
# Usage:
#   curl -fsSL https://github.com/DayMug/DayMug/releases/latest/download/install.sh | bash
#   curl -fsSL https://github.com/DayMug/DayMug/releases/latest/download/install.sh | bash -s -- --yes
#   bash install.sh [vX.Y.Z] [--skip-service] [--no-start] [--yes] [--no-deps]
#
# Environment:
#   DAYMUG_RELEASES_URL  GitHub releases page of the repo to install from
#                        (default https://github.com/DayMug/DayMug/releases),
#                        e.g. a fork's.
#
# Re-running is safe: the binary is overwritten, an existing config.yaml is
# preserved.

set -euo pipefail

DEFAULT_RELEASES_URL="https://github.com/DayMug/DayMug/releases"
INSTALL_URL="${DEFAULT_RELEASES_URL}/latest/download/install.sh"
BUILD_FROM_SOURCE_URL="https://github.com/DayMug/DayMug#build-from-source"
INSTALL_DIR="${HOME}/.daymug"
TARGET_BINARY="${INSTALL_DIR}/daymug"
CONFIG_PATH="${INSTALL_DIR}/config.yaml"
AGENT_SDK_PKG="@anthropic-ai/claude-agent-sdk"
CODEX_PKG="@openai/codex"
MIN_NODE_MAJOR=18

# Everything above the DAYMUG_INSTALL_LIBRARY_ONLY guard below is free of
# side effects, so install_test.sh can source it and drive each decision
# with stubbed commands.

extract_manifest_field() {
    # $1 = platform key (linux/amd64), $2 = field name (url|sha256).
    # The release workflow formats JSON across multiple lines, while older
    # manifests may be compact. Start scanning at the requested platform key
    # and return the first matching field in its object.
    awk -v plat="$1" -v fld="$2" '
        !in_platform {
            marker = "\"" plat "\""
            start = index($0, marker)
            if (start == 0) next
            in_platform = 1
            line = substr($0, start)
        }
        in_platform {
            if (line == "") line = $0
            pat = "\"" fld "\"[[:space:]]*:[[:space:]]*\""
            i = match(line, pat)
            if (i != 0) {
                rest = substr(line, i + RLENGTH)
                j = index(rest, "\"")
                if (j != 0) {
                    print substr(rest, 1, j - 1)
                    exit
                }
            }
            line = ""
        }
    '
}

releases_base_url() {
    # A trailing slash on the override would produce "releases//latest/…",
    # which GitHub answers with a 404.
    local base="${DAYMUG_RELEASES_URL:-$DEFAULT_RELEASES_URL}"
    while [[ "$base" == */ ]]; do base="${base%/}"; done
    printf '%s\n' "$base"
}

manifest_url_for() {
    # $1 = optional pinned tag. Every release carries its own manifest.json
    # asset, mirroring service.VersionedManifestURL().
    local base
    base="$(releases_base_url)"
    if [[ -n "${1:-}" ]]; then
        printf '%s/download/%s/manifest.json\n' "$base" "$1"
    else
        printf '%s/latest/download/manifest.json\n' "$base"
    fi
}

platform_for() {
    # $1 = uname -s, $2 = uname -m. Prints "<manifest key> <asset name>";
    # fails for hosts the release pipeline does not target.
    case "$1-$2" in
        Linux-x86_64)              echo "linux/amd64 daymug-linux-amd64" ;;
        Linux-aarch64|Linux-arm64) echo "linux/arm64 daymug-linux-arm64" ;;
        Darwin-arm64)              echo "darwin/arm64 daymug-darwin-arm64" ;;
        *) return 1 ;;
    esac
}

print_help() {
    cat <<HELP
DayMug installer.

Usage:
  curl -fsSL ${INSTALL_URL} | bash
  curl -fsSL ${INSTALL_URL} | bash -s -- [options]
  bash install.sh [vX.Y.Z] [options]

Options:
  vX.Y.Z          install this tag instead of the latest release
  --skip-service  install files only, no service (upgrades cannot auto-restart)
  --no-start      register the service but do not start it yet
  -y, --yes       non-interactive: accept dependency installs
                  (npm install -g ${AGENT_SDK_PKG})
  --no-deps       never install dependencies, only report them
  -h, --help      show this help

Environment:
  DAYMUG_RELEASES_URL  GitHub releases page to install from
                       (default ${DEFAULT_RELEASES_URL})

Re-running is safe: the binary is overwritten, an existing config.yaml is
preserved.
HELP
}

parse_args() {
    # Sets VERSION_OVERRIDE SKIP_SERVICE NO_START ASSUME_YES NO_DEPS SHOW_HELP.
    # Returns 2 on an unknown argument and leaves exiting to the caller.
    VERSION_OVERRIDE=""
    SKIP_SERVICE=0
    NO_START=0
    ASSUME_YES=0
    NO_DEPS=0
    SHOW_HELP=0
    local arg
    for arg in "$@"; do
        case "$arg" in
            --skip-service) SKIP_SERVICE=1 ;;
            --no-start)     NO_START=1 ;;
            --yes|-y)       ASSUME_YES=1 ;;
            --no-deps)      NO_DEPS=1 ;;
            --help|-h)      SHOW_HELP=1 ;;
            v[0-9]*)        VERSION_OVERRIDE="$arg" ;;
            *)
                echo "unknown argument: $arg (see --help)" >&2
                return 2
                ;;
        esac
    done
}

bootstrap_args() {
    # Only forward flags the caller asked for: a pinned older binary may
    # not know --no-start, and Go's flag parser exits on unknown flags.
    BOOTSTRAP_ARGS=(bootstrap)
    if [[ "$SKIP_SERVICE" -eq 1 ]]; then BOOTSTRAP_ARGS+=(--skip-service); fi
    if [[ "$NO_START" -eq 1 ]]; then BOOTSTRAP_ARGS+=(--no-start); fi
}

node_major() {
    # Prints the major version of `node` on PATH; nothing if it is missing
    # or prints something unexpected.
    local v
    v="$(node --version 2>/dev/null)" || return 0
    v="${v#v}"
    v="${v%%.*}"
    if [[ "$v" =~ ^[0-9]+$ ]]; then printf '%s\n' "$v"; fi
}

agent_sdk_installed() {
    # The directory check is the reliable signal. `npm ls -g` alone is not:
    # it exits non-zero when *any* global package is broken, which would
    # re-offer an install that already exists. It stays as the fallback for
    # prefixes where `npm root -g` resolves somewhere unexpected.
    local root
    root="$(npm root -g 2>/dev/null)" || root=""
    if [[ -n "$root" && -d "${root}/${AGENT_SDK_PKG}" ]]; then
        return 0
    fi
    npm ls -g --depth=0 "$AGENT_SDK_PKG" >/dev/null 2>&1
}

have_tty() {
    # Under `curl | bash` stdin is the script itself, so answers must come
    # from the controlling terminal. `-r /dev/tty` is true even without one
    # (cron, CI, ssh -T); only actually opening it tells.
    (exec </dev/tty) 2>/dev/null
}

answer_is_yes() {
    # The prompt is [Y/n]: an empty answer means yes.
    case "${1:-}" in
        ''|y|Y|yes|Yes|YES) return 0 ;;
        *) return 1 ;;
    esac
}

sdk_install_decision() {
    # Prints what to do about the Claude Agent SDK:
    #   no-node  node not on PATH          node-old  node < MIN_NODE_MAJOR
    #   no-npm   npm not on PATH           present   already installed
    #   skip     missing, --no-deps        install   missing, --yes given
    #   ask      missing, prompt /dev/tty  no-tty    missing, nobody to ask
    # Node itself is never installed — too many competing ways (distro,
    # nvm, brew) to pick one for the user.
    if ! command -v node >/dev/null 2>&1; then echo no-node; return; fi
    local major
    major="$(node_major)"
    if [[ -z "$major" || "$major" -lt "$MIN_NODE_MAJOR" ]]; then echo node-old; return; fi
    if ! command -v npm >/dev/null 2>&1; then echo no-npm; return; fi
    if agent_sdk_installed; then echo present; return; fi
    if [[ "$NO_DEPS" -eq 1 ]]; then echo skip; return; fi
    if [[ "$ASSUME_YES" -eq 1 ]]; then echo install; return; fi
    if have_tty; then echo ask; return; fi
    echo no-tty
}

npm_install_global() {
    # $1 = package. Never escalates with sudo: the installer is deliberately
    # unprivileged, and a root-owned global package is one the user could
    # later only upgrade or remove with sudo again.
    local pkg="$1" out status=0
    out="$(mktemp -t daymug-npm.XXXXXX)"
    npm install -g "$pkg" >"$out" 2>&1 || status=$?
    cat "$out"
    if [[ "$status" -ne 0 ]]; then
        if command grep -q 'EACCES' "$out"; then
            warn "npm cannot write its global prefix ($(npm config get prefix 2>/dev/null || echo unknown))."
            warn "Switch to a user-level prefix instead of using sudo, then retry:"
            warn "  npm config set prefix ~/.npm-global && export PATH=\"\$HOME/.npm-global/bin:\$PATH\""
            warn "  npm install -g ${pkg}"
        else
            warn "npm install -g ${pkg} failed (exit ${status})."
        fi
    fi
    rm -f "$out"
    return "$status"
}

service_state() {
    # $1 = uname -s. Prints started | not-started | skipped | unmanaged.
    # Asks the service manager instead of trusting flags: bootstrap skips
    # registration on its own when no user service manager is reachable
    # (containers, WSL without systemd), and a start can fail.
    if [[ "$SKIP_SERVICE" -eq 1 ]]; then echo skipped; return; fi
    case "$1" in
        Linux)
            if ! command -v systemctl >/dev/null 2>&1 \
                || ! systemctl --user cat daymug.service >/dev/null 2>&1; then
                echo unmanaged
            elif systemctl --user is-active --quiet daymug.service; then
                echo started
            else
                echo not-started
            fi
            ;;
        Darwin)
            local info
            if ! info="$(launchctl print "gui/$(id -u)/com.daymug.daymug" 2>/dev/null)"; then
                echo unmanaged
            elif [[ "$info" == *"state = running"* ]]; then
                echo started
            else
                echo not-started
            fi
            ;;
        *) echo unmanaged ;;
    esac
}

config_port() {
    # $1 = config path. Prints the port of server.addr (":8090",
    # "127.0.0.1:8090", quoted or bare); 8080 — the binary's own default —
    # when the file or the field is absent, since config is optional.
    local port
    port="$(awk '
        /^[[:space:]]*addr:/ {
            v = $0
            sub(/^[[:space:]]*addr:[[:space:]]*/, "", v)
            sub(/[[:space:]]+#.*$/, "", v)
            gsub(/["\047[:space:]]/, "", v)
            n = split(v, a, ":")
            print (n >= 2 ? a[n] : v)
            exit
        }
    ' "$1" 2>/dev/null)" || port=""
    if [[ "$port" =~ ^[0-9]+$ ]]; then
        printf '%s\n' "$port"
    else
        printf '8080\n'
    fi
}

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m  %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m  %s\n' "$*" >&2; exit 1; }

# Tests source this file to exercise the exact helpers without running the
# installer. Normal executions, including `curl ... | bash`, never set it.
if [[ "${DAYMUG_INSTALL_LIBRARY_ONLY:-0}" == "1" ]]; then
    return 0 2>/dev/null || exit 0
fi

parse_args "$@" || exit 2
if [[ "$SHOW_HELP" -eq 1 ]]; then
    print_help
    exit 0
fi

# --- 0. Refuse to install as root -------------------------------------------
# DayMug installs into $HOME and configures a *user-level* service
# (systemd --user / launchd LaunchAgent). Running as root puts the binary in
# /root/.daymug, registers a unit only root can start, and means every agent
# child process runs as root too. Bail out with guidance instead of producing
# a half-working layout that the user then has to clean up by hand.
if [[ "$(id -u)" -eq 0 ]]; then
    cat >&2 <<EOF
xx  Refusing to install DayMug as root.

DayMug is designed to run as a regular user — it installs into \$HOME/.daymug
and configures a user-level service (systemd --user on Linux, launchd on
macOS). Installing as root would:

  - place the binary and config under /root/.daymug
  - register a service only root can manage
  - run every spawned agent process with root privileges

Re-run this installer as the user that should own the DayMug service, e.g.:

  su - <youruser>
  curl -fsSL ${INSTALL_URL} | bash

EOF
    exit 1
fi

# --- 1. Detect OS / arch ----------------------------------------------------
uname_s="$(uname -s)"
uname_m="$(uname -m)"
platform_line="$(platform_for "$uname_s" "$uname_m")" \
    || die "no prebuilt DayMug binary for ${uname_s} ${uname_m}. Build from source: ${BUILD_FROM_SOURCE_URL}"
PLATFORM="${platform_line%% *}"
ASSET="${platform_line#* }"
log "Detected platform: ${PLATFORM}"

# --- 2. Sanity-check tooling ------------------------------------------------
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }
need curl

# Pick a sha256 verifier. macOS ships shasum; Linux usually has sha256sum.
if command -v sha256sum >/dev/null 2>&1; then
    SHA_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
    SHA_CMD="shasum -a 256"
else
    die "neither sha256sum nor shasum is available"
fi

# --- 3. Resolve manifest ----------------------------------------------------
manifest_url="$(manifest_url_for "$VERSION_OVERRIDE")"
log "Fetching manifest: ${manifest_url}"
manifest_json="$(curl -fsSL "$manifest_url")" || die "failed to fetch ${manifest_url}"

# Tiny JSON extractor avoids requiring jq, which macOS does not ship.
binary_url="$(printf '%s' "$manifest_json" | extract_manifest_field "$PLATFORM" url)"
binary_sha="$(printf '%s' "$manifest_json" | extract_manifest_field "$PLATFORM" sha256)"
version="$(printf '%s' "$manifest_json" | awk -F'"' '/"version"/ {print $4; exit}')"

if [[ -z "$binary_url" || -z "$binary_sha" ]]; then
    if [[ -n "$VERSION_OVERRIDE" ]]; then
        die "release ${VERSION_OVERRIDE} has no ${PLATFORM} build. Install the latest release, or build from source: ${BUILD_FROM_SOURCE_URL}"
    fi
    die "no published ${PLATFORM} build in ${manifest_url}. Build from source: ${BUILD_FROM_SOURCE_URL}"
fi
log "Release: ${version:-unknown}"
log "Binary:  ${binary_url}"

# --- 4. Download + verify ---------------------------------------------------
tmpdir="$(mktemp -d -t daymug-install.XXXXXX)"
trap 'rm -rf "$tmpdir"' EXIT
tmp_bin="${tmpdir}/${ASSET}"

log "Downloading…"
curl -fL --progress-bar -o "$tmp_bin" "$binary_url" || die "download failed"

actual_sha="$($SHA_CMD "$tmp_bin" | awk '{print $1}')"
if [[ "$actual_sha" != "$binary_sha" ]]; then
    die "checksum mismatch: expected ${binary_sha}, got ${actual_sha}"
fi
log "Checksum OK"
chmod +x "$tmp_bin"

# --- 5. Dependency assist ---------------------------------------------------
# Runs before bootstrap so the service it starts already finds the SDK and
# its prerequisite report reflects what was just installed. DEP_NOTES is
# repeated in the summary, where it isn't buried under npm/bootstrap output.
DEP_NOTES=()
sdk_decision="$(sdk_install_decision)"
sdk_install_now=0
case "$sdk_decision" in
    install) sdk_install_now=1 ;;
    ask)
        printf 'Install %s globally with npm? [Y/n] ' "$AGENT_SDK_PKG" >/dev/tty
        sdk_answer=""
        IFS= read -r sdk_answer </dev/tty || sdk_answer="n"
        if answer_is_yes "$sdk_answer"; then sdk_install_now=1; fi
        ;;
    no-node)
        warn "Node.js ${MIN_NODE_MAJOR}+ not found — Claude needs it: https://nodejs.org"
        DEP_NOTES+=("Node.js ${MIN_NODE_MAJOR}+ not found (Claude needs it): https://nodejs.org")
        ;;
    node-old)
        DEP_NOTES+=("Node.js $(node --version 2>/dev/null) is too old; Claude needs ${MIN_NODE_MAJOR}+: https://nodejs.org") ;;
    no-npm)
        DEP_NOTES+=("npm not found; Claude needs: npm install -g ${AGENT_SDK_PKG}") ;;
esac
if [[ "$sdk_install_now" -eq 1 ]]; then
    log "Installing ${AGENT_SDK_PKG}…"
    if npm_install_global "$AGENT_SDK_PKG"; then
        DEP_NOTES+=("installed ${AGENT_SDK_PKG}")
    else
        DEP_NOTES+=("${AGENT_SDK_PKG} install failed (see npm output above)")
    fi
elif [[ "$sdk_decision" == ask || "$sdk_decision" == no-tty || "$sdk_decision" == skip ]]; then
    DEP_NOTES+=("Claude needs: npm install -g ${AGENT_SDK_PKG}")
fi
# Codex is per-provider and opt-in, so it is only mentioned, never installed.
if ! command -v codex >/dev/null 2>&1; then
    DEP_NOTES+=("Codex (optional): npm install -g ${CODEX_PKG}")
fi

# --- 6. Run `daymug bootstrap` ----------------------------------------------
# The binary's bootstrap subcommand handles the pre-flight report,
# filesystem layout, and service registration + start in one go.
bootstrap_args
log "Running '${ASSET} ${BOOTSTRAP_ARGS[*]}'…"
"$tmp_bin" "${BOOTSTRAP_ARGS[@]}"

# --- 7. Summary -------------------------------------------------------------
# Deliberately short so it is still on screen after bootstrap's report.
# Deployment recipes (reverse proxy, TLS, LAN access) live in the docs.
LOCAL_PORT="$(config_port "$CONFIG_PATH")"
case "$(service_state "$uname_s")" in
    started) service_line="started" ;;
    not-started)
        if [[ "$uname_s" == "Linux" ]]; then
            service_line="installed, not started — systemctl --user start daymug.service"
        else
            service_line="installed, not started — launchctl kickstart gui/$(id -u)/com.daymug.daymug"
        fi
        ;;
    skipped) service_line="skipped (--skip-service) — run: ${TARGET_BINARY} serve" ;;
    *)       service_line="not registered (no user service manager) — run: ${TARGET_BINARY} serve" ;;
esac

cat <<EOF

==> DayMug ${version:-} installed: ${TARGET_BINARY}
    Service:  ${service_line}
    Open:     http://127.0.0.1:${LOCAL_PORT} and create the first admin account
    Next:     add an AI provider under Settings → Administration → Providers & models
EOF
for note in "${DEP_NOTES[@]+"${DEP_NOTES[@]}"}"; do
    printf '    Deps:     %s\n' "$note"
done
cat <<EOF
    Trouble:  ${TARGET_BINARY} doctor
    Log:      ${INSTALL_DIR}/logs/daymug.log
    Public access (reverse proxy / LAN): https://github.com/DayMug/DayMug/blob/main/docs/guides/installation.md#4-public-access
EOF
