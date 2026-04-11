#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
TEST_ROOT="$(mktemp -d /tmp/property-target-runtime.XXXXXX)"
SYSTEM_RUNTIME_ROOT="$TEST_ROOT/system"
USER_RUNTIME_ROOT="$TEST_ROOT/user"
GROUP_DIR="/etc/servicectl/groups.d"
TARGET_DIR="/etc/servicectl/targets.d"
GROUP_FILE="$GROUP_DIR/test-audio.conf"
TARGET_FILE="$TARGET_DIR/test-audio.conf"
UNIT_NAME="target-audio-demo"
UNIT_PATH="/etc/systemd/system/${UNIT_NAME}.service"
SYSTEMD_TARGET_PATH="/etc/systemd/system/pipewire.target"
SYSTEMD_TARGET_WANTS_DIR="/etc/systemd/system/pipewire.target.wants"
SYSTEM_PROPERTY_SOCK="$SYSTEM_RUNTIME_ROOT/sys-propertyd.sock"
SHOW_OUTPUT="$(mktemp /tmp/property-target-show.XXXXXX)"
GROUP_OUTPUT="$(mktemp /tmp/property-target-group.XXXXXX)"
PIDS=()

start_bg() {
  "$@" &
  PIDS+=("$!")
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

cleanup() {
  local pid
  for (( idx=${#PIDS[@]}-1; idx>=0; idx-- )); do
    pid="${PIDS[idx]}"
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" >/dev/null 2>&1 || true
    fi
  done
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --group pipewire disable >/dev/null 2>&1 || true
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" stop "$UNIT_NAME" >/dev/null 2>&1 || true
  dinitctl stop "${UNIT_NAME}-log" >/dev/null 2>&1 || true
  rm -f "$GROUP_FILE" "$TARGET_FILE" "$UNIT_PATH" "$SYSTEMD_TARGET_PATH"
  rm -rf "$SYSTEMD_TARGET_WANTS_DIR"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}" "/etc/dinit.d/${UNIT_NAME}"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}-log" "/etc/dinit.d/${UNIT_NAME}-log"
  rm -f "$SHOW_OUTPUT" "$GROUP_OUTPUT"
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

assert_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Fq "$pattern" "$file"; then
    printf 'assertion failed: %s missing %s\n' "$file" "$pattern" >&2
    exit 1
  fi
}

printf 'Building property target binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-propertyd" ./cmd/sys-propertyd
go build -o "$ROOT/sysvisiond" ./cmd/sysvisiond

mkdir -p "$GROUP_DIR" "$TARGET_DIR"

printf 'Starting isolated control plane...\n'
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" serve-api >/tmp/property-target-api.log 2>&1
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/sys-propertyd" >/tmp/property-target-propertyd.log 2>&1
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/sysvisiond" >/tmp/property-target-sysvisiond.log 2>&1
wait_for_socket "$SYSTEM_PROPERTY_SOCK" "sys-propertyd"

cat >"$UNIT_PATH" <<EOF
[Unit]
Description=Target audio demo

[Service]
Type=simple
ExecStart=/bin/sleep 30
EOF

cat >"$SYSTEMD_TARGET_PATH" <<EOF
[Unit]
Description=PipeWire target
Wants=${UNIT_NAME}.service
EOF
mkdir -p "$SYSTEMD_TARGET_WANTS_DIR"
ln -sf "$UNIT_PATH" "$SYSTEMD_TARGET_WANTS_DIR/${UNIT_NAME}.service"

printf 'Enabling target alias...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" enable pipewire.target >"$SHOW_OUTPUT"
assert_contains "$SHOW_OUTPUT" "Enabled group:pipewire"

printf 'Checking group status...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --group pipewire status >"$GROUP_OUTPUT"
assert_contains "$GROUP_OUTPUT" "group:pipewire enabled"
assert_contains "$GROUP_OUTPUT" "${UNIT_NAME}.service"
assert_contains "$GROUP_OUTPUT" "pipewire.target"

printf 'Checking alternate --group syntax...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --group status pipewire >"$GROUP_OUTPUT"
assert_contains "$GROUP_OUTPUT" "group:pipewire enabled"

printf 'Checking selector-based --group enable path...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" disable pipewire.target >/dev/null
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --group enable pipewire >"$SHOW_OUTPUT"
assert_contains "$SHOW_OUTPUT" "Enabled group:pipewire"

printf 'Checking service auto-resolution to unique group...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" status "$UNIT_NAME" >"$GROUP_OUTPUT"
assert_contains "$GROUP_OUTPUT" "${UNIT_NAME}.service"
assert_contains "$GROUP_OUTPUT" "Target audio demo"

printf 'Checking is-enabled for target...\n'
TARGET_ENABLED="$(env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" is-enabled pipewire.target || true)"
if [[ "$TARGET_ENABLED" != "enabled" ]]; then
  printf 'assertion failed: expected enabled, got %s\n' "$TARGET_ENABLED" >&2
  exit 1
fi

printf 'Disabling target alias...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" disable pipewire.target >"$SHOW_OUTPUT"
assert_contains "$SHOW_OUTPUT" "Disabled group:pipewire"

TARGET_DISABLED="$(env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" is-enabled pipewire.target || true)"
if [[ "$TARGET_DISABLED" != "disabled" ]]; then
  printf 'assertion failed: expected disabled, got %s\n' "$TARGET_DISABLED" >&2
  exit 1
fi

printf 'property target integration test passed.\n'
