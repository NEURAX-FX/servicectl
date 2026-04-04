#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PREFIX="${HOME}/.local"
BINDIR=""
SYSTEM_MODE=0

usage() {
  cat <<'EOF'
Usage: scripts/install.sh [--prefix DIR] [--bindir DIR] [--system]

Installs:
  - servicectl
  - sys-notifyd
  - sys-logd
  - sysvisiond
  - sys-orchestrd

Defaults:
  prefix: ~/.local
  bindir: <prefix>/bin

Options:
  --prefix DIR  Install under DIR
  --bindir DIR  Install binaries into DIR
  --system      Shortcut for --prefix /usr/local
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

printf 'Installing to %s\n' "$BINDIR"
mkdir -p "$BINDIR"

build_and_install() {
  local output_name="$1"
  local package_path="$2"
  printf 'Building %s\n' "$output_name"
  go build -o "$BINDIR/$output_name" "$package_path"
}

build_and_install servicectl "$ROOT"
build_and_install sys-notifyd "$ROOT/cmd/sys-notifyd"
build_and_install sys-logd "$ROOT/cmd/sys-logd"
build_and_install sysvisiond "$ROOT/cmd/sysvisiond"
build_and_install sys-orchestrd "$ROOT/cmd/sys-orchestrd"

printf '\nInstalled binaries:\n'
printf '  %s\n' "$BINDIR/servicectl"
printf '  %s\n' "$BINDIR/sys-notifyd"
printf '  %s\n' "$BINDIR/sys-logd"
printf '  %s\n' "$BINDIR/sysvisiond"
printf '  %s\n' "$BINDIR/sys-orchestrd"

case ":$PATH:" in
  *":$BINDIR:"*)
    ;;
  *)
    printf '\nNote: %s is not currently in PATH\n' "$BINDIR"
    ;;
esac
