#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
WORKDIR="$(mktemp -d /tmp/servicectl-modes-env.XXXXXX)"

USER_UNIT_NAME="user-mode-env-demo"
SYSTEM_UNIT_NAME="system-mode-env-demo"
USER_UNIT_PATH="/root/.config/systemd/user/${USER_UNIT_NAME}.service"
SYSTEM_UNIT_PATH="/etc/systemd/system/${SYSTEM_UNIT_NAME}.service"

USER_ENV_FILE="$WORKDIR/user.env"
SYSTEM_ENV_FILE="$WORKDIR/system.env"
USER_DUMP_FILE="$WORKDIR/user-runtime.env"
SYSTEM_DUMP_FILE="$WORKDIR/system-runtime.env"

USER_SHOW_FULL="$WORKDIR/user-show-full.txt"
USER_SHOW_MISMATCH="$WORKDIR/user-show-mismatch.txt"
SYSTEM_SHOW="$WORKDIR/system-show.txt"
USER_LOGS="$WORKDIR/user-logs.txt"
SYSTEM_LOGS="$WORKDIR/system-logs.txt"
MISSING_RUNTIME_ERR="$WORKDIR/missing-runtime.err"

USER_RUNTIME_BASE="$WORKDIR/user-runtime"
USER_STATE_BASE="$WORKDIR/user-state"
USER_CACHE_BASE="$WORKDIR/user-cache"
USER_RUNTIME_DIR="$USER_RUNTIME_BASE/${USER_UNIT_NAME}-runtime"
USER_STATE_DIR="$USER_STATE_BASE/${USER_UNIT_NAME}-state"
USER_LOGS_DIR="$USER_STATE_BASE/log/${USER_UNIT_NAME}-logs"

SYSTEM_RUNTIME_DIR="/run/${SYSTEM_UNIT_NAME}-runtime"
SYSTEM_STATE_DIR="/var/lib/${SYSTEM_UNIT_NAME}-state"
SYSTEM_LOGS_DIR="/var/log/${SYSTEM_UNIT_NAME}-logs"

