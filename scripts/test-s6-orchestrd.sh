#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
SYSTEM_UNIT_NAME="s6-orchestrd-demo"
SYSTEM_UNIT_PATH="/etc/systemd/system/${SYSTEM_UNIT_NAME}.service"
SYSTEM_SERVICE_DIR="/s6/rc/${SYSTEM_UNIT_NAME}-orchestrd"
SYSTEM_SYSVISION_DIR="/s6/rc/sysvisiond"
SYSTEM_API_DIR="/s6/rc/servicectl-api"
SYSTEM_BUNDLE_DIR="/s6/rc/servicectl-enabled"
SYSTEM_DEFAULT_CONTENTS="/s6/rc/default/contents"
USER_UNIT_NAME="s6-user-orchestrd-demo"
USER_RUNTIME="/tmp/runtime-0"
USER_BACKEND_ROOT="/run/user/$(id -u)"
USER_ROOT="$USER_BACKEND_ROOT/s6/rc"
USER_UNIT_PATH="$HOME/.config/systemd/user/${USER_UNIT_NAME}.service"
USER_SERVICE_DIR="$USER_ROOT/${USER_UNIT_NAME}-user-orchestrd"
USER_SYSVISION_DIR="$USER_ROOT/sysvisiond-user"
USER_API_DIR="$USER_ROOT/servicectl-user-api"
USER_BUNDLE_DIR="$USER_ROOT/servicectl-user-enabled"
USER_DEFAULT_CONTENTS="$USER_ROOT/default/contents"
BACKUP_DIR="$(mktemp -d /tmp/servicectl-s6-backup.XXXXXX)"

