#!/bin/sh
#
# Container entrypoint: make the state volume usable, write a starter config
# on first run, then hand over to the daymug binary (`serve` by default).
#
# Runs on every start, so everything here must be idempotent: a restarted or
# recreated container finds its existing config and data untouched.

set -eu

STATE_DIR=/var/lib/daymug

# /opt/daymug/{data,logs} are symlinks into the volume. A fresh named volume
# inherits these directories from the image, but a bind mount starts empty,
# and serve cannot MkdirAll through a dangling symlink.
mkdir -p "${STATE_DIR}/data" "${STATE_DIR}/logs" "${STATE_DIR}/users" "${HOME}"

# First run: `init` writes config.yaml with users.default_home_root on the
# volume. Its 127.0.0.1:8090 listen address is overridden by DAYMUG_ADDR.
# Only for the default location — a custom DAYMUG_CONFIG that doesn't exist
# is the operator's mistake, and serve's "no config" error says so.
if [ "${DAYMUG_CONFIG:-}" = "${STATE_DIR}/config.yaml" ] && [ ! -e "${DAYMUG_CONFIG}" ]; then
    echo "docker-entrypoint: no config at ${DAYMUG_CONFIG}; running 'daymug init'"
    (cd "${STATE_DIR}" && daymug init)
fi

exec daymug "$@"
