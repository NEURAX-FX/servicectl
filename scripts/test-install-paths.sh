#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
INSTALL_SCRIPT="$ROOT/scripts/install.sh"

grep -Fq 'dbusActivationHelperPath=/usr/local/libexec/servicectl/sys-dbusd-daemon-helper' "$INSTALL_SCRIPT"
grep -Fq '<servicehelper>/usr/local/libexec/servicectl/sys-dbusd-daemon-helper</servicehelper>' "$INSTALL_SCRIPT"
grep -Fq -- '-helper-path /usr/local/libexec/servicectl/sys-dbusd-daemon-helper' "$INSTALL_SCRIPT"
grep -Fq 'build_and_install sys-cgroupd "$ROOT/cmd/sys-cgroupd"' "$INSTALL_SCRIPT"
grep -Fq 'command = /usr/local/bin/sys-cgroupd' "$INSTALL_SCRIPT"
grep -Fq '/run/servicectl/sys-cgroupd' "$ROOT/packaging/servicectl.tmpfiles"
grep -Fq 'go build -trimpath -buildvcs=false -o _build/bin/sys-cgroupd ./cmd/sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"
grep -Fq '%{_bindir}/sys-cgroupd' "$ROOT/packaging/servicectl-stack.spec"

printf '%s\n' 'install path checks passed'
