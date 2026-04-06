#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"

HOST_INTEGRATION_ENABLED=0
if [[ "${SERVICECTL_RUN_HOST_INTEGRATION:-}" == "1" || "${SERVICECTL_RUN_HOST_INTEGRATION:-}" == "true" ]]; then
  HOST_INTEGRATION_ENABLED=1
fi

run_step() {
  local label="$1"
  shift
  printf '\n== %s ==\n' "$label"
  "$@"
}

run_host_step() {
  local label="$1"
  shift
  if [[ "$HOST_INTEGRATION_ENABLED" -eq 1 ]]; then
    run_step "$label" "$@"
    return
  fi
  printf '\n== %s ==\n' "$label"
  printf 'Skipped host integration suite. Set SERVICECTL_RUN_HOST_INTEGRATION=1 to run it.\n'
}

run_step "Go build" go build ./...
run_step "Go test" go test ./...
run_step "sys-notifyd integration" bash "$ROOT/scripts/test-sys-notifyd.sh"
run_host_step "System/User environment" bash "$ROOT/scripts/test-system-user-env.sh"
run_host_step "Directory directives" bash "$ROOT/scripts/test-directory-directives.sh"
run_host_step "logs -f integration" bash "$ROOT/scripts/test-logs-follow.sh"
run_host_step "System restart dependency tree" bash "$ROOT/scripts/test-restart-system-tree.sh"
run_host_step "Stale socket cleanup" bash "$ROOT/scripts/test-stale-socket-cleanup.sh"
run_host_step "servicectl+dinit integration" bash "$ROOT/scripts/test-servicectl-dinit.sh"
run_host_step "notify-managed integration" bash "$ROOT/scripts/test-notify-managed.sh"
run_host_step "sysvisiond bus" bash "$ROOT/scripts/test-sysvisiond-bus.sh"
run_host_step "sys-orchestrd integration" bash "$ROOT/scripts/test-sys-orchestrd.sh"
run_host_step "s6 orchestrd backend" bash "$ROOT/scripts/test-s6-orchestrd.sh"

printf '\nAll test suites passed.\n'
