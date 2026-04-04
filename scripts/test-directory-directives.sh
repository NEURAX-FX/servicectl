#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
USER_UNIT_NAME="directory-directives-user-demo"
SYSTEM_UNIT_NAME="directory-directives-system-demo"
USER_UNIT_PATH="/root/.config/systemd/user/${USER_UNIT_NAME}.service"
SYSTEM_UNIT_PATH="/etc/systemd/system/${SYSTEM_UNIT_NAME}.service"

USER_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/runtime-0}/${USER_UNIT_NAME}-runtime"
USER_STATE_DIR="${XDG_STATE_HOME:-/root/.local/state}/${USER_UNIT_NAME}-state"
USER_LOGS_DIR="${XDG_STATE_HOME:-/root/.local/state}/log/${USER_UNIT_NAME}-logs"

SYSTEM_RUNTIME_DIR="/run/${SYSTEM_UNIT_NAME}-runtime"
SYSTEM_STATE_DIR="/var/lib/${SYSTEM_UNIT_NAME}-state"
SYSTEM_LOGS_DIR="/var/log/${SYSTEM_UNIT_NAME}-logs"

cleanup() {
	XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/runtime-0}" "$ROOT/servicectl" --user stop "$USER_UNIT_NAME" >/dev/null 2>&1 || true
	"$ROOT/servicectl" stop "$SYSTEM_UNIT_NAME" >/dev/null 2>&1 || true
	dinitctl --user stop "${USER_UNIT_NAME}-log" >/dev/null 2>&1 || true
	dinitctl stop "${SYSTEM_UNIT_NAME}-log" >/dev/null 2>&1 || true
	rm -f "$USER_UNIT_PATH" "$SYSTEM_UNIT_PATH"
	rm -rf "$USER_RUNTIME_DIR" "$USER_STATE_DIR" "$USER_LOGS_DIR"
	rm -rf "$SYSTEM_RUNTIME_DIR" "$SYSTEM_STATE_DIR" "$SYSTEM_LOGS_DIR"
	rm -f "${XDG_RUNTIME_DIR:-/tmp/runtime-0}/dinit.d/generated/${USER_UNIT_NAME}" "/root/.config/dinit.d/${USER_UNIT_NAME}"
	rm -f "/run/dinit.d/generated/${SYSTEM_UNIT_NAME}" "/etc/dinit.d/${SYSTEM_UNIT_NAME}"
}
trap cleanup EXIT

assert_mode() {
	local path="$1"
	local expected="$2"
	local actual
	actual="$(stat -c '%a' "$path")"
	[[ "$actual" == "$expected" ]] || { printf 'unexpected mode for %s: %s\n' "$path" "$actual" >&2; exit 1; }
}

printf 'Building servicectl...\n'
go build -o "$ROOT/servicectl" .

cat >"$USER_UNIT_PATH" <<EOF
[Unit]
Description=Directory directives demo

[Service]
Type=oneshot
User=root
Group=root
RuntimeDirectory=${USER_UNIT_NAME}-runtime
RuntimeDirectoryMode=0750
StateDirectory=${USER_UNIT_NAME}-state
StateDirectoryMode=0700
LogsDirectory=${USER_UNIT_NAME}-logs
LogsDirectoryMode=0750
ExecStart=/bin/true
EOF

cat >"$SYSTEM_UNIT_PATH" <<EOF
[Unit]
Description=Directory directives demo (system)

[Service]
Type=oneshot
User=root
Group=root
RuntimeDirectory=${SYSTEM_UNIT_NAME}-runtime
RuntimeDirectoryMode=0750
StateDirectory=${SYSTEM_UNIT_NAME}-state
StateDirectoryMode=0700
LogsDirectory=${SYSTEM_UNIT_NAME}-logs
LogsDirectoryMode=0750
ExecStart=/bin/true
EOF

printf 'Starting user-mode directory directives demo...\n'
XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/runtime-0}" "$ROOT/servicectl" --user start "$USER_UNIT_NAME"

printf 'Checking user-mode directories...\n'
[[ -d "$USER_RUNTIME_DIR" ]] || { printf 'missing runtime dir %s\n' "$USER_RUNTIME_DIR" >&2; exit 1; }
[[ -d "$USER_STATE_DIR" ]] || { printf 'missing state dir %s\n' "$USER_STATE_DIR" >&2; exit 1; }
[[ -d "$USER_LOGS_DIR" ]] || { printf 'missing logs dir %s\n' "$USER_LOGS_DIR" >&2; exit 1; }

assert_mode "$USER_RUNTIME_DIR" 750
assert_mode "$USER_STATE_DIR" 700
assert_mode "$USER_LOGS_DIR" 750

printf 'Starting system-mode directory directives demo...\n'
"$ROOT/servicectl" start "$SYSTEM_UNIT_NAME"

printf 'Checking system-mode directories...\n'
[[ -d "$SYSTEM_RUNTIME_DIR" ]] || { printf 'missing runtime dir %s\n' "$SYSTEM_RUNTIME_DIR" >&2; exit 1; }
[[ -d "$SYSTEM_STATE_DIR" ]] || { printf 'missing state dir %s\n' "$SYSTEM_STATE_DIR" >&2; exit 1; }
[[ -d "$SYSTEM_LOGS_DIR" ]] || { printf 'missing logs dir %s\n' "$SYSTEM_LOGS_DIR" >&2; exit 1; }

assert_mode "$SYSTEM_RUNTIME_DIR" 750
assert_mode "$SYSTEM_STATE_DIR" 700
assert_mode "$SYSTEM_LOGS_DIR" 750

printf 'Directory directives test passed for user and system modes.\n'
