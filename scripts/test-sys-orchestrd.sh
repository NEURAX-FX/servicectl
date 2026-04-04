#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
UNIT_NAME="sys-orchestrd-demo"
UNIT_PATH="/etc/systemd/system/${UNIT_NAME}.service"
USER_UNIT_NAME="sys-orchestrd-user-demo"
USER_UNIT_PATH="$HOME/.config/systemd/user/${USER_UNIT_NAME}.service"
USER_RUNTIME="/tmp/runtime-0"
FAKE_CTL="$(mktemp /tmp/fake-servicectl.XXXXXX)"
CALL_LOG="$(mktemp /tmp/sys-orchestrd-calls.XXXXXX)"
STATE_FILE="$(mktemp /tmp/sys-orchestrd-state.XXXXXX)"
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
  rm -f "$UNIT_PATH" "$USER_UNIT_PATH" "$FAKE_CTL" "$CALL_LOG" "$STATE_FILE"
}
trap cleanup EXIT

assert_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Fq -- "$pattern" "$file"; then
    printf 'assertion failed: %s missing %s\n' "$file" "$pattern" >&2
    exit 1
  fi
}

printf 'Building sys-orchestrd test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sysvisiond" ./cmd/sysvisiond
go build -o "$ROOT/sys-orchestrd" ./cmd/sys-orchestrd

cat >"$UNIT_PATH" <<'EOF'
[Unit]
Description=sys-orchestrd test unit

[Service]
Type=simple
ExecStart=/bin/sleep 30
EOF

mkdir -p "$(dirname "$USER_UNIT_PATH")" "$USER_RUNTIME"
cat >"$USER_UNIT_PATH" <<'EOF'
[Unit]
Description=sys-orchestrd user test unit

[Service]
Type=simple
ExecStart=/bin/sleep 30
EOF

cat >"$FAKE_CTL" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$CALL_LOG"
exit 0
EOF
chmod +x "$FAKE_CTL"

printf 'Starting servicectl API and sysvisiond...\n'
start_bg "$ROOT/servicectl" serve-api >/tmp/servicectl-api.log 2>&1
start_bg "$ROOT/sysvisiond" >/tmp/sysvisiond.log 2>&1
start_bg env XDG_RUNTIME_DIR="$USER_RUNTIME" "$ROOT/servicectl" --user serve-api >/tmp/servicectl-user-api.log 2>&1
start_bg env XDG_RUNTIME_DIR="$USER_RUNTIME" "$ROOT/sysvisiond" --user >/tmp/sysvisiond-user.log 2>&1
sleep 2

printf 'Checking sys-orchestrd startup and stop path...\n'
start_bg env SERVICECTL_BIN="$FAKE_CTL" SYS_ORCHESTRD_STATE_FILE="$STATE_FILE" "$ROOT/sys-orchestrd" --unit "${UNIT_NAME}.service" >/tmp/sys-orchestrd.log 2>&1
ORCH_PID="$(last_pid)"
sleep 2
assert_contains "$CALL_LOG" "start ${UNIT_NAME}.service"
kill -TERM "$ORCH_PID"
wait "$ORCH_PID"
assert_contains "$CALL_LOG" "stop ${UNIT_NAME}.service"
assert_contains "$STATE_FILE" "state=stopping"

printf 'Checking sys-orchestrd failure propagation...\n'
: >"$CALL_LOG"
start_bg env SERVICECTL_BIN="$FAKE_CTL" SYS_ORCHESTRD_STATE_FILE="$STATE_FILE" "$ROOT/sys-orchestrd" --unit "${UNIT_NAME}.service" >/tmp/sys-orchestrd.log 2>&1
ORCH_PID="$(last_pid)"
sleep 2
printf '%s' '{"source":"sys-notifyd","kind":"unit.runtime","unit":"sys-orchestrd-demo.service","timestamp":"2026-04-04T00:00:00Z","payload":{"failure":"boom"}}' | socat - UNIX-SENDTO:/run/servicectl/sysvision/events.sock
set +e
wait "$ORCH_PID"
STATUS=$?
set -e
if [[ "$STATUS" -eq 0 ]]; then
  printf 'assertion failed: sys-orchestrd exited successfully after failure event\n' >&2
  exit 1
fi
assert_contains "$STATE_FILE" "state=failed"

printf 'Checking user-mode sys-orchestrd startup and stop path...\n'
: >"$CALL_LOG"
start_bg env SERVICECTL_BIN="$FAKE_CTL" SYS_ORCHESTRD_STATE_FILE="$STATE_FILE" XDG_RUNTIME_DIR="$USER_RUNTIME" "$ROOT/sys-orchestrd" --user --unit "${USER_UNIT_NAME}.service" >/tmp/sys-orchestrd-user.log 2>&1
ORCH_PID="$(last_pid)"
sleep 2
assert_contains "$CALL_LOG" "--user start ${USER_UNIT_NAME}.service"
if kill -0 "$ORCH_PID" >/dev/null 2>&1; then
  kill -TERM "$ORCH_PID"
fi
wait "$ORCH_PID" || true

printf 'sys-orchestrd integration test passed.\n'
