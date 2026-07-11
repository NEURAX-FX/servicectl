#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
INSTALL_SCRIPT="$ROOT/scripts/install.sh"

grep -Fq 'dbusActivationHelperPath=/usr/local/libexec/servicectl/sys-dbusd-daemon-helper' "$INSTALL_SCRIPT"
grep -Fq '<servicehelper>/usr/local/libexec/servicectl/sys-dbusd-daemon-helper</servicehelper>' "$INSTALL_SCRIPT"
grep -Fq -- '-helper-path /usr/local/libexec/servicectl/sys-dbusd-daemon-helper' "$INSTALL_SCRIPT"
grep -Fq 'build_and_install sys-cgroupd "$ROOT/cmd/sys-cgroupd"' "$INSTALL_SCRIPT"
grep -Fq 'rm -f /etc/dinit.d/sys-cgroupd' "$INSTALL_SCRIPT"
grep -Fq '"$SYSTEM_BINDIR/servicectl" ensure-s6' "$INSTALL_SCRIPT"
if grep -Eq '(^|[[:space:]])mount[[:space:]].*cgroup2|unix\.Mount' "$INSTALL_SCRIPT"; then
  printf '%s\n' 'installer must not mount cgroup2' >&2
  exit 1
fi
test ! -e "$ROOT/packaging/sys-cgroupd"
if grep -Eq 'install .*dinit\.d/sys-cgroupd|%config.*dinit\.d/sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"; then
	printf '%s\n' 'RPM must not package a dinit sys-cgroupd service' >&2
	exit 1
fi
grep -Fq 'dinitctl stop sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"
grep -Fq 'rm -f %{_sysconfdir}/dinit.d/sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"
grep -Fq '%{_bindir}/servicectl ensure-s6' "$ROOT/packaging/servicectl-stack.spec"
grep -Fq '/run/servicectl/sys-cgroupd' "$ROOT/packaging/servicectl.tmpfiles"
grep -Fq 'go build -trimpath -buildvcs=false -o _build/bin/sys-cgroupd ./cmd/sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"
grep -Fq '%{_bindir}/sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"

printf '%s\n' 'install path checks passed'
