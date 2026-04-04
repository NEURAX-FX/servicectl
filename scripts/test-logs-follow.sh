#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
WORKDIR="$(mktemp -d /tmp/servicectl-logs-follow.XXXXXX)"

USER_UNIT_NAME="user-follow-demo"
SYSTEM_UNIT_NAME="system-follow-demo"
USER_UNIT_PATH="/root/.config/systemd/user/${USER_UNIT_NAME}.service"
SYSTEM_UNIT_PATH="/etc/systemd/system/${SYSTEM_UNIT_NAME}.service"

USER_RUNTIME_BASE="$WORKDIR/user-runtime"
USER_FOLLOW_OUT="$WORKDIR/user-follow.out"
SYSTEM_FOLLOW_OUT="$WORKDIR/system-follow.out"

cleanup() {
  XDG_RUNTIME_DIR="$USER_RUNTIME_BASE" "$ROOT/servicectl" --user stop "$USER_UNIT_NAME" >/dev/null 2>&1 || true
  "$ROOT/servicectl" stop "$SYSTEM_UNIT_NAME" >/dev/null 2>&1 || true
  dinitctl --user stop "${USER_UNIT_NAME}-log" >/dev/null 2>&1 || true
  dinitctl stop "${SYSTEM_UNIT_NAME}-log" >/dev/null 2>&1 || true
  pkill -P $$ timeout >/dev/null 2>&1 || true
  rm -f "$USER_UNIT_PATH" "$SYSTEM_UNIT_PATH"
  rm -f "${USER_RUNTIME_BASE}/dinit.d/generated/${USER_UNIT_NAME}" "/root/.config/dinit.d/${USER_UNIT_NAME}"
  rm -f "/run/dinit.d/generated/${SYSTEM_UNIT_NAME}" "/etc/dinit.d/${SYSTEM_UNIT_NAME}"
  rm -rf "$USER_RUNTIME_BASE" "$WORKDIR"
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

wait_for_contains() {
  local file="$1"
  local pattern="$2"
  local seconds="$3"
  local i
  for ((i = 0; i < seconds * 10; i++)); do
    if [[ -f "$file" ]] && grep -Fq "$pattern" "$file"; then
      return 0
    fi
    sleep 0.1
  done
  printf 'assertion failed: timed out waiting for %s in %s\n' "$pattern" "$file" >&2
  exit 1
}

printf 'Building follow-log test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-logd" ./cmd/sys-logd
go build -o "$ROOT/test-envd" ./cmd/test-envd

mkdir -p "$USER_RUNTIME_BASE"

cat >"$SYSTEM_UNIT_PATH" <<EOF
[Unit]
Description=System follow logs demo

[Service]
Type=simple
ExecStart=$ROOT/test-envd -ready-message system-follow-ready -sleep-seconds 300
EOF

cat >"$USER_UNIT_PATH" <<EOF
[Unit]
Description=User follow logs demo

[Service]
Type=simple
ExecStart=$ROOT/test-envd -ready-message user-follow-ready -sleep-seconds 300
EOF

printf 'Checking system-mode logs -f...\n'
timeout 10s "$ROOT/servicectl" logs -f "$SYSTEM_UNIT_NAME" >"$SYSTEM_FOLLOW_OUT" 2>&1 &
SYSTEM_FOLLOW_PID=$!
sleep 1
"$ROOT/servicectl" start "$SYSTEM_UNIT_NAME"
wait_for_contains "$SYSTEM_FOLLOW_OUT" "system-follow-ready" 8
kill "$SYSTEM_FOLLOW_PID" >/dev/null 2>&1 || true
wait "$SYSTEM_FOLLOW_PID" >/dev/null 2>&1 || true
assert_contains "$SYSTEM_FOLLOW_OUT" "servicectl[$SYSTEM_UNIT_NAME]"

printf 'Checking user-mode logs -f...\n'
HOME=/root XDG_RUNTIME_DIR="$USER_RUNTIME_BASE" timeout 10s "$ROOT/servicectl" --user logs -f "$USER_UNIT_NAME" >"$USER_FOLLOW_OUT" 2>&1 &
USER_FOLLOW_PID=$!
sleep 1
HOME=/root XDG_RUNTIME_DIR="$USER_RUNTIME_BASE" "$ROOT/servicectl" --user start "$USER_UNIT_NAME"
wait_for_contains "$USER_FOLLOW_OUT" "user-follow-ready" 8
kill "$USER_FOLLOW_PID" >/dev/null 2>&1 || true
wait "$USER_FOLLOW_PID" >/dev/null 2>&1 || true
assert_contains "$USER_FOLLOW_OUT" "servicectl[$USER_UNIT_NAME]"

printf 'logs -f test passed for system and user modes.\n'
