#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
WORKDIR="$(mktemp -d /tmp/servicectl-restart-tree.XXXXXX)"

LEAF_UNIT="restart-tree-leaf"
MID_UNIT="restart-tree-mid"
TOP_UNIT="restart-tree-top"

LEAF_UNIT_PATH="/etc/systemd/system/${LEAF_UNIT}.service"
MID_UNIT_PATH="/etc/systemd/system/${MID_UNIT}.service"
TOP_UNIT_PATH="/etc/systemd/system/${TOP_UNIT}.service"

LEAF_PID_FILE="$WORKDIR/${LEAF_UNIT}.pid"
MID_PID_FILE="$WORKDIR/${MID_UNIT}.pid"
TOP_PID_FILE="$WORKDIR/${TOP_UNIT}.pid"
RESTART_OUTPUT="$WORKDIR/restart.out"

cleanup() {
  "$ROOT/servicectl" stop "$TOP_UNIT" >/dev/null 2>&1 || true
  "$ROOT/servicectl" stop "$MID_UNIT" >/dev/null 2>&1 || true
  "$ROOT/servicectl" stop "$LEAF_UNIT" >/dev/null 2>&1 || true
  dinitctl stop "${TOP_UNIT}-log" >/dev/null 2>&1 || true
  dinitctl stop "${MID_UNIT}-log" >/dev/null 2>&1 || true
  dinitctl stop "${LEAF_UNIT}-log" >/dev/null 2>&1 || true
  rm -f "$LEAF_UNIT_PATH" "$MID_UNIT_PATH" "$TOP_UNIT_PATH"
  rm -f "/run/dinit.d/generated/${LEAF_UNIT}" "/etc/dinit.d/${LEAF_UNIT}"
  rm -f "/run/dinit.d/generated/${MID_UNIT}" "/etc/dinit.d/${MID_UNIT}"
  rm -f "/run/dinit.d/generated/${TOP_UNIT}" "/etc/dinit.d/${TOP_UNIT}"
  rm -rf "$WORKDIR"
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

read_pid() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    printf 'assertion failed: missing pid file %s\n' "$file" >&2
    exit 1
  fi
  tr -d '[:space:]' <"$file"
}

printf 'Building restart-tree test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-logd" ./cmd/sys-logd
go build -o "$ROOT/test-envd" ./cmd/test-envd

cat >"$LEAF_UNIT_PATH" <<EOF
[Unit]
Description=Restart tree leaf demo

[Service]
Type=simple
ExecStart=$ROOT/test-envd -pid-file $LEAF_PID_FILE -ready-message ${LEAF_UNIT}-ready -sleep-seconds 300
EOF

cat >"$MID_UNIT_PATH" <<EOF
[Unit]
Description=Restart tree mid demo
Requires=${LEAF_UNIT}.service
After=${LEAF_UNIT}.service

[Service]
Type=simple
ExecStart=$ROOT/test-envd -pid-file $MID_PID_FILE -ready-message ${MID_UNIT}-ready -sleep-seconds 300
EOF

cat >"$TOP_UNIT_PATH" <<EOF
[Unit]
Description=Restart tree top demo
Requires=${MID_UNIT}.service
After=${MID_UNIT}.service

[Service]
Type=simple
ExecStart=$ROOT/test-envd -pid-file $TOP_PID_FILE -ready-message ${TOP_UNIT}-ready -sleep-seconds 300
EOF

printf 'Starting system-mode dependency tree...\n'
"$ROOT/servicectl" start "$TOP_UNIT"
sleep 1

LEAF_PID_BEFORE="$(read_pid "$LEAF_PID_FILE")"
MID_PID_BEFORE="$(read_pid "$MID_PID_FILE")"
TOP_PID_BEFORE="$(read_pid "$TOP_PID_FILE")"

printf 'Restarting leaf and checking dependent restart closure...\n'
"$ROOT/servicectl" restart "$LEAF_UNIT" >"$RESTART_OUTPUT"
sleep 1

assert_contains "$RESTART_OUTPUT" "Stopping dependents: ${TOP_UNIT}, ${MID_UNIT}"
assert_contains "$RESTART_OUTPUT" "Restarting target: ${LEAF_UNIT}"
assert_contains "$RESTART_OUTPUT" "Restoring dependents: ${MID_UNIT}, ${TOP_UNIT}"

LEAF_PID_AFTER="$(read_pid "$LEAF_PID_FILE")"
MID_PID_AFTER="$(read_pid "$MID_PID_FILE")"
TOP_PID_AFTER="$(read_pid "$TOP_PID_FILE")"

[[ "$LEAF_PID_BEFORE" != "$LEAF_PID_AFTER" ]] || { printf 'leaf pid did not change\n' >&2; exit 1; }
[[ "$MID_PID_BEFORE" != "$MID_PID_AFTER" ]] || { printf 'mid pid did not change\n' >&2; exit 1; }
[[ "$TOP_PID_BEFORE" != "$TOP_PID_AFTER" ]] || { printf 'top pid did not change\n' >&2; exit 1; }

"$ROOT/servicectl" is-active "$LEAF_UNIT"
"$ROOT/servicectl" is-active "$MID_UNIT"
"$ROOT/servicectl" is-active "$TOP_UNIT"

printf 'System restart dependency-tree test passed.\n'
