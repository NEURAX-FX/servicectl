#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"

warn_host_integration() {
  if [[ "${SERVICECTL_NO_HOST_WARNING:-}" == "1" || "${SERVICECTL_NO_HOST_WARNING:-}" == "true" ]]; then
    return
  fi
  printf 'WARNING: running host integration suites against this machine.\n'
  printf 'This may start/stop real services and write servicectl/systemd/s6 runtime state.\n'
  printf 'Continuing in 5 seconds. Press Ctrl-C to abort.\n'
  sleep 5
}

run_step() {
  local label="$1"
  shift
  printf '\n== %s ==\n' "$label"
  "$@"
}

run_step "Go build" go build ./...
run_step "Go test" go test ./...
warn_host_integration
run_step "sys-notifyd integration" bash "$ROOT/scripts/test-sys-notifyd.sh"
run_step "System/User environment" bash "$ROOT/scripts/test-system-user-env.sh"
run_step "Directory directives" bash "$ROOT/scripts/test-directory-directives.sh"
run_step "logs -f integration" bash "$ROOT/scripts/test-logs-follow.sh"
run_step "System restart dependency tree" bash "$ROOT/scripts/test-restart-system-tree.sh"
run_step "Stale socket cleanup" bash "$ROOT/scripts/test-stale-socket-cleanup.sh"
run_step "servicectl+dinit integration" bash "$ROOT/scripts/test-servicectl-dinit.sh"
run_step "notify-managed integration" bash "$ROOT/scripts/test-notify-managed.sh"
run_step "external-managed integration" bash "$ROOT/scripts/test-external-management.sh"
run_step "property target integration" bash "$ROOT/scripts/test-property-targets.sh"
run_step "user group target integration" bash "$ROOT/scripts/test-user-group-targets.sh"
run_step "group orchestration integration" bash "$ROOT/scripts/test-group-orchestration.sh"
run_step "sysvisiond bus" bash "$ROOT/scripts/test-sysvisiond-bus.sh"
run_step "sys-orchestrd integration" bash "$ROOT/scripts/test-sys-orchestrd.sh"
run_step "s6 orchestrd backend" bash "$ROOT/scripts/test-s6-orchestrd.sh"

printf '\nAll test suites passed.\n'
