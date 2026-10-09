#!/usr/bin/env bash

# Stubs (node, npm, have_tty) are invoked indirectly by the sourced helpers.
# shellcheck disable=SC2329
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export DAYMUG_INSTALL_LIBRARY_ONLY=1
# shellcheck source=install.sh
source "${SCRIPT_DIR}/install.sh"
unset DAYMUG_INSTALL_LIBRARY_ONLY

assert_field() {
    local manifest="$1" platform="$2" field="$3" want="$4" got
    got="$(printf '%s' "$manifest" | extract_manifest_field "$platform" "$field")"
    if [[ "$got" != "$want" ]]; then
        printf 'extract_manifest_field %s.%s: got %q, want %q\n' \
            "$platform" "$field" "$got" "$want" >&2
        return 1
    fi
}

assert_equal() {
    local what="$1" got="$2" want="$3"
    if [[ "$got" != "$want" ]]; then
        printf '%s: got %q, want %q\n' "$what" "$got" "$want" >&2
        return 1
    fi
}

pretty_manifest='{
  "version": "v1.0.220",
  "binaries": {
    "linux/amd64": {
      "url": "https://cdn.example/daymug-linux-amd64",
      "sha256": "linux-sha"
    },
    "darwin/arm64": {
      "url": "https://cdn.example/daymug-darwin-arm64",
      "sha256": "darwin-sha"
    }
  }
}'

compact_manifest='{"version":"v1.0.220","binaries":{"linux/amd64":{"url":"https://cdn.example/daymug-linux-amd64","sha256":"linux-sha"},"darwin/arm64":{"url":"https://cdn.example/daymug-darwin-arm64","sha256":"darwin-sha"}}}'

for manifest in "$pretty_manifest" "$compact_manifest"; do
    assert_field "$manifest" "linux/amd64" url "https://cdn.example/daymug-linux-amd64"
    assert_field "$manifest" "linux/amd64" sha256 "linux-sha"
    assert_field "$manifest" "darwin/arm64" url "https://cdn.example/daymug-darwin-arm64"
    assert_field "$manifest" "darwin/arm64" sha256 "darwin-sha"
done

echo "install manifest parser tests passed"

# --- Releases URL override --------------------------------------------------
(
    unset DAYMUG_RELEASES_URL
    assert_equal "default latest" "$(manifest_url_for "")" \
        "https://github.com/DayMug/DayMug/releases/latest/download/manifest.json"
    assert_equal "default pinned" "$(manifest_url_for v1.2.3)" \
        "https://github.com/DayMug/DayMug/releases/download/v1.2.3/manifest.json"
    export DAYMUG_RELEASES_URL="https://github.com/someone/fork/releases/"
    assert_equal "fork latest (trailing slash)" "$(manifest_url_for "")" \
        "https://github.com/someone/fork/releases/latest/download/manifest.json"
    assert_equal "fork pinned" "$(manifest_url_for v1.2.3)" \
        "https://github.com/someone/fork/releases/download/v1.2.3/manifest.json"
)

# --- Platform mapping --------------------------------------------------------
assert_equal "linux amd64" "$(platform_for Linux x86_64)" "linux/amd64 daymug-linux-amd64"
assert_equal "linux aarch64" "$(platform_for Linux aarch64)" "linux/arm64 daymug-linux-arm64"
assert_equal "linux arm64" "$(platform_for Linux arm64)" "linux/arm64 daymug-linux-arm64"
assert_equal "darwin arm64" "$(platform_for Darwin arm64)" "darwin/arm64 daymug-darwin-arm64"
for unsupported in "Darwin x86_64" "FreeBSD amd64" "Linux armv7l"; do
    # shellcheck disable=SC2086 # split into uname -s / uname -m on purpose
    if platform_for $unsupported >/dev/null; then
        echo "platform_for $unsupported: want failure" >&2
        exit 1
    fi
done

