#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
TEST_ROOT="$(mktemp -d /tmp/external-management.XXXXXX)"
SYSTEM_RUNTIME_ROOT="$TEST_ROOT/system"
USER_RUNTIME_ROOT="$TEST_ROOT/user"
SYSTEM_PROPERTY_SOCK="$SYSTEM_RUNTIME_ROOT/sys-propertyd.sock"
UNIT_NAME="external-managed-demo"
UNIT_PATH="/etc/systemd/system/${UNIT_NAME}.service"
STATUS_OUTPUT="$(mktemp /tmp/external-managed-status.XXXXXX)"
SHOW_OUTPUT="$(mktemp /tmp/external-managed-show.XXXXXX)"
SYSTEM_PROPS="$(mktemp /tmp/external-managed-system-props.XXXXXX)"
USER_PROPS="$(mktemp /tmp/external-managed-user-props.XXXXXX)"
NO_MODE_OUTPUT="$(mktemp /tmp/external-managed-nomode.XXXXXX)"
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

assert_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Fq -- "$pattern" "$file"; then
    printf 'assertion failed: %s missing %s\n' "$file" "$pattern" >&2
    exit 1
  fi
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
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" external-unmanage "$UNIT_NAME" >/dev/null 2>&1 || true
  env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" stop "$UNIT_NAME" >/dev/null 2>&1 || true
  rm -f "$UNIT_PATH" "$STATUS_OUTPUT" "$SHOW_OUTPUT" "$SYSTEM_PROPS" "$USER_PROPS" "$NO_MODE_OUTPUT"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}" "/etc/dinit.d/${UNIT_NAME}"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}-log" "/etc/dinit.d/${UNIT_NAME}-log"
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

printf 'Building external-management test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-propertyd" ./cmd/sys-propertyd

cat >"$UNIT_PATH" <<EOF
[Unit]
Description=External managed demo

[Service]
Type=simple
ExecStart=/bin/sleep 60
EOF

printf 'Starting isolated control plane...\n'
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" serve-api >/tmp/external-management-api.log 2>&1
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/sys-propertyd" >/tmp/external-management-propertyd.log 2>&1
wait_for_socket "$SYSTEM_PROPERTY_SOCK" 'sys-propertyd'

printf 'Checking explicit external management commands...\n'
MANAGE_OUTPUT="$(env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" external-manage "$UNIT_NAME")"
if [[ "$MANAGE_OUTPUT" != *"Marked external-managed: ${UNIT_NAME}"* ]]; then
  printf 'assertion failed: unexpected external-manage output: %s\n' "$MANAGE_OUTPUT" >&2
  exit 1
fi

ENABLED_OUTPUT="$(env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" is-enabled "$UNIT_NAME" || true)"
if [[ "$ENABLED_OUTPUT" != "enabled" ]]; then
  printf 'assertion failed: expected enabled, got %s\n' "$ENABLED_OUTPUT" >&2
  exit 1
fi

printf 'Checking status and show expose external management...\n'
env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" status "$UNIT_NAME" >"$STATUS_OUTPUT"
assert_contains "$STATUS_OUTPUT" "${UNIT_NAME}.service"
assert_contains "$STATUS_OUTPUT" 'External managed demo'

env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" show "$UNIT_NAME" >"$SHOW_OUTPUT"
assert_contains "$SHOW_OUTPUT" "servicectl show ${UNIT_NAME}.service"
assert_contains "$SHOW_OUTPUT" 'Description    External managed demo'

printf 'Checking property API mode requirements and shared persistent state...\n'
NO_MODE_STATUS="$(curl --silent --show-error --output "$NO_MODE_OUTPUT" --write-out '%{http_code}' --unix-socket "$SYSTEM_PROPERTY_SOCK" http://unix/v1/properties)"
if [[ "$NO_MODE_STATUS" != "400" ]]; then
  printf 'assertion failed: expected 400 without mode, got %s\n' "$NO_MODE_STATUS" >&2
  exit 1
fi
assert_contains "$NO_MODE_OUTPUT" 'mode is required'

curl --silent --show-error --unix-socket "$SYSTEM_PROPERTY_SOCK" 'http://unix/v1/properties?mode=system' >"$SYSTEM_PROPS"
curl --silent --show-error --unix-socket "$SYSTEM_PROPERTY_SOCK" 'http://unix/v1/properties?mode=user' >"$USER_PROPS"
assert_contains "$SYSTEM_PROPS" 'persist.external-managed.external-managed-demo'
assert_contains "$USER_PROPS" 'persist.external-managed.external-managed-demo'

printf 'Checking external-unmanage clears effective enablement...\n'
UNMANAGE_OUTPUT="$(env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" external-unmanage "$UNIT_NAME")"
if [[ "$UNMANAGE_OUTPUT" != *"Cleared external-managed: ${UNIT_NAME}"* ]]; then
  printf 'assertion failed: unexpected external-unmanage output: %s\n' "$UNMANAGE_OUTPUT" >&2
  exit 1
fi

DISABLED_OUTPUT="$(env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" is-enabled "$UNIT_NAME" || true)"
if [[ "$DISABLED_OUTPUT" != "disabled" ]]; then
  printf 'assertion failed: expected disabled, got %s\n' "$DISABLED_OUTPUT" >&2
  exit 1
fi

printf 'external-management integration test passed.\n'
