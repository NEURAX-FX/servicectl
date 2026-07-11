#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
INSTALL_SCRIPT="$ROOT/scripts/install.sh"

grep -Fq 'dbusActivationHelperPath=/usr/local/libexec/servicectl/sys-dbusd-daemon-helper' "$INSTALL_SCRIPT"
grep -Fq '<servicehelper>/usr/local/libexec/servicectl/sys-dbusd-daemon-helper</servicehelper>' "$INSTALL_SCRIPT"
grep -Fq -- '-helper-path /usr/local/libexec/servicectl/sys-dbusd-daemon-helper' "$INSTALL_SCRIPT"
grep -Fq 'build_and_install sys-cgroupd "$ROOT/cmd/sys-cgroupd"' "$INSTALL_SCRIPT"
grep -Fq 'command = /usr/local/bin/sys-cgroupd' "$INSTALL_SCRIPT"
if grep -Eq '(^|[[:space:]])mount[[:space:]].*cgroup2|unix\.Mount' "$INSTALL_SCRIPT"; then
  printf '%s\n' 'installer must not mount cgroup2' >&2
  exit 1
fi
grep -Fq 'command = /usr/bin/sys-cgroupd' "$ROOT/packaging/sys-cgroupd"
if grep -Fq -- '--no-auto-mount' "$ROOT/packaging/sys-cgroupd"; then
  printf '%s\n' 'packaged daemon must keep safe auto-mount enabled by default' >&2
  exit 1
fi
grep -Fq '/run/servicectl/sys-cgroupd' "$ROOT/packaging/servicectl.tmpfiles"
grep -Fq 'go build -trimpath -buildvcs=false -o _build/bin/sys-cgroupd ./cmd/sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"
grep -Fq '%{_bindir}/sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"

printf '%s\n' 'install path checks passed'
