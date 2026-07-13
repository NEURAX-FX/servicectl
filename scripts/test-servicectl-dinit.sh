#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
STATUS_IDLE="$(mktemp /tmp/servicectl-status-idle.XXXXXX)"
STATUS_ACTIVE="$(mktemp /tmp/servicectl-status-active.XXXXXX)"
LOG_OUTPUT="$(mktemp /tmp/servicectl-logs.XXXXXX)"
SOCKET_PATH="/tmp/service-notify-demo.sock"
SYSLOG_OUTPUT="$(mktemp /tmp/servicectl-syslog.XXXXXX)"
JOURNAL_TAG='servicectl[service-notify-demo]'

cleanup() {
  "$ROOT/servicectl" --user stop service-notify-demo >/dev/null 2>&1 || true
  rm -f "$STATUS_IDLE" "$STATUS_ACTIVE" "$LOG_OUTPUT" "$SYSLOG_OUTPUT"
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

printf 'Building servicectl binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-notifyd" ./cmd/sys-notifyd
go build -o "$ROOT/sys-logd" ./cmd/sys-logd
go build -o "$ROOT/notify-echod" ./cmd/notify-echod

printf 'Restarting service-notify-demo through servicectl...\n'
START_TIME="$(date --iso-8601=seconds)"
"$ROOT/servicectl" --user restart service-notify-demo
sleep 1

printf 'Checking idle socketd status...\n'
"$ROOT/servicectl" --user status service-notify-demo --plain --verbose >"$STATUS_IDLE"
assert_contains "$STATUS_IDLE" "service-notify-demo.service - Service notify socket activation demo"
assert_contains "$STATUS_IDLE" "active  disabled - user@"
assert_contains "$STATUS_IDLE" "ORCHESTRATION"
assert_contains "$STATUS_IDLE" "service-notify-demo.service"
assert_contains "$STATUS_IDLE" "manager_pid="

printf 'Triggering socket activation...\n'
RESPONSE="$(printf '' | socat - UNIX-CONNECT:"$SOCKET_PATH")"
if [[ "$RESPONSE" != "hello from notify-echod" ]]; then
  printf 'assertion failed: unexpected socket response: %s\n' "$RESPONSE" >&2
  exit 1
fi
sleep 1

printf 'Checking active backend status...\n'
"$ROOT/servicectl" --user status service-notify-demo --plain --verbose >"$STATUS_ACTIVE"
assert_contains "$STATUS_ACTIVE" "active  disabled - user@"
assert_contains "$STATUS_ACTIVE" "PID "
assert_contains "$STATUS_ACTIVE" "healthy | active | pid "
assert_contains "$STATUS_ACTIVE" "manager_pid="
assert_contains "$STATUS_ACTIVE" "notify-echod accepting connections"

printf 'Checking rsyslog-backed logs...\n'
grep -F "$JOURNAL_TAG" /var/log/messages >"$SYSLOG_OUTPUT"
assert_contains "$SYSLOG_OUTPUT" "activation trigger: incoming traffic"
assert_contains "$SYSLOG_OUTPUT" "notify-echod accepting connections"

printf 'Checking servicectl logs...\n'
"$ROOT/servicectl" --user logs -n 50 service-notify-demo >"$LOG_OUTPUT"
assert_contains "$LOG_OUTPUT" "activation trigger: incoming traffic"
assert_contains "$LOG_OUTPUT" "notify-echod accepting connections"

printf 'Stopping demo service...\n'
"$ROOT/servicectl" --user stop service-notify-demo
sleep 1
if [[ -e "$SOCKET_PATH" ]]; then
  printf 'assertion failed: socket path still exists: %s\n' "$SOCKET_PATH" >&2
  exit 1
fi

printf 'servicectl + dinit integration test passed.\n'
