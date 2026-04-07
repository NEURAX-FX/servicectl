#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
TEST_ROOT="$(mktemp -d /tmp/group-orch.XXXXXX)"
SYSTEM_RUNTIME_ROOT="$TEST_ROOT/system"
USER_RUNTIME_ROOT="$TEST_ROOT/user"
GROUP_DIR="/etc/servicectl/groups.d"
GROUP_FILE="$GROUP_DIR/group-orchestration-chain.conf"
SYSTEM_SYSVISION_SOCK="$SYSTEM_RUNTIME_ROOT/sysvision/sysvisiond.sock"
BASE_UNIT="group-chain-base"
MID_UNIT="group-chain-mid"
TOP_UNIT="group-chain-top"
BASE_UNIT_PATH="/etc/systemd/system/${BASE_UNIT}.service"
MID_UNIT_PATH="/etc/systemd/system/${MID_UNIT}.service"
TOP_UNIT_PATH="/etc/systemd/system/${TOP_UNIT}.service"
PIDS=()

start_bg() {
  "$@" &
  PIDS+=("$!")
}

cleanup() {
  local pid
  for (( idx=${#PIDS[@]}-1; idx>=0; idx-- )); do
    pid="${PIDS[idx]}"
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" >/dev/null 2>&1 || true
    fi
  done
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" stop "$TOP_UNIT" >/dev/null 2>&1 || true
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" stop "$MID_UNIT" >/dev/null 2>&1 || true
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" stop "$BASE_UNIT" >/dev/null 2>&1 || true
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" disable group:test-chain >/dev/null 2>&1 || true
  rm -f "$GROUP_FILE" "$BASE_UNIT_PATH" "$MID_UNIT_PATH" "$TOP_UNIT_PATH"
  rm -f "/run/dinit.d/generated/${BASE_UNIT}" "/etc/dinit.d/${BASE_UNIT}"
  rm -f "/run/dinit.d/generated/${MID_UNIT}" "/etc/dinit.d/${MID_UNIT}"
  rm -f "/run/dinit.d/generated/${TOP_UNIT}" "/etc/dinit.d/${TOP_UNIT}"
  rm -f "/run/dinit.d/generated/${BASE_UNIT}-log" "/etc/dinit.d/${BASE_UNIT}-log"
  rm -f "/run/dinit.d/generated/${MID_UNIT}-log" "/etc/dinit.d/${MID_UNIT}-log"
  rm -f "/run/dinit.d/generated/${TOP_UNIT}-log" "/etc/dinit.d/${TOP_UNIT}-log"
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

assert_file_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Fq "$pattern" "$file"; then
    printf 'assertion failed: %s missing %s\n' "$file" "$pattern" >&2
    exit 1
  fi
}

wait_for_file_contains() {
  local file="$1"
  local pattern="$2"
  local label="$3"
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [[ -f "$file" ]] && grep -Fq "$pattern" "$file"; then
      return 0
    fi
    sleep 1
  done
  printf 'assertion failed: timed out waiting for %s in %s (%s)\n' "$pattern" "$file" "$label" >&2
  if [[ -f "$file" ]]; then
    cat "$file" >&2 || true
  fi
  exit 1
}

wait_for_active() {
  local unit="$1"
  local label="$2"
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" is-active "$unit" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  printf 'assertion failed: %s never became active\n' "$label" >&2
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" status "$unit" >&2 || true
  exit 1
}

wait_for_socket() {
  local path="$1"
  local label="$2"
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [[ -S "$path" ]]; then
      return 0
    fi
    sleep 1
  done
  printf 'assertion failed: %s socket never appeared at %s\n' "$label" "$path" >&2
  exit 1
}

printf 'Building group orchestration test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-propertyd" ./cmd/sys-propertyd
go build -o "$ROOT/sysvisiond" ./cmd/sysvisiond
go build -o "$ROOT/sys-orchestrd" ./cmd/sys-orchestrd

mkdir -p "$GROUP_DIR"

cat >"$GROUP_FILE" <<EOF
[Group]
Name=test-chain
Units=${TOP_UNIT}.service
Targets=test-chain.target
EOF

cat >"$BASE_UNIT_PATH" <<EOF
[Unit]
Description=Group chain base

[Service]
Type=simple
ExecStart=/bin/sleep 120
EOF

cat >"$MID_UNIT_PATH" <<EOF
[Unit]
Description=Group chain mid
Requires=${BASE_UNIT}.service
After=${BASE_UNIT}.service

[Service]
Type=simple
ExecStart=/bin/sleep 120
EOF

cat >"$TOP_UNIT_PATH" <<EOF
[Unit]
Description=Group chain top
Requires=${MID_UNIT}.service
After=${MID_UNIT}.service

[Service]
Type=simple
ExecStart=/bin/sleep 120
EOF

printf 'Starting isolated control plane...\n'
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" serve-api >/tmp/group-orch-api.log 2>&1
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/sys-propertyd" >/tmp/group-orch-propertyd.log 2>&1
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/sysvisiond" >/tmp/group-orch-sysvisiond.log 2>&1
wait_for_socket "$SYSTEM_SYSVISION_SOCK" "sysvisiond"
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" SERVICECTL_BIN="$ROOT/servicectl" "$ROOT/sys-orchestrd" --unit "${TOP_UNIT}.service" >/tmp/group-orch-top.log 2>&1
sleep 2

printf 'Enabling group through target alias...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" enable test-chain.target >/tmp/group-orch-enable.out
assert_file_contains /tmp/group-orch-enable.out 'Enabled group:test-chain'

printf 'Checking dependency chain startup...\n'
wait_for_active "$TOP_UNIT" "$TOP_UNIT"
wait_for_active "$MID_UNIT" "$MID_UNIT"
wait_for_active "$BASE_UNIT" "$BASE_UNIT"

printf 'Disabling group and checking top service stop...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" disable test-chain.target >/tmp/group-orch-disable.out
assert_file_contains /tmp/group-orch-disable.out 'Disabled group:test-chain'
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if ! "$ROOT/servicectl" is-active "$TOP_UNIT" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" is-active "$TOP_UNIT" >/dev/null 2>&1; then
  printf 'assertion failed: %s still active after disable\n' "$TOP_UNIT" >&2
  exit 1
fi

printf 'group orchestration integration test passed.\n'
