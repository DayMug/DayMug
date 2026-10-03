#!/usr/bin/env bash

set -u

mode="${1:-full}"
case "$mode" in
  full) frontend_command=(pnpm test) ;;
  fast) frontend_command=(pnpm test:fast) ;;
  *)
    echo "usage: $0 [full|fast]" >&2
    exit 2
    ;;
esac

(cd backend && go test ./...) &
backend_pid=$!
(cd frontend && "${frontend_command[@]}") &
frontend_pid=$!

status=0
wait "$backend_pid" || status=$?
wait "$frontend_pid" || status=$?
exit "$status"
