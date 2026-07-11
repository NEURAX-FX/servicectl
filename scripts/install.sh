#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PREFIX="${HOME}/.local"
BINDIR=""
SYSTEM_MODE=0
INSTALL_SYSTEM_COPY=0

usage() {
  cat <<'EOF'
Usage: scripts/install.sh [--prefix DIR] [--bindir DIR] [--system] [--also-system]

Installs:
  - servicectl
  - sys-notifyd
  - sys-cgroupd
  - sys-dbusd
  - sys-logd
  - sys-propertyd
  - sysvisiond
  - sys-orchestrd

Defaults:
  prefix: ~/.local
  bindir: <prefix>/bin

Options:
  --prefix DIR  Install under DIR
  --bindir DIR  Install binaries into DIR
  --system      Shortcut for --prefix /usr/local
  --also-system Also install a copy into /usr/local/bin
  -h, --help    Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)
      if [[ $# -lt 2 ]]; then
        printf 'missing value for %s\n' "$1" >&2
        exit 1
      fi
      PREFIX="$2"
      shift 2
      ;;
    --bindir)
      if [[ $# -lt 2 ]]; then
        printf 'missing value for %s\n' "$1" >&2
        exit 1
      fi
      BINDIR="$2"
      shift 2
      ;;
    --system)
      SYSTEM_MODE=1
      PREFIX="/usr/local"
      shift
      ;;
    --also-system)
      INSTALL_SYSTEM_COPY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown option: %s\n' "$1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ -z "$BINDIR" ]]; then
  BINDIR="${PREFIX}/bin"
fi

if [[ "$SYSTEM_MODE" -eq 1 && "$EUID" -ne 0 ]]; then
  printf '--system installs into /usr/local and requires root\n' >&2
  exit 1
fi

if [[ "$INSTALL_SYSTEM_COPY" -eq 1 && "$EUID" -ne 0 ]]; then
  printf '--also-system installs into /usr/local/bin and requires root\n' >&2
  exit 1
fi

printf 'Installing to %s\n' "$BINDIR"
mkdir -p "$BINDIR"

SYSTEM_BINDIR="/usr/local/bin"
if [[ "$SYSTEM_MODE" -eq 1 ]]; then
  INSTALL_SYSTEM_COPY=0
fi
if [[ "$INSTALL_SYSTEM_COPY" -eq 1 ]]; then
  printf 'Installing system copy to %s\n' "$SYSTEM_BINDIR"
  mkdir -p "$SYSTEM_BINDIR"
fi

build_and_install() {
  local output_name="$1"
  local package_path="$2"
  local -a build_args=()
  if [[ "$output_name" == "servicectl" && ( "$SYSTEM_MODE" -eq 1 || "$INSTALL_SYSTEM_COPY" -eq 1 ) ]]; then
    build_args=(-ldflags '-X main.dbusActivationHelperPath=/usr/local/libexec/servicectl/sys-dbusd-daemon-helper')
  fi
  printf 'Building %s\n' "$output_name"
  go build "${build_args[@]}" -o "$BINDIR/$output_name" "$package_path"
  if [[ "$INSTALL_SYSTEM_COPY" -eq 1 ]]; then
    install -m 0755 "$BINDIR/$output_name" "$SYSTEM_BINDIR/$output_name"
  fi
}

build_and_install servicectl "$ROOT"
build_and_install sys-notifyd "$ROOT/cmd/sys-notifyd"
build_and_install sys-cgroupd "$ROOT/cmd/sys-cgroupd"
build_and_install sys-dbusd "$ROOT/cmd/sys-dbusd"
build_and_install sys-logd "$ROOT/cmd/sys-logd"
build_and_install sys-propertyd "$ROOT/cmd/sys-propertyd"
build_and_install sysvisiond "$ROOT/cmd/sysvisiond"
build_and_install sys-orchestrd "$ROOT/cmd/sys-orchestrd"

if [[ "$SYSTEM_MODE" -eq 1 || "$INSTALL_SYSTEM_COPY" -eq 1 ]]; then
  printf 'Building sys-dbusd-daemon-helper\n'
  make -C "$ROOT/cmd/sys-dbusd-daemon-helper" clean all
  install -d -m 0755 /usr/local/libexec/servicectl
  install -m 4750 -o root -g dbus "$ROOT/cmd/sys-dbusd-daemon-helper/sys-dbusd-daemon-helper" /usr/local/libexec/servicectl/sys-dbusd-daemon-helper
  install -d -m 0755 /usr/local/lib/servicectl/dbus-activation /etc/dinit.d
  printf '%s\n' \
    '<busconfig>' \
    '  <servicehelper>/usr/local/libexec/servicectl/sys-dbusd-daemon-helper</servicehelper>' \
    '</busconfig>' > /usr/local/lib/servicectl/dbus-activation/50-servicectl-activation.conf
  chmod 0644 /usr/local/lib/servicectl/dbus-activation/50-servicectl-activation.conf
  printf '%s\n' \
    '# servicectl D-Bus activation coordinator' \
    'type = process' \
    'command = /usr/local/bin/sys-dbusd -helper-path /usr/local/libexec/servicectl/sys-dbusd-daemon-helper -admin-path /usr/local/bin/servicectl -servicectl-path /usr/local/bin/servicectl -dinitctl-path /bin/dinitctl' \
    'restart = true' \
    'smooth-recovery = true' \
    'log-type = buffer' > /etc/dinit.d/sys-dbusd
	if command -v dinitctl >/dev/null 2>&1; then
		dinitctl stop sys-cgroupd >/dev/null 2>&1 || true
		dinitctl unload sys-cgroupd >/dev/null 2>&1 || true
	fi
	rm -f /etc/dinit.d/sys-cgroupd
	install -d -m 0700 /run/servicectl/sys-cgroupd
	"$SYSTEM_BINDIR/servicectl" ensure-s6
fi

printf '\nInstalled binaries:\n'
printf '  %s\n' "$BINDIR/servicectl"
printf '  %s\n' "$BINDIR/sys-notifyd"
printf '  %s\n' "$BINDIR/sys-cgroupd"
printf '  %s\n' "$BINDIR/sys-dbusd"
printf '  %s\n' "$BINDIR/sys-logd"
printf '  %s\n' "$BINDIR/sys-propertyd"
printf '  %s\n' "$BINDIR/sysvisiond"
printf '  %s\n' "$BINDIR/sys-orchestrd"
if [[ "$INSTALL_SYSTEM_COPY" -eq 1 ]]; then
  printf '  %s\n' "$SYSTEM_BINDIR/servicectl"
  printf '  %s\n' "$SYSTEM_BINDIR/sys-notifyd"
  printf '  %s\n' "$SYSTEM_BINDIR/sys-cgroupd"
  printf '  %s\n' "$SYSTEM_BINDIR/sys-dbusd"
  printf '  %s\n' "$SYSTEM_BINDIR/sys-logd"
  printf '  %s\n' "$SYSTEM_BINDIR/sys-propertyd"
  printf '  %s\n' "$SYSTEM_BINDIR/sysvisiond"
  printf '  %s\n' "$SYSTEM_BINDIR/sys-orchestrd"
fi

case ":$PATH:" in
  *":$BINDIR:"*)
    ;;
  *)
    printf '\nNote: %s is not currently in PATH\n' "$BINDIR"
    ;;
esac
