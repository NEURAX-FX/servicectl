#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
TEST_ROOT="$(mktemp -d /tmp/user-group-targets.XXXXXX)"
HOME_ROOT="$TEST_ROOT/home"
USER_CONFIG_ROOT="$HOME_ROOT/.config/servicectl"
GROUP_DIR="$USER_CONFIG_ROOT/groups.d"
USER_SYSTEMD_DIR="$HOME_ROOT/.config/systemd/user"
SYSTEM_RUNTIME_ROOT="$TEST_ROOT/system"
USER_RUNTIME_ROOT="$TEST_ROOT/user"
PROPERTY_SOCK="$SYSTEM_RUNTIME_ROOT/sys-propertyd.sock"
NO_TARGET_GROUP="only-group"
HAS_TARGET_GROUP="with-target"
NO_TARGET_FILE="$GROUP_DIR/only-group.conf"
HAS_TARGET_FILE="$GROUP_DIR/with-target.conf"
SYSTEMD_TARGET_PATH="$USER_SYSTEMD_DIR/real-target.target"
STATUS_OUTPUT="$(mktemp /tmp/user-group-status.XXXXXX)"
ENABLED_OUTPUT="$(mktemp /tmp/user-group-enabled.XXXXXX)"
PIDS=()

start_bg() {
  "$@" &
  PIDS+=("$!")
}

wait_for_socket() {
  local path="$1"
  local label="$2"
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [[ -S "$path" ]]; then
      return 0
    fi
    sleep 1
  done
  printf 'assertion failed: %s socket never appeared at %s\n' "$label" "$path" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Fq -- "$pattern" "$file"; then
    printf 'assertion failed: %s missing %s\n' "$file" "$pattern" >&2
    exit 1
  fi
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
  rm -f "$STATUS_OUTPUT" "$ENABLED_OUTPUT"
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

printf 'Building user group target test binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-propertyd" ./cmd/sys-propertyd

mkdir -p "$GROUP_DIR" "$USER_SYSTEMD_DIR"

cat >"$NO_TARGET_FILE" <<EOF
[Group]
Name=${NO_TARGET_GROUP}
Units=alpha.service
EOF

cat >"$HAS_TARGET_FILE" <<EOF
[Group]
Name=${HAS_TARGET_GROUP}
Units=beta.service
Targets=with-target.target
EOF

cat >"$SYSTEMD_TARGET_PATH" <<EOF
[Unit]
Description=Real target alias
Wants=gamma.service
EOF

printf 'Starting isolated property daemon...\n'
start_bg env HOME="$HOME_ROOT" SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/sys-propertyd" >/tmp/user-group-targets-propertyd.log 2>&1
wait_for_socket "$PROPERTY_SOCK" 'sys-propertyd'

printf 'Checking explicit group status still works without Targets=...\n'
HOME="$HOME_ROOT" SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --user --group status "$NO_TARGET_GROUP" >"$STATUS_OUTPUT"
assert_contains "$STATUS_OUTPUT" "group:${NO_TARGET_GROUP} disabled"
assert_contains "$STATUS_OUTPUT" 'Units: alpha.service'

printf 'Checking foo.target does not resolve from bare group name...\n'
set +e
HOME="$HOME_ROOT" SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --user is-enabled "${NO_TARGET_GROUP}.target" >"$ENABLED_OUTPUT" 2>&1
STATUS=$?
set -e
if [[ "$STATUS" -eq 0 ]]; then
  printf 'assertion failed: expected %s.target to fail without Targets=\n' "$NO_TARGET_GROUP" >&2
  exit 1
fi
assert_contains "$ENABLED_OUTPUT" 'property resolve returned 404 Not Found'

printf 'Checking explicit Targets= alias resolves...\n'
HOME="$HOME_ROOT" SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --user --group status "$HAS_TARGET_GROUP" >"$STATUS_OUTPUT"
assert_contains "$STATUS_OUTPUT" 'Targets: with-target.target'

TARGET_ENABLED="$(HOME="$HOME_ROOT" SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --user is-enabled with-target.target || true)"
if [[ "$TARGET_ENABLED" != 'disabled' ]]; then
  printf 'assertion failed: expected disabled before enable, got %s\n' "$TARGET_ENABLED" >&2
  exit 1
fi

printf 'Checking real .target file also resolves...\n'
REAL_TARGET_ENABLED="$(HOME="$HOME_ROOT" SERVICECTL_SYSTEM_RUNTIME_ROOT="$SYSTEM_RUNTIME_ROOT" SERVICECTL_USER_RUNTIME_ROOT="$USER_RUNTIME_ROOT" "$ROOT/servicectl" --user is-enabled real-target.target || true)"
if [[ "$REAL_TARGET_ENABLED" != 'disabled' ]]; then
  printf 'assertion failed: expected disabled for real target before enable, got %s\n' "$REAL_TARGET_ENABLED" >&2
  exit 1
fi

printf 'user group target integration test passed.\n'