cleanup() {
  if [[ -f "$BACKUP_DIR/system.default.contents" ]]; then
    cp "$BACKUP_DIR/system.default.contents" "$SYSTEM_DEFAULT_CONTENTS"
  fi
  rm -rf "$SYSTEM_BUNDLE_DIR" "$SYSTEM_SERVICE_DIR"
  rm -rf "$SYSTEM_SYSVISION_DIR"
  rm -rf "$SYSTEM_API_DIR"
  if [[ -d "$BACKUP_DIR/system.bundle" ]]; then
    cp -a "$BACKUP_DIR/system.bundle" "$SYSTEM_BUNDLE_DIR"
  fi
  if [[ -d "$BACKUP_DIR/system.service" ]]; then
    cp -a "$BACKUP_DIR/system.service" "$SYSTEM_SERVICE_DIR"
  fi
  if [[ -d "$BACKUP_DIR/system.sysvisiond" ]]; then
    cp -a "$BACKUP_DIR/system.sysvisiond" "$SYSTEM_SYSVISION_DIR"
  fi
  if [[ -d "$BACKUP_DIR/system.api" ]]; then
    cp -a "$BACKUP_DIR/system.api" "$SYSTEM_API_DIR"
  fi
  if [[ -f "$BACKUP_DIR/user.default.contents" ]]; then
    mkdir -p "$(dirname "$USER_DEFAULT_CONTENTS")"
    cp "$BACKUP_DIR/user.default.contents" "$USER_DEFAULT_CONTENTS"
  fi
  rm -rf "$USER_BUNDLE_DIR" "$USER_SERVICE_DIR"
  rm -rf "$USER_SYSVISION_DIR"
  rm -rf "$USER_API_DIR"
  if [[ -d "$BACKUP_DIR/user.bundle" ]]; then
    cp -a "$BACKUP_DIR/user.bundle" "$USER_BUNDLE_DIR"
  fi
  if [[ -d "$BACKUP_DIR/user.service" ]]; then
    cp -a "$BACKUP_DIR/user.service" "$USER_SERVICE_DIR"
  fi
  if [[ -d "$BACKUP_DIR/user.sysvisiond" ]]; then
    cp -a "$BACKUP_DIR/user.sysvisiond" "$USER_SYSVISION_DIR"
  fi
  if [[ -d "$BACKUP_DIR/user.api" ]]; then
    cp -a "$BACKUP_DIR/user.api" "$USER_API_DIR"
  fi
  rm -f "$SYSTEM_UNIT_PATH" "$USER_UNIT_PATH"
  rm -rf "$BACKUP_DIR"
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

printf 'Building s6/orchestrd binaries...\n'
go build -o "$ROOT/servicectl" .
go build -o "$ROOT/sys-orchestrd" ./cmd/sys-orchestrd

cp "$SYSTEM_DEFAULT_CONTENTS" "$BACKUP_DIR/system.default.contents"
if [[ -d "$SYSTEM_BUNDLE_DIR" ]]; then
  cp -a "$SYSTEM_BUNDLE_DIR" "$BACKUP_DIR/system.bundle"
fi
if [[ -d "$SYSTEM_SERVICE_DIR" ]]; then
  cp -a "$SYSTEM_SERVICE_DIR" "$BACKUP_DIR/system.service"
fi
if [[ -d "$SYSTEM_SYSVISION_DIR" ]]; then
  cp -a "$SYSTEM_SYSVISION_DIR" "$BACKUP_DIR/system.sysvisiond"
fi
if [[ -d "$SYSTEM_API_DIR" ]]; then
  cp -a "$SYSTEM_API_DIR" "$BACKUP_DIR/system.api"
fi

mkdir -p "$USER_ROOT" "$(dirname "$USER_UNIT_PATH")" "$USER_BACKEND_ROOT"
if [[ -f "$USER_DEFAULT_CONTENTS" ]]; then
  cp "$USER_DEFAULT_CONTENTS" "$BACKUP_DIR/user.default.contents"
fi
if [[ -d "$USER_BUNDLE_DIR" ]]; then
  cp -a "$USER_BUNDLE_DIR" "$BACKUP_DIR/user.bundle"
fi
if [[ -d "$USER_SERVICE_DIR" ]]; then
  cp -a "$USER_SERVICE_DIR" "$BACKUP_DIR/user.service"
fi
if [[ -d "$USER_SYSVISION_DIR" ]]; then
  cp -a "$USER_SYSVISION_DIR" "$BACKUP_DIR/user.sysvisiond"
fi
if [[ -d "$USER_API_DIR" ]]; then
  cp -a "$USER_API_DIR" "$BACKUP_DIR/user.api"
fi

cat >"$SYSTEM_UNIT_PATH" <<'EOF'
[Unit]
Description=s6 orchestrd demo

[Service]
Type=simple
ExecStart=/bin/sleep 30
EOF

cat >"$USER_UNIT_PATH" <<'EOF'
[Unit]
Description=s6 user orchestrd demo

[Service]
Type=simple
ExecStart=/bin/sleep 30
EOF

printf 'Enabling system unit through s6 backend...\n'
"$ROOT/servicectl" enable "$SYSTEM_UNIT_NAME" >/tmp/s6-enable.out
assert_contains /tmp/s6-enable.out "Enabled ${SYSTEM_UNIT_NAME}"
assert_contains "$SYSTEM_BUNDLE_DIR/type" "bundle"
assert_contains "$SYSTEM_BUNDLE_DIR/contents" "${SYSTEM_UNIT_NAME}-orchestrd"
assert_contains "$SYSTEM_DEFAULT_CONTENTS" "servicectl-enabled"
assert_contains "$SYSTEM_DEFAULT_CONTENTS" "sysvisiond"
assert_contains "$SYSTEM_DEFAULT_CONTENTS" "servicectl-api"
assert_contains "$SYSTEM_SERVICE_DIR/type" "longrun"
assert_contains "$SYSTEM_SERVICE_DIR/run" "sys-orchestrd --unit ${SYSTEM_UNIT_NAME}.service"
assert_contains "$SYSTEM_SYSVISION_DIR/run" "sysvisiond"
assert_contains "$SYSTEM_API_DIR/run" "servicectl serve-api"

printf 'Checking is-enabled output...\n'
ENABLED_OUTPUT="$("$ROOT/servicectl" is-enabled "$SYSTEM_UNIT_NAME" || true)"
if [[ "$ENABLED_OUTPUT" != "enabled" ]]; then
  printf 'assertion failed: expected enabled, got %s\n' "$ENABLED_OUTPUT" >&2
  exit 1
fi

printf 'Disabling system unit through s6 backend...\n'
"$ROOT/servicectl" disable "$SYSTEM_UNIT_NAME" >/tmp/s6-disable.out
assert_contains /tmp/s6-disable.out "Disabled ${SYSTEM_UNIT_NAME}"
if [[ -d "$SYSTEM_SERVICE_DIR" ]]; then
  printf 'assertion failed: service source dir still exists: %s\n' "$SYSTEM_SERVICE_DIR" >&2
  exit 1
fi
DISABLED_OUTPUT="$("$ROOT/servicectl" is-enabled "$SYSTEM_UNIT_NAME" || true)"
if [[ "$DISABLED_OUTPUT" != "disabled" ]]; then
  printf 'assertion failed: expected disabled, got %s\n' "$DISABLED_OUTPUT" >&2
  exit 1
fi

printf 'Enabling user unit through s6 backend...\n'
XDG_RUNTIME_DIR="$USER_RUNTIME" "$ROOT/servicectl" --user enable "$USER_UNIT_NAME" >/tmp/s6-user-enable.out
assert_contains /tmp/s6-user-enable.out "Enabled ${USER_UNIT_NAME}"
assert_contains "$USER_BUNDLE_DIR/type" "bundle"
assert_contains "$USER_BUNDLE_DIR/contents" "${USER_UNIT_NAME}-user-orchestrd"
assert_contains "$USER_DEFAULT_CONTENTS" "servicectl-user-enabled"
assert_contains "$USER_DEFAULT_CONTENTS" "sysvisiond-user"
assert_contains "$USER_DEFAULT_CONTENTS" "servicectl-user-api"
assert_contains "$USER_SERVICE_DIR/type" "longrun"
assert_contains "$USER_SERVICE_DIR/run" "sys-orchestrd --user --unit ${USER_UNIT_NAME}.service"
assert_contains "$USER_SYSVISION_DIR/run" "sysvisiond --user"
assert_contains "$USER_API_DIR/run" "servicectl --user serve-api"

printf 'Checking user is-enabled output...\n'
USER_ENABLED_OUTPUT="$(XDG_RUNTIME_DIR="$USER_RUNTIME" "$ROOT/servicectl" --user is-enabled "$USER_UNIT_NAME" || true)"
if [[ "$USER_ENABLED_OUTPUT" != "enabled" ]]; then
  printf 'assertion failed: expected enabled, got %s\n' "$USER_ENABLED_OUTPUT" >&2
  exit 1
fi

printf 'Disabling user unit through s6 backend...\n'
XDG_RUNTIME_DIR="$USER_RUNTIME" "$ROOT/servicectl" --user disable "$USER_UNIT_NAME" >/tmp/s6-user-disable.out
assert_contains /tmp/s6-user-disable.out "Disabled ${USER_UNIT_NAME}"
if [[ -d "$USER_SERVICE_DIR" ]]; then
  printf 'assertion failed: user service source dir still exists: %s\n' "$USER_SERVICE_DIR" >&2
  exit 1
fi
USER_DISABLED_OUTPUT="$(XDG_RUNTIME_DIR="$USER_RUNTIME" "$ROOT/servicectl" --user is-enabled "$USER_UNIT_NAME" || true)"
if [[ "$USER_DISABLED_OUTPUT" != "disabled" ]]; then
  printf 'assertion failed: expected disabled, got %s\n' "$USER_DISABLED_OUTPUT" >&2
  exit 1
fi

printf 's6/orchestrd backend test passed.\n'
