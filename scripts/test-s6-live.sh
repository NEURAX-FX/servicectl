#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
UNIT_NAME="s6-live-demo"
UNIT_PATH="/etc/systemd/system/${UNIT_NAME}.service"
SERVICE_NAME="${UNIT_NAME}-orchestrd"
SERVICE_DIR="/s6/rc/${SERVICE_NAME}"
SERVICECTL_S6_LIVE=1

cleanup() {
  SERVICECTL_S6_LIVE=1 "$ROOT/servicectl" disable "$UNIT_NAME" >/dev/null 2>&1 || true
  rm -f "$UNIT_PATH"
}
trap cleanup EXIT

assert_contains_text() {
  local text="$1"
  local pattern="$2"
  if [[ "$text" != *"$pattern"* ]]; then
    printf 'assertion failed: output missing %s\n' "$pattern" >&2
    exit 1
  fi
}

live_db_list() {
  local live_state
  live_state="$(readlink -f /run/s6/state)"
  s6-rc-db -l "$live_state" list all
}

assert_list_contains() {
  local pattern="$1"
  local output
  output="$(live_db_list || true)"
  assert_contains_text "$output" "$pattern"
}

assert_list_not_contains() {
  local pattern="$1"
  local output
  output="$(live_db_list || true)"
  if [[ "$output" == *"$pattern"* ]]; then
    printf 'assertion failed: live list unexpectedly contains %s\n' "$pattern" >&2
    exit 1
  fi
}

if [[ "${EUID}" -ne 0 ]]; then
  printf 'scripts/test-s6-live.sh must run as root\n' >&2
  exit 1
fi

if [[ ! -L /run/s6/state || ! -S /run/dinitctl ]]; then
  printf 'live s6/dinit environment is not ready; refusing to run\n' >&2
  exit 1
fi

if ! live_db_list >/dev/null 2>&1; then
  printf 'current s6 live database is not readable; refusing to run\n' >&2
  exit 1
fi

printf 'Building live validation binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sysvisiond" ./cmd/sysvisiond
go build -o "$ROOT/sys-orchestrd" ./cmd/sys-orchestrd

cat >"$UNIT_PATH" <<'EOF'
[Unit]
Description=s6 live validation demo

[Service]
Type=simple
ExecStart=/bin/sleep 120
EOF

printf 'Enabling live orchestrd service...\n'
ENABLE_OUTPUT="$(SERVICECTL_S6_LIVE=1 "$ROOT/servicectl" enable "$UNIT_NAME")"
assert_contains_text "$ENABLE_OUTPUT" "Enabled ${UNIT_NAME}"

printf 'Checking live s6 state...\n'
sleep 1
assert_list_contains "sysvisiond"
assert_list_contains "$SERVICE_NAME"

if [[ ! -d "$SERVICE_DIR" ]]; then
  printf 'assertion failed: missing generated service dir %s\n' "$SERVICE_DIR" >&2
  exit 1
fi

printf 'Disabling live orchestrd service...\n'
DISABLE_OUTPUT="$(SERVICECTL_S6_LIVE=1 "$ROOT/servicectl" disable "$UNIT_NAME")"
assert_contains_text "$DISABLE_OUTPUT" "Disabled ${UNIT_NAME}"

printf 'Checking live s6 teardown...\n'
sleep 1
assert_list_not_contains "$SERVICE_NAME"

printf 's6 live validation passed.\n'