# An arm64 manifest entry resolves like any other platform.
arm_manifest='{"version":"v1.4.0","binaries":{"linux/amd64":{"url":"https://cdn.example/a","sha256":"a-sha"},"linux/arm64":{"url":"https://cdn.example/arm","sha256":"arm-sha"}}}'
assert_field "$arm_manifest" "linux/arm64" url "https://cdn.example/arm"
assert_field "$arm_manifest" "linux/arm64" sha256 "arm-sha"

# --- Flag parsing ------------------------------------------------------------
parse_args
bootstrap_args
assert_equal "no flags -> bare bootstrap" "${BOOTSTRAP_ARGS[*]}" "bootstrap"
assert_equal "no flags -> no pin" "$VERSION_OVERRIDE" ""

parse_args v1.3.81 --no-start --yes --no-deps
bootstrap_args
assert_equal "pin" "$VERSION_OVERRIDE" "v1.3.81"
assert_equal "--no-start passes through" "${BOOTSTRAP_ARGS[*]}" "bootstrap --no-start"
assert_equal "--yes" "$ASSUME_YES" 1
assert_equal "--no-deps" "$NO_DEPS" 1

parse_args -y --skip-service
bootstrap_args
assert_equal "-y" "$ASSUME_YES" 1
assert_equal "--skip-service passes through" "${BOOTSTRAP_ARGS[*]}" "bootstrap --skip-service"

parse_args --help
assert_equal "--help" "$SHOW_HELP" 1

for bad in --bogus verbose -x; do
    if parse_args "$bad" 2>/dev/null; then
        echo "parse_args $bad: want failure" >&2
        exit 1
    fi
done

help_text="$(print_help)"
case "$help_text" in
    *"https://github.com/DayMug/DayMug/releases/latest/download/install.sh"*"--no-start"*"--yes"*"--no-deps"*"DAYMUG_RELEASES_URL"*) ;;
    *) echo "help text is missing a flag or the real install URL" >&2; exit 1 ;;
esac

# --- Answer parsing ----------------------------------------------------------
for yes in "" y Y yes YES; do
    answer_is_yes "$yes" || { echo "answer_is_yes '$yes': want yes" >&2; exit 1; }
done
for no in n N no nope; do
    if answer_is_yes "$no"; then echo "answer_is_yes '$no': want no" >&2; exit 1; fi
done

# --- Dependency decision -----------------------------------------------------
# node/npm are stubbed as shell functions; `command -v` sees them like real
# binaries. Each case runs in a subshell so stubs never leak.
stub_root="$(mktemp -d)"
trap 'rm -rf "$stub_root"' EXIT
mkdir -p "${stub_root}/global/@anthropic-ai/claude-agent-sdk" "${stub_root}/empty"

decide() {
    # $1 node version ("" = no node), $2 npm present (1/0), $3 SDK present
    # (1/0), $4 tty (1/0), $5 ASSUME_YES, $6 NO_DEPS.
    (
        # shellcheck disable=SC2123 # emptying PATH is the point: hide real node/npm
        PATH="${stub_root}/empty"
        if [[ -n "$1" ]]; then
            eval "node() { echo '$1'; }"
        fi
        if [[ "$2" == 1 ]]; then
            if [[ "$3" == 1 ]]; then
                eval "npm() { [[ \"\$1 \$2\" == 'root -g' ]] && echo '${stub_root}/global'; [[ \$1 == root ]]; }"
            else
                npm() { if [[ "$1" == root ]]; then echo /nonexistent; return 0; fi; return 1; }
            fi
        fi
        if [[ "$4" == 1 ]]; then have_tty() { return 0; }; else have_tty() { return 1; }; fi
        ASSUME_YES="$5"
        NO_DEPS="$6"
        sdk_install_decision
    )
}

#            node      npm sdk tty yes nodeps   want
check_decision() {
    assert_equal "decision($*)" "$(decide "$1" "$2" "$3" "$4" "$5" "$6")" "$7"
}
check_decision ""        1 0 1 1 0 no-node
check_decision "v16.20.0" 1 0 1 1 0 node-old
check_decision "garbage"  1 0 1 1 0 node-old
check_decision "v20.11.1" 0 0 1 1 0 no-npm
check_decision "v20.11.1" 1 1 1 0 0 present
check_decision "v20.11.1" 1 1 0 0 1 present
check_decision "v18.0.0"  1 0 1 0 0 ask
check_decision "v22.1.0"  1 0 0 0 0 no-tty
check_decision "v22.1.0"  1 0 0 1 0 install
check_decision "v22.1.0"  1 0 1 1 0 install
check_decision "v22.1.0"  1 0 1 1 1 skip
check_decision "v22.1.0"  1 0 1 0 1 skip

