#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"

run_step() {
  local label="$1"
  shift
  printf '\n== %s ==\n' "$label"
  "$@"
}

run_step "Go build" go build ./...
run_step "Go test" go test ./...
run_step "System/User environment" bash "$ROOT/scripts/test-system-user-env.sh"
run_step "Directory directives" bash "$ROOT/scripts/test-directory-directives.sh"
run_step "logs -f integration" bash "$ROOT/scripts/test-logs-follow.sh"
run_step "System restart dependency tree" bash "$ROOT/scripts/test-restart-system-tree.sh"
run_step "Stale socket cleanup" bash "$ROOT/scripts/test-stale-socket-cleanup.sh"
run_step "servicectl+dinit integration" bash "$ROOT/scripts/test-servicectl-dinit.sh"
run_step "sys-notifyd integration" bash "$ROOT/scripts/test-sys-notifyd.sh"
run_step "sysvisiond bus" bash "$ROOT/scripts/test-sysvisiond-bus.sh"
run_step "sys-orchestrd integration" bash "$ROOT/scripts/test-sys-orchestrd.sh"
run_step "s6 orchestrd backend" bash "$ROOT/scripts/test-s6-orchestrd.sh"

printf '\nAll test suites passed.\n'
