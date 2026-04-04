#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
BIN="$ROOT/sys-notifyd"
SLEEPER="$ROOT/notify-sleeper"
ECHOD="$ROOT/notify-echod"
WORKDIR="$(mktemp -d /tmp/sys-notifyd-test.XXXXXX)"

cleanup() {
  pkill -f "$BIN -service notify-only-test" >/dev/null 2>&1 || true
  pkill -f "$BIN -service socket-notify-test" >/dev/null 2>&1 || true
  pkill -f "$BIN -service socket-restart-test" >/dev/null 2>&1 || true
  pkill -f "$BIN -service extend-test" >/dev/null 2>&1 || true
  pkill -f "$BIN -service watchdog-test" >/dev/null 2>&1 || true
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

printf 'Building test binaries...\n'
go build -o "$BIN" ./cmd/sys-notifyd
go build -o "$ROOT/notify-sleeper" ./cmd/notify-sleeper
go build -o "$ROOT/notify-echod" ./cmd/notify-echod

printf 'Test 1: notify-only lifecycle...\n'
NOTIFY_LOG="$WORKDIR/notify-only.log"
STOP_MARKER="$WORKDIR/notify-only.stopped"
"$BIN" -service notify-only-test -service-type notify -command "$SLEEPER" -stop-command "/bin/sh -c 'touch \"$STOP_MARKER\"; kill -TERM \"\$MAINPID\"'" -ready-timeout 5s -stop-timeout 5s -notify -notify-path "$WORKDIR/notify-only.sock" -start-now >"$NOTIFY_LOG" 2>&1 &
NOTIFY_PID=$!
sleep 1
assert_contains "$NOTIFY_LOG" "READY=1"
assert_contains "$NOTIFY_LOG" "status: notify-sleeper running"
assert_contains "$NOTIFY_LOG" "mainpid:"
kill -TERM "$NOTIFY_PID"
wait "$NOTIFY_PID"
assert_contains "$NOTIFY_LOG" "backend requested stop"
assert_contains "$NOTIFY_LOG" "status: notify-sleeper stopping"
if [[ ! -e "$STOP_MARKER" ]]; then
  printf 'assertion failed: stop marker missing: %s\n' "$STOP_MARKER" >&2
  exit 1
fi

printf 'Test 2: socket + notify activation...\n'
SOCKET_PATH="$WORKDIR/echo.sock"
SOCKET_LOG="$WORKDIR/socket-notify.log"
"$BIN" -service socket-notify-test -service-type notify -command "$ECHOD" -ready-timeout 5s -notify -notify-path "$WORKDIR/socket-notify.sock" -listen "unix:$SOCKET_PATH" -fdname api -socket-mode 0666 >"$SOCKET_LOG" 2>&1 &
SOCKET_PID=$!
sleep 1
assert_contains "$SOCKET_LOG" "READY=1"
assert_contains "$SOCKET_LOG" "ready: service=socket-notify-test sockets=1"
if grep -Fq "notify-echod accepting connections" "$SOCKET_LOG"; then
  printf 'assertion failed: backend started before traffic\n' >&2
  exit 1
fi
RESPONSE="$(printf '' | socat - UNIX-CONNECT:"$SOCKET_PATH")"
if [[ "$RESPONSE" != "hello from notify-echod fdnames=api" ]]; then
  printf 'assertion failed: unexpected socket response: %s\n' "$RESPONSE" >&2
  exit 1
fi
assert_contains "$SOCKET_LOG" "activation trigger: incoming traffic"
assert_contains "$SOCKET_LOG" "status: notify-echod accepting connections"
assert_contains "$SOCKET_LOG" "mainpid:"
kill -TERM "$SOCKET_PID"
wait "$SOCKET_PID"
assert_contains "$SOCKET_LOG" "backend requested stop"
assert_contains "$SOCKET_LOG" "status: notify-echod shutting down"
if [[ -e "$SOCKET_PATH" ]]; then
  printf 'assertion failed: socket path still exists: %s\n' "$SOCKET_PATH" >&2
  exit 1
fi

printf 'Test 3: socket backend restart on next traffic...\n'
RESTART_PATH="$WORKDIR/restart.sock"
RESTART_LOG="$WORKDIR/socket-restart.log"
env NOTIFY_ECHOD_EXIT_AFTER_ACCEPT=1 \
  "$BIN" -service socket-restart-test -service-type notify -command "env NOTIFY_ECHOD_EXIT_AFTER_ACCEPT=1 $ECHOD" -ready-timeout 5s -notify -notify-path "$WORKDIR/socket-restart.sock" -listen "unix:$RESTART_PATH" -fdname api -socket-mode 0666 >"$RESTART_LOG" 2>&1 &
RESTART_PID=$!
sleep 1
FIRST_RESPONSE="$(printf '' | socat - UNIX-CONNECT:"$RESTART_PATH")"
SECOND_RESPONSE="$(printf '' | socat - UNIX-CONNECT:"$RESTART_PATH")"
if [[ "$FIRST_RESPONSE" != "hello from notify-echod fdnames=api" || "$SECOND_RESPONSE" != "hello from notify-echod fdnames=api" ]]; then
  printf 'assertion failed: restart test responses unexpected\n' >&2
  exit 1
fi
assert_contains "$RESTART_LOG" "backend exited, sockets remain armed"
START_COUNT="$(rg -c "activation trigger: incoming traffic" "$RESTART_LOG")"
if [[ "$START_COUNT" -lt 2 ]]; then
  printf 'assertion failed: expected two activation triggers, got %s\n' "$START_COUNT" >&2
  exit 1
fi
kill -TERM "$RESTART_PID"
wait "$RESTART_PID"

printf 'Test 4: notify startup timeout extension...\n'
EXTEND_LOG="$WORKDIR/extend.log"
env NOTIFY_EXTEND_START_USEC=1200000 NOTIFY_EXTEND_INTERVAL_USEC=200000 \
  "$BIN" -service extend-test -service-type notify -command "env NOTIFY_EXTEND_START_USEC=1200000 NOTIFY_EXTEND_INTERVAL_USEC=200000 $SLEEPER" -ready-timeout 500ms -notify -notify-path "$WORKDIR/extend.sock" -start-now >"$EXTEND_LOG" 2>&1 &
EXTEND_PID=$!
sleep 2
assert_contains "$EXTEND_LOG" "extended start timeout"
assert_contains "$EXTEND_LOG" "READY=1"
kill -TERM "$EXTEND_PID"
wait "$EXTEND_PID"

printf 'Test 5: notify watchdog keepalive...\n'
WATCHDOG_LOG="$WORKDIR/watchdog.log"
"$BIN" -service watchdog-test -service-type notify -command "env NOTIFY_WATCHDOG_USEC=500000 $SLEEPER" -ready-timeout 5s -notify -notify-path "$WORKDIR/watchdog.sock" -start-now >"$WATCHDOG_LOG" 2>&1 &
WATCHDOG_PID=$!
sleep 2
assert_contains "$WATCHDOG_LOG" "watchdog interval set to 500ms"
assert_contains "$WATCHDOG_LOG" "READY=1"
kill -TERM "$WATCHDOG_PID"
wait "$WATCHDOG_PID"

printf 'All sys-notifyd tests passed. Logs kept in %s until script exit.\n' "$WORKDIR"