# `npm ls -g` is the fallback when `npm root -g` points elsewhere.
assert_equal "npm ls fallback" "$(
    # shellcheck disable=SC2123 # emptying PATH is the point: hide real node/npm
    PATH="${stub_root}/empty"
    node() { echo v20.0.0; }
    npm() { case "$1" in root) echo /nonexistent ;; ls) return 0 ;; *) return 1 ;; esac; }
    ASSUME_YES=0 NO_DEPS=0
    sdk_install_decision
)" present

# EACCES from npm gets the user-level prefix hint, not sudo.
eacces_out="$(
    npm() { if [[ "$1" == install ]]; then echo "npm ERR! code EACCES" >&2; return 243; fi; echo /usr/local; }
    npm_install_global "@anthropic-ai/claude-agent-sdk" 2>&1
)" && { echo "npm_install_global: want failure on EACCES" >&2; exit 1; }
case "$eacces_out" in
    *"npm config set prefix ~/.npm-global"*) ;;
    *) echo "EACCES hint missing: $eacces_out" >&2; exit 1 ;;
esac
case "$eacces_out" in
    *sudo\ npm*) echo "EACCES hint must not suggest sudo npm" >&2; exit 1 ;;
esac

# --- Port detection ----------------------------------------------------------
cfg="${stub_root}/config.yaml"
for line_want in \
    'addr: "127.0.0.1:8090"|8090' \
    "addr: ':9000'|9000" \
    'addr: 0.0.0.0:7777   # LAN|7777' \
    'addr: ""|8080'; do
    printf 'server:\n  %s\n' "${line_want%|*}" >"$cfg"
    assert_equal "config_port ${line_want%|*}" "$(config_port "$cfg")" "${line_want##*|}"
done
assert_equal "config_port missing file" "$(config_port "${stub_root}/nope.yaml")" 8080

echo "install helper tests passed"

# --- End to end --------------------------------------------------------------
# Runs the real installer with a temp HOME and stubbed curl/uname/node/npm/
# systemctl; the "binary" just records the arguments bootstrap was given.
e2e="$(mktemp -d)"
trap 'rm -rf "$stub_root" "$e2e"' EXIT
mkdir -p "${e2e}/bin" "${e2e}/home/.daymug" "${e2e}/npm-global"
cat >"${e2e}/fake-daymug" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >"${HOME}/bootstrap-args"
printf 'server:\n  addr: "127.0.0.1:8090"\n' >"${HOME}/.daymug/config.yaml"
FAKE
chmod +x "${e2e}/fake-daymug"
if command -v sha256sum >/dev/null 2>&1; then
    fake_sha="$(sha256sum "${e2e}/fake-daymug" | awk '{print $1}')"
else
    fake_sha="$(shasum -a 256 "${e2e}/fake-daymug" | awk '{print $1}')"
fi
printf '{"version":"v9.9.9","binaries":{"linux/arm64":{"url":"https://mirror.example/v9.9.9/daymug-linux-arm64","sha256":"%s"}}}' \
    "$fake_sha" >"${e2e}/latest.json"

cat >"${e2e}/bin/curl" <<CURL
#!/usr/bin/env bash
# -fsSL <url> prints the manifest; -fL ... -o <file> <url> "downloads".
echo "\$*" >>"${e2e}/curl.log"
out=""
while [[ \$# -gt 0 ]]; do
    case "\$1" in -o) out="\$2"; shift 2 ;; *) url="\$1"; shift ;; esac
done
case "\$url" in
    https://github.com/fork/dm/releases/latest/download/manifest.json) cat "${e2e}/latest.json" ;;
    https://mirror.example/v9.9.9/daymug-linux-arm64) cp "${e2e}/fake-daymug" "\$out" ;;
    *) exit 22 ;;
