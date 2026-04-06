#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
UNIT_NAME="notify-managed-demo"
UNIT_PATH="/etc/systemd/system/${UNIT_NAME}.service"
SHOW_OUTPUT="$(mktemp /tmp/${UNIT_NAME}-show.XXXXXX)"
STATUS_OUTPUT="$(mktemp /tmp/${UNIT_NAME}-status.XXXXXX)"
LOG_OUTPUT="$(mktemp /tmp/${UNIT_NAME}-logs.XXXXXX)"

cleanup() {
  "$ROOT/servicectl" stop "$UNIT_NAME" >/dev/null 2>&1 || true
  dinitctl stop "${UNIT_NAME}-notifyd-log" >/dev/null 2>&1 || true
  rm -f "$UNIT_PATH"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}-notifyd" "/etc/dinit.d/${UNIT_NAME}-notifyd"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}-notifyd-log" "/etc/dinit.d/${UNIT_NAME}-notifyd-log"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}-notifyd.state"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}-notifyd.notify.sock"
  rm -f "$SHOW_OUTPUT" "$STATUS_OUTPUT" "$LOG_OUTPUT"
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

assert_not_contains() {
  local file="$1"
  local pattern="$2"
  if grep -Fq "$pattern" "$file"; then
    printf 'assertion failed: %s unexpectedly contains %s\n' "$file" "$pattern" >&2
    exit 1
  fi
}

printf 'Building notify-managed test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-notifyd" ./cmd/sys-notifyd
go build -o "$ROOT/sys-logd" ./cmd/sys-logd
go build -o "$ROOT/notify-sleeper" ./cmd/notify-sleeper

cat >"$UNIT_PATH" <<EOF
[Unit]
Description=Notify-managed demo

[Service]
Type=notify
ExecStart=$ROOT/notify-sleeper
ExecStop=/bin/kill -TERM \$MAINPID
EOF

printf 'Starting notify-managed demo...\n'
"$ROOT/servicectl" start "$UNIT_NAME"
sleep 1

printf 'Checking generated backend name...\n'
[[ -f "/run/dinit.d/generated/${UNIT_NAME}-notifyd" ]] || { printf 'missing generated notifyd service\n' >&2; exit 1; }

printf 'Checking show output...\n'
"$ROOT/servicectl" show "$UNIT_NAME" >"$SHOW_OUTPUT"
assert_contains "$SHOW_OUTPUT" "Managed By     sys-notifyd"
assert_contains "$SHOW_OUTPUT" "Activation Model notifyd"
assert_contains "$SHOW_OUTPUT" "Dinit          ${UNIT_NAME}-notifyd"
assert_contains "$SHOW_OUTPUT" "Service Type   notify"

printf 'Checking status output...\n'
"$ROOT/servicectl" status "$UNIT_NAME" >"$STATUS_OUTPUT"
assert_contains "$STATUS_OUTPUT" "Manager PID:"
assert_contains "$STATUS_OUTPUT" "Main PID:"

printf 'Checking logs for notify handshake...\n'
"$ROOT/servicectl" logs -n 50 "$UNIT_NAME" >"$LOG_OUTPUT"
assert_contains "$LOG_OUTPUT" "READY=1"
assert_contains "$LOG_OUTPUT" "notify-sleeper running"
assert_not_contains "$LOG_OUTPUT" "xsystemd_change_mainpid: connect() failed for /dev/null"

printf 'Stopping notify-managed demo...\n'
"$ROOT/servicectl" stop "$UNIT_NAME"

printf 'notify-managed integration test passed.\n'
