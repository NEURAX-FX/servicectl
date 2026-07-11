#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d /var/tmp/servicectl-dbus-daemon.XXXXXX)
chmod 0755 "$TMP"
DBUS_PID=""
CORE_PID=""

cleanup() {
  if [[ -n "$CORE_PID" ]]; then
    kill "$CORE_PID" 2>/dev/null || true
    wait "$CORE_PID" 2>/dev/null || true
  fi
  if [[ -n "$DBUS_PID" ]]; then
    kill "$DBUS_PID" 2>/dev/null || true
    wait "$DBUS_PID" 2>/dev/null || true
  fi
  rm -rf -- "$TMP"
}
trap cleanup EXIT

for command in dbus-daemon busctl cc go getent; do
  command -v "$command" >/dev/null 2>&1 || {
    printf 'required command not found: %s\n' "$command" >&2
    exit 1
  }
done
if [[ $EUID -ne 0 ]]; then
  printf '%s\n' 'test-dbus-daemon-activation.sh must run as root for the setuid helper test' >&2
  exit 1
fi

DBUS_UID=$(getent passwd dbus | cut -d: -f3)
DBUS_GID=$(getent group dbus | cut -d: -f3)
[[ -n "$DBUS_UID" && -n "$DBUS_GID" ]]

install -d -m 0755 "$TMP/bin" "$TMP/services" "$TMP/units" "$TMP/runtime" "$TMP/bus"
chown dbus:dbus "$TMP/bus"

go build -o "$TMP/bin/sys-dbusd" "$ROOT/cmd/sys-dbusd"
go build -o "$TMP/bin/dbus-echod" "$ROOT/cmd/dbus-echod"
cc \
  -DSDBUSD_CONTROL_PATH=\"$TMP/runtime/control.sock\" \
  -DSDBUSD_DAEMON_PATH=\"/usr/bin/dbus-daemon\" \
  -O2 -pipe -Wall -Wextra -Werror -std=c17 \
  -o "$TMP/bin/sys-dbusd-daemon-helper" \
  "$ROOT/cmd/sys-dbusd-daemon-helper/src/main.c"
chown root:dbus "$TMP/bin/sys-dbusd-daemon-helper"
chmod 4750 "$TMP/bin/sys-dbusd-daemon-helper"

cat > "$TMP/services/org.example.Echo.service" <<EOF
[D-BUS Service]
Name=org.example.Echo
Exec=$TMP/bin/dbus-echod org.example.Echo
User=root
EOF

cat > "$TMP/system.conf" <<EOF
<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>system</type>
  <user>dbus</user>
  <listen>unix:path=$TMP/bus/system_bus_socket</listen>
  <servicehelper>$TMP/bin/sys-dbusd-daemon-helper</servicehelper>
  <servicedir>$TMP/services</servicedir>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
    <allow user="*"/>
  </policy>
</busconfig>
EOF

dbus-daemon --config-file="$TMP/system.conf" --nofork --print-address=1 > "$TMP/dbus.address" 2> "$TMP/dbus.log" &
DBUS_PID=$!
for _ in $(seq 1 200); do
  [[ -s "$TMP/dbus.address" ]] && break
  sleep 0.01
done
DBUS_ADDRESS=$(sed -n '1p' "$TMP/dbus.address")
[[ -n "$DBUS_ADDRESS" ]]

"$TMP/bin/sys-dbusd" \
  -control-path "$TMP/runtime/control.sock" \
  -bus-address "$DBUS_ADDRESS" \
  -service-dir "$TMP/services" \
  -systemd-path "$TMP/units" \
  -helper-path "$TMP/bin/sys-dbusd-daemon-helper" \
  -admin-path "$TMP/bin/unused-admin" \
  -state-file "$TMP/runtime/state.json" \
  > "$TMP/sys-dbusd.log" 2>&1 &
CORE_PID=$!

for _ in $(seq 1 200); do
  [[ -S "$TMP/runtime/control.sock" ]] && break
  sleep 0.01
done
[[ -S "$TMP/runtime/control.sock" ]]

set +e
output=$(timeout 10s busctl --address="$DBUS_ADDRESS" call org.example.Echo /org/example/Echo org.example.Echo Echo s hello 2> "$TMP/busctl.err")
busctl_status=$?
set -e
if [[ $busctl_status -ne 0 ]]; then
  printf 'busctl failed with status %d\n' "$busctl_status" >&2
  while IFS= read -r line; do printf '%s\n' "$line" >&2; done < "$TMP/busctl.err"
  printf '%s\n' 'dbus-daemon log:' >&2
  while IFS= read -r line; do printf '%s\n' "$line" >&2; done < "$TMP/dbus.log"
  printf '%s\n' 'sys-dbusd log:' >&2
  while IFS= read -r line; do printf '%s\n' "$line" >&2; done < "$TMP/sys-dbusd.log"
  exit 1
fi
if [[ "$output" != 's "hello"' ]]; then
  printf 'unexpected Echo response: %s\n' "$output" >&2
  printf '%s\n' 'sys-dbusd log:' >&2
  while IFS= read -r line; do printf '%s\n' "$line" >&2; done < "$TMP/sys-dbusd.log"
  exit 1
fi

printf '%s\n' 'dbus-daemon activation integration passed'