cleanup() {
  XDG_RUNTIME_DIR="$USER_RUNTIME_BASE" "$ROOT/servicectl" --user stop "$USER_UNIT_NAME" >/dev/null 2>&1 || true
  "$ROOT/servicectl" stop "$SYSTEM_UNIT_NAME" >/dev/null 2>&1 || true
  dinitctl --user stop "${USER_UNIT_NAME}-log" >/dev/null 2>&1 || true
  dinitctl stop "${SYSTEM_UNIT_NAME}-log" >/dev/null 2>&1 || true
  rm -f "$USER_UNIT_PATH" "$SYSTEM_UNIT_PATH"
  rm -f "$USER_ENV_FILE" "$SYSTEM_ENV_FILE"
  rm -f "$USER_DUMP_FILE" "$SYSTEM_DUMP_FILE"
  rm -f "${USER_RUNTIME_BASE}/dinit.d/generated/${USER_UNIT_NAME}" "/root/.config/dinit.d/${USER_UNIT_NAME}"
  rm -f "/run/dinit.d/generated/${SYSTEM_UNIT_NAME}" "/etc/dinit.d/${SYSTEM_UNIT_NAME}"
  rm -rf "$USER_RUNTIME_BASE" "$USER_STATE_BASE" "$USER_CACHE_BASE"
  rm -rf "$SYSTEM_RUNTIME_DIR" "$SYSTEM_STATE_DIR" "$SYSTEM_LOGS_DIR"
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

assert_not_contains() {
  local file="$1"
  local pattern="$2"
  if grep -Fq "$pattern" "$file"; then
    printf 'assertion failed: %s unexpectedly contains %s\n' "$file" "$pattern" >&2
    exit 1
  fi
}

printf 'Building servicectl binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-logd" ./cmd/sys-logd
go build -o "$ROOT/test-envd" ./cmd/test-envd

mkdir -p "$USER_RUNTIME_BASE" "$USER_STATE_BASE" "$USER_CACHE_BASE"

cat >"$USER_ENV_FILE" <<'EOF'
FILE_ONLY_USER=from-user-env-file
EOF

cat >"$SYSTEM_ENV_FILE" <<'EOF'
FILE_ONLY_SYSTEM=from-system-env-file
EOF

cat >"$USER_UNIT_PATH" <<EOF
[Unit]
Description=User mode environment demo

[Service]
Type=simple
Environment=INLINE_USER=from-inline-user
Environment=SHARED_VALUE=user-inline
EnvironmentFile=$USER_ENV_FILE
RuntimeDirectory=${USER_UNIT_NAME}-runtime
StateDirectory=${USER_UNIT_NAME}-state
LogsDirectory=${USER_UNIT_NAME}-logs
ExecStart=$ROOT/test-envd -dump-file $USER_DUMP_FILE -ready-message user-env-ready -sleep-seconds 300
EOF

cat >"$SYSTEM_UNIT_PATH" <<EOF
[Unit]
Description=System mode environment demo

[Service]
Type=simple
Environment=INLINE_SYSTEM=from-inline-system
Environment=SHARED_VALUE=system-inline
EnvironmentFile=$SYSTEM_ENV_FILE
RuntimeDirectory=${SYSTEM_UNIT_NAME}-runtime
StateDirectory=${SYSTEM_UNIT_NAME}-state
LogsDirectory=${SYSTEM_UNIT_NAME}-logs
ExecStart=$ROOT/test-envd -dump-file $SYSTEM_DUMP_FILE -ready-message system-env-ready -sleep-seconds 300
EOF

printf 'Starting system-mode environment demo...\n'
"$ROOT/servicectl" start "$SYSTEM_UNIT_NAME"
sleep 1

printf 'Checking system-mode show and runtime environment...\n'
"$ROOT/servicectl" show "$SYSTEM_UNIT_NAME" >"$SYSTEM_SHOW"
assert_contains "$SYSTEM_SHOW" "Mode           system"
assert_contains "$SYSTEM_SHOW" "Source         $SYSTEM_UNIT_PATH"
assert_not_contains "$SYSTEM_SHOW" "Caller HOME"
assert_not_contains "$SYSTEM_SHOW" "Env Warning"

assert_contains "$SYSTEM_DUMP_FILE" "FILE_ONLY_SYSTEM=from-system-env-file"
assert_contains "$SYSTEM_DUMP_FILE" "INLINE_SYSTEM=from-inline-system"
assert_contains "$SYSTEM_DUMP_FILE" "SHARED_VALUE=system-inline"
[[ -d "$SYSTEM_RUNTIME_DIR" ]] || { printf 'missing system runtime dir %s\n' "$SYSTEM_RUNTIME_DIR" >&2; exit 1; }
[[ -d "$SYSTEM_STATE_DIR" ]] || { printf 'missing system state dir %s\n' "$SYSTEM_STATE_DIR" >&2; exit 1; }
[[ -d "$SYSTEM_LOGS_DIR" ]] || { printf 'missing system logs dir %s\n' "$SYSTEM_LOGS_DIR" >&2; exit 1; }

"$ROOT/servicectl" logs -n 50 "$SYSTEM_UNIT_NAME" >"$SYSTEM_LOGS"
assert_contains "$SYSTEM_LOGS" "system-env-ready"

printf 'Starting user-mode environment demo with explicit session environment...\n'
HOME=/root \
XDG_RUNTIME_DIR="$USER_RUNTIME_BASE" \
XDG_STATE_HOME="$USER_STATE_BASE" \
XDG_CACHE_HOME="$USER_CACHE_BASE" \
DBUS_SESSION_BUS_ADDRESS="unix:path=$WORKDIR/fake-user-bus" \
"$ROOT/servicectl" --user start "$USER_UNIT_NAME"
sleep 1

printf 'Checking user-mode show, diagnostics, and runtime environment...\n'
HOME=/root \
XDG_RUNTIME_DIR="$USER_RUNTIME_BASE" \
XDG_STATE_HOME="$USER_STATE_BASE" \
XDG_CACHE_HOME="$USER_CACHE_BASE" \
DBUS_SESSION_BUS_ADDRESS="unix:path=$WORKDIR/fake-user-bus" \
"$ROOT/servicectl" --user show "$USER_UNIT_NAME" >"$USER_SHOW_FULL"

assert_contains "$USER_SHOW_FULL" "Mode           user"
assert_contains "$USER_SHOW_FULL" "Source         $USER_UNIT_PATH"
assert_contains "$USER_SHOW_FULL" "Env XDG RT     $USER_RUNTIME_BASE"
assert_contains "$USER_SHOW_FULL" "Env XDG ST     $USER_STATE_BASE"
assert_contains "$USER_SHOW_FULL" "Env XDG CA     $USER_CACHE_BASE"
assert_contains "$USER_SHOW_FULL" "Env DBUS       unix:path=$WORKDIR/fake-user-bus"
assert_contains "$USER_SHOW_FULL" "Caller XDG RT  $USER_RUNTIME_BASE"
assert_contains "$USER_SHOW_FULL" "Caller XDG ST  $USER_STATE_BASE"
assert_contains "$USER_SHOW_FULL" "Caller XDG CA  $USER_CACHE_BASE"
assert_not_contains "$USER_SHOW_FULL" "Env Warning"

assert_contains "$USER_DUMP_FILE" "FILE_ONLY_USER=from-user-env-file"
assert_contains "$USER_DUMP_FILE" "INLINE_USER=from-inline-user"
assert_contains "$USER_DUMP_FILE" "SHARED_VALUE=user-inline"
assert_contains "$USER_DUMP_FILE" "XDG_RUNTIME_DIR=$USER_RUNTIME_BASE"
assert_contains "$USER_DUMP_FILE" "XDG_STATE_HOME=$USER_STATE_BASE"
assert_contains "$USER_DUMP_FILE" "XDG_CACHE_HOME=$USER_CACHE_BASE"
assert_contains "$USER_DUMP_FILE" "DBUS_SESSION_BUS_ADDRESS=unix:path=$WORKDIR/fake-user-bus"
[[ -d "$USER_RUNTIME_DIR" ]] || { printf 'missing user runtime dir %s\n' "$USER_RUNTIME_DIR" >&2; exit 1; }
[[ -d "$USER_STATE_DIR" ]] || { printf 'missing user state dir %s\n' "$USER_STATE_DIR" >&2; exit 1; }
[[ -d "$USER_LOGS_DIR" ]] || { printf 'missing user logs dir %s\n' "$USER_LOGS_DIR" >&2; exit 1; }

HOME=/root XDG_RUNTIME_DIR="$USER_RUNTIME_BASE" "$ROOT/servicectl" --user logs -n 50 "$USER_UNIT_NAME" >"$USER_LOGS"
assert_contains "$USER_LOGS" "user-env-ready"

printf 'Checking user-mode mismatch diagnostics...\n'
HOME=/root XDG_RUNTIME_DIR="$USER_RUNTIME_BASE" "$ROOT/servicectl" --user show "$USER_UNIT_NAME" >"$USER_SHOW_MISMATCH"
assert_contains "$USER_SHOW_MISMATCH" "Caller XDG ST  -"
assert_contains "$USER_SHOW_MISMATCH" "Caller XDG CA  -"
assert_contains "$USER_SHOW_MISMATCH" "Env Warning    calling environment differs from managed service environment"

printf 'Checking missing XDG_RUNTIME_DIR failure path...\n'
if env -u XDG_RUNTIME_DIR HOME=/root "$ROOT/servicectl" --user show "$USER_UNIT_NAME" >"$MISSING_RUNTIME_ERR" 2>&1; then
  printf 'assertion failed: missing XDG_RUNTIME_DIR unexpectedly succeeded\n' >&2
  exit 1
fi
assert_contains "$MISSING_RUNTIME_ERR" "user mode requires XDG_RUNTIME_DIR to be set in the calling environment"

printf 'System/user environment test passed. Logs kept in %s until script exit.\n' "$WORKDIR"