esac
CURL
cat >"${e2e}/bin/uname" <<'UNAME'
#!/usr/bin/env bash
case "$1" in -s) echo Linux ;; -m) echo aarch64 ;; esac
UNAME
cat >"${e2e}/bin/node" <<'NODE'
#!/usr/bin/env bash
echo v20.11.1
NODE
cat >"${e2e}/bin/npm" <<NPM
#!/usr/bin/env bash
echo "\$*" >>"${e2e}/npm.log"
case "\$1" in
    root) echo "${e2e}/npm-global" ;;
    ls) exit 1 ;;
    install) mkdir -p "${e2e}/npm-global/\$3" ;;
esac
NPM
cat >"${e2e}/bin/systemctl" <<'SYSTEMCTL'
#!/usr/bin/env bash
# The unit exists; it is active unless bootstrap was told --no-start.
case "$*" in
    *is-active*) ! grep -q -- --no-start "${HOME}/bootstrap-args" ;;
    *) exit 0 ;;
esac
SYSTEMCTL
chmod +x "${e2e}/bin/"*

run_installer() {
    HOME="${e2e}/home" PATH="${e2e}/bin:/usr/bin:/bin" \
        DAYMUG_RELEASES_URL="https://github.com/fork/dm/releases" \
        bash "${SCRIPT_DIR}/install.sh" "$@" </dev/null 2>&1
}

e2e_out="$(run_installer --yes)"
assert_equal "e2e bootstrap args" "$(cat "${e2e}/home/bootstrap-args")" "bootstrap"
assert_equal "e2e manifest from mirror" "$(head -1 "${e2e}/curl.log")" \
    "-fsSL https://github.com/fork/dm/releases/latest/download/manifest.json"
command grep -q -- "install -g @anthropic-ai/claude-agent-sdk" "${e2e}/npm.log" \
    || { echo "e2e: --yes did not install the Agent SDK" >&2; exit 1; }
for want in "DayMug v9.9.9 installed" "Service:  started" "http://127.0.0.1:8090" \
    "daymug doctor" "logs/daymug.log" "installation.md#4-public-access" \
    "installed @anthropic-ai/claude-agent-sdk" "Codex (optional)"; do
    [[ "$e2e_out" == *"$want"* ]] || { printf 'e2e output missing %q:\n%s\n' "$want" "$e2e_out" >&2; exit 1; }
done
[[ "$e2e_out" != *nginx* ]] || { echo "e2e output still mentions nginx" >&2; exit 1; }

# Second run: SDK now present, --no-start passes through, nothing installed.
: >"${e2e}/npm.log"
e2e_out="$(run_installer --no-start)"
assert_equal "e2e --no-start args" "$(cat "${e2e}/home/bootstrap-args")" "bootstrap --no-start"
[[ "$e2e_out" == *"installed, not started"* ]] \
    || { printf 'e2e --no-start state wrong:\n%s\n' "$e2e_out" >&2; exit 1; }
if command grep -q install "${e2e}/npm.log"; then
    echo "e2e: reinstalled an SDK that was already present" >&2
    exit 1
fi

# No tty and no --yes: never install, just report. setsid drops the
# controlling terminal, which is what `curl | bash` under cron/CI looks like;
# macOS lacks setsid, so the case only runs where it exists.
if command -v setsid >/dev/null 2>&1; then
    rm -rf "${e2e}/npm-global/@anthropic-ai"
    : >"${e2e}/npm.log"
    e2e_out="$(e2e="$e2e" SCRIPT_DIR="$SCRIPT_DIR" \
        setsid bash -c "$(declare -f run_installer); run_installer")"
    if command grep -q install "${e2e}/npm.log"; then
        echo "e2e: installed without a tty or --yes" >&2
        exit 1
    fi
    [[ "$e2e_out" == *"Claude needs: npm install -g @anthropic-ai/claude-agent-sdk"* ]] \
        || { printf 'e2e no-tty note missing:\n%s\n' "$e2e_out" >&2; exit 1; }
fi

echo "install end-to-end tests passed"
