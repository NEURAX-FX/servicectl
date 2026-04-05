#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
WATCH_ALL="$(mktemp /tmp/sysvision-watch-all.XXXXXX)"
WATCH_FILTERED="$(mktemp /tmp/sysvision-watch-filtered.XXXXXX)"
TEST_ROOT="$(mktemp -d /tmp/sysvision-test-root.XXXXXX)"
SYSTEM_RUNTIME_ROOT="$TEST_ROOT/system"
USER_RUNTIME_ROOT="$TEST_ROOT/user"
SYSTEM_SYSVISION_SOCK="$SYSTEM_RUNTIME_ROOT/sysvision/sysvisiond.sock"
SYSTEM_SYSVISION_INGRESS="$SYSTEM_RUNTIME_ROOT/sysvision/events.sock"
SYSTEM_SERVICECTL_EVENTS="$SYSTEM_RUNTIME_ROOT/servicectl-events.sock"
PIDS=()

start_bg() {
  "$@" &
  PIDS+=("$!")
}

last_pid() {
  printf '%s\n' "${PIDS[${#PIDS[@]}-1]}"
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
  rm -f "$WATCH_ALL" "$WATCH_FILTERED"
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

assert_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Fq -- "$pattern" "$file"; then
    printf 'assertion failed: %s missing %s\n' "$file" "$pattern" >&2
    printf -- '--- %s ---\n' "$file" >&2
    cat "$file" >&2 || true
    printf -- '--- /tmp/sysvisiond.log ---\n' >&2
    cat /tmp/sysvisiond.log >&2 || true
    exit 1
  fi
}

printf 'Building sysvisiond test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sysvisiond" ./cmd/sysvisiond

printf 'Starting servicectl API and sysvisiond...\n'
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" serve-api >/tmp/servicectl-api.log 2>&1
start_bg env SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/sysvisiond" >/tmp/sysvisiond.log 2>&1
sleep 2

printf 'Checking sysvisiond query proxy...\n'
QUERY_OUTPUT="$(curl --silent --unix-socket "$SYSTEM_SYSVISION_SOCK" http://unix/v1/query/unit/munge.service)"
if [[ "$QUERY_OUTPUT" != *'"name": "munge"'* ]]; then
  printf 'assertion failed: sysvisiond query output missing munge unit\n' >&2
  exit 1
fi

printf 'Waiting for sysvisiond event bridge...\n'
for _ in 1 2 3 4 5 6 7 8 9 10; do
  META_OUTPUT="$(curl --silent --unix-socket "$SYSTEM_SYSVISION_SOCK" http://unix/v1/meta || true)"
  if [[ "$META_OUTPUT" == *'"system_servicectl_events_connected": true'* && -S "$SYSTEM_SERVICECTL_EVENTS" ]]; then
    break
  fi
  sleep 1
done
if [[ "$META_OUTPUT" != *'"system_servicectl_events_connected": true'* || ! -S "$SYSTEM_SERVICECTL_EVENTS" ]]; then
  printf 'assertion failed: sysvisiond event bridge never became ready\n' >&2
  printf '%s\n' "$META_OUTPUT" >&2
  exit 1
fi

printf 'Checking sysvisiond watch filters...\n'
start_bg timeout 10 curl --silent --no-buffer --unix-socket "$SYSTEM_SYSVISION_SOCK" "http://unix/v1/watch?mode=system&source=servicectl" >"$WATCH_ALL"
PID_ALL="$(last_pid)"
for _ in 1 2 3 4 5 6; do
  sleep 1
  printf '%s' '{"source":"servicectl","kind":"unit.command","mode":"system","unit":"munge.service","timestamp":"2026-04-04T00:00:00Z","payload":{"action":"reload","result":"ok"}}' | socat - UNIX-SENDTO:"$SYSTEM_SERVICECTL_EVENTS"
  if [[ -s "$WATCH_ALL" ]]; then
    break
  fi
done

wait "$PID_ALL" || true

start_bg timeout 10 curl --silent --no-buffer --unix-socket "$SYSTEM_SYSVISION_SOCK" "http://unix/v1/watch?mode=system&source=servicectl&kind=unit.command&unit=munge.service" >"$WATCH_FILTERED"
PID_FILTERED="$(last_pid)"
for _ in 1 2 3 4 5 6; do
  sleep 1
  printf '%s' '{"source":"servicectl","kind":"unit.command","mode":"system","unit":"munge.service","timestamp":"2026-04-04T00:00:00Z","payload":{"action":"reload","result":"ok"}}' | socat - UNIX-SENDTO:"$SYSTEM_SERVICECTL_EVENTS"
  if [[ -s "$WATCH_FILTERED" ]]; then
    break
  fi
done

wait "$PID_FILTERED" || true

assert_contains "$WATCH_ALL" '"source":"servicectl"'
assert_contains "$WATCH_ALL" '"kind":"unit.command"'
assert_contains "$WATCH_ALL" '"mode":"system"'
assert_contains "$WATCH_ALL" '"unit":"munge.service"'
assert_contains "$WATCH_FILTERED" '"source":"servicectl"'
assert_contains "$WATCH_FILTERED" '"kind":"unit.command"'
assert_contains "$WATCH_FILTERED" '"mode":"system"'
assert_contains "$WATCH_FILTERED" '"unit":"munge.service"'
assert_contains "$WATCH_FILTERED" '"action":"reload"'

printf 'sysvisiond bus test passed.\n'
