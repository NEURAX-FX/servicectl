#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
UNIT_NAME="stale-socket-cleanup-demo"
SERVICE_PATH="/root/.config/systemd/user/${UNIT_NAME}.service"
SOCKET_UNIT_PATH="/root/.config/systemd/user/${UNIT_NAME}.socket"
SOCKET_PATH="/tmp/${UNIT_NAME}.sock"
START_OUTPUT="$(mktemp /tmp/${UNIT_NAME}-start.XXXXXX)"

cleanup() {
  XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/runtime-0}" "$ROOT/servicectl" --user stop "$UNIT_NAME" >/dev/null 2>&1 || true
  dinitctl --user stop "${UNIT_NAME}-socketd-log" >/dev/null 2>&1 || true
  pkill -P $$ socat >/dev/null 2>&1 || true
  rm -f "$SERVICE_PATH" "$SOCKET_UNIT_PATH" "$SOCKET_PATH" "$SOCKET_PATH.lock" "$START_OUTPUT"
  rm -f "${XDG_RUNTIME_DIR:-/tmp/runtime-0}/dinit.d/generated/${UNIT_NAME}-socketd" "/root/.config/dinit.d/${UNIT_NAME}-socketd"
  rm -f "${XDG_RUNTIME_DIR:-/tmp/runtime-0}/dinit.d/generated/${UNIT_NAME}-socketd-log" "/root/.config/dinit.d/${UNIT_NAME}-socketd-log"
  rm -f "${XDG_RUNTIME_DIR:-/tmp/runtime-0}/dinit.d/generated/${UNIT_NAME}-socketd.state"
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

printf 'Building socket cleanup test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-notifyd" ./cmd/sys-notifyd
go build -o "$ROOT/sys-logd" ./cmd/sys-logd
go build -o "$ROOT/notify-echod" ./cmd/notify-echod

cat >"$SERVICE_PATH" <<EOF
[Unit]
Description=Stale socket cleanup demo
Requires=${UNIT_NAME}.socket

[Service]
Type=notify
ExecStart=$ROOT/notify-echod
EOF

cat >"$SOCKET_UNIT_PATH" <<EOF
[Unit]
Description=Stale socket cleanup demo socket

[Socket]
ListenStream=$SOCKET_PATH
SocketMode=0666
EOF

printf 'Starting blocking socket holder...\n'
socat UNIX-LISTEN:"$SOCKET_PATH",fork EXEC:/bin/cat >/dev/null 2>&1 &
BLOCKER_PID=$!
sleep 1
kill -0 "$BLOCKER_PID"

printf 'Starting servicectl-managed socket service...\n'
SERVICECTL_KILL_STALE_SOCKET_HOLDERS=1 XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/runtime-0}" "$ROOT/servicectl" --user start "$UNIT_NAME" >"$START_OUTPUT"
sleep 1

assert_contains "$START_OUTPUT" "Cleaning stale socket holders for ${UNIT_NAME}: ${BLOCKER_PID}"
if kill -0 "$BLOCKER_PID" >/dev/null 2>&1; then
  printf 'assertion failed: blocker pid still running: %s\n' "$BLOCKER_PID" >&2
  exit 1
fi

printf 'Checking managed socket activation after cleanup...\n'
RESPONSE="$(printf '' | socat - UNIX-CONNECT:"$SOCKET_PATH")"
if [[ "$RESPONSE" != "hello from notify-echod" ]]; then
  printf 'assertion failed: unexpected socket response: %s\n' "$RESPONSE" >&2
  exit 1
fi

printf 'Stale socket cleanup test passed.\n'
