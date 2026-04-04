#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
WATCH_ALL="$(mktemp /tmp/sysvision-watch-all.XXXXXX)"
WATCH_FILTERED="$(mktemp /tmp/sysvision-watch-filtered.XXXXXX)"

cleanup() {
  pkill -f "servicectl serve-api" >/dev/null 2>&1 || true
  pkill -f "$ROOT/sysvisiond" >/dev/null 2>&1 || true
  pkill -f "$ROOT/sys-orchestrd" >/dev/null 2>&1 || true
  rm -f "$WATCH_ALL" "$WATCH_FILTERED"
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
"$ROOT/servicectl" serve-api >/tmp/servicectl-api.log 2>&1 &
"$ROOT/sysvisiond" >/tmp/sysvisiond.log 2>&1 &
sleep 2

printf 'Checking sysvisiond query proxy...\n'
QUERY_OUTPUT="$(curl --silent --unix-socket /run/servicectl/sysvision/sysvisiond.sock http://unix/v1/query/unit/munge.service)"
if [[ "$QUERY_OUTPUT" != *'"name": "munge"'* ]]; then
  printf 'assertion failed: sysvisiond query output missing munge unit\n' >&2
  exit 1
fi

printf 'Waiting for sysvisiond event bridge...\n'
for _ in 1 2 3 4 5 6 7 8 9 10; do
  META_OUTPUT="$(curl --silent --unix-socket /run/servicectl/sysvision/sysvisiond.sock http://unix/v1/meta || true)"
  if [[ "$META_OUTPUT" == *'"servicectl_events_connected": true'* && -S /run/servicectl/servicectl-events.sock ]]; then
    break
  fi
  sleep 1
done
if [[ "$META_OUTPUT" != *'"servicectl_events_connected": true'* || ! -S /run/servicectl/servicectl-events.sock ]]; then
  printf 'assertion failed: sysvisiond event bridge never became ready\n' >&2
  printf '%s\n' "$META_OUTPUT" >&2
  exit 1
fi

printf 'Checking sysvisiond watch filters...\n'
timeout 10 curl --silent --no-buffer --unix-socket /run/servicectl/sysvision/sysvisiond.sock "http://unix/v1/watch?source=servicectl" >"$WATCH_ALL" &
PID_ALL=$!
for _ in 1 2 3 4 5 6; do
  sleep 1
  printf '%s' '{"source":"servicectl","kind":"unit.command","unit":"munge.service","timestamp":"2026-04-04T00:00:00Z","payload":{"action":"reload","result":"ok"}}' | socat - UNIX-SENDTO:/run/servicectl/servicectl-events.sock
  if [[ -s "$WATCH_ALL" ]]; then
    break
  fi
done

wait "$PID_ALL" || true

timeout 10 curl --silent --no-buffer --unix-socket /run/servicectl/sysvision/sysvisiond.sock "http://unix/v1/watch?source=servicectl&kind=unit.command&unit=munge.service" >"$WATCH_FILTERED" &
PID_FILTERED=$!
for _ in 1 2 3 4 5 6; do
  sleep 1
  printf '%s' '{"source":"servicectl","kind":"unit.command","unit":"munge.service","timestamp":"2026-04-04T00:00:00Z","payload":{"action":"reload","result":"ok"}}' | socat - UNIX-SENDTO:/run/servicectl/servicectl-events.sock
  if [[ -s "$WATCH_FILTERED" ]]; then
    break
  fi
done

wait "$PID_FILTERED" || true

assert_contains "$WATCH_ALL" '"source":"servicectl"'
assert_contains "$WATCH_ALL" '"kind":"unit.command"'
assert_contains "$WATCH_ALL" '"unit":"munge.service"'
assert_contains "$WATCH_FILTERED" '"source":"servicectl"'
assert_contains "$WATCH_FILTERED" '"kind":"unit.command"'
assert_contains "$WATCH_FILTERED" '"unit":"munge.service"'
assert_contains "$WATCH_FILTERED" '"action":"reload"'

printf 'sysvisiond bus test passed.\n'
