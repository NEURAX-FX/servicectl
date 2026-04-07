#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
GROUP_DIR="/etc/servicectl/groups.d"
TARGET_DIR="/etc/servicectl/targets.d"
GROUP_FILE="$GROUP_DIR/test-audio.conf"
TARGET_FILE="$TARGET_DIR/test-audio.conf"
UNIT_NAME="target-audio-demo"
UNIT_PATH="/etc/systemd/system/${UNIT_NAME}.service"
SHOW_OUTPUT="$(mktemp /tmp/property-target-show.XXXXXX)"
GROUP_OUTPUT="$(mktemp /tmp/property-target-group.XXXXXX)"

cleanup() {
  "$ROOT/servicectl" disable group:test-audio >/dev/null 2>&1 || true
  "$ROOT/servicectl" stop "$UNIT_NAME" >/dev/null 2>&1 || true
  dinitctl stop "${UNIT_NAME}-log" >/dev/null 2>&1 || true
  rm -f "$GROUP_FILE" "$TARGET_FILE" "$UNIT_PATH"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}" "/etc/dinit.d/${UNIT_NAME}"
  rm -f "/run/dinit.d/generated/${UNIT_NAME}-log" "/etc/dinit.d/${UNIT_NAME}-log"
  rm -f "$SHOW_OUTPUT" "$GROUP_OUTPUT"
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

printf 'Building property target binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-propertyd" ./cmd/sys-propertyd
go build -o "$ROOT/sysvisiond" ./cmd/sysvisiond

mkdir -p "$GROUP_DIR" "$TARGET_DIR"

cat >"$GROUP_FILE" <<EOF
[Group]
Name=test-audio
Units=${UNIT_NAME}.service
Targets=pipewire.target
EOF

cat >"$TARGET_FILE" <<EOF
[Target]
Name=pipewire.target
Group=test-audio
EOF

cat >"$UNIT_PATH" <<EOF
[Unit]
Description=Target audio demo

[Service]
Type=simple
ExecStart=/bin/sleep 30
EOF

printf 'Enabling target alias...\n'
"$ROOT/servicectl" enable pipewire.target >"$SHOW_OUTPUT"
assert_contains "$SHOW_OUTPUT" "Enabled group:test-audio"

printf 'Checking group status...\n'
"$ROOT/servicectl" status group:test-audio >"$GROUP_OUTPUT"
assert_contains "$GROUP_OUTPUT" "group:test-audio enabled"
assert_contains "$GROUP_OUTPUT" "${UNIT_NAME}.service"

printf 'Checking is-enabled for target...\n'
TARGET_ENABLED="$("$ROOT/servicectl" is-enabled pipewire.target || true)"
if [[ "$TARGET_ENABLED" != "enabled" ]]; then
  printf 'assertion failed: expected enabled, got %s\n' "$TARGET_ENABLED" >&2
  exit 1
fi

printf 'Disabling target alias...\n'
"$ROOT/servicectl" disable pipewire.target >"$SHOW_OUTPUT"
assert_contains "$SHOW_OUTPUT" "Disabled group:test-audio"

TARGET_DISABLED="$("$ROOT/servicectl" is-enabled pipewire.target || true)"
if [[ "$TARGET_DISABLED" != "disabled" ]]; then
  printf 'assertion failed: expected disabled, got %s\n' "$TARGET_DISABLED" >&2
  exit 1
fi

printf 'property target integration test passed.\n'
