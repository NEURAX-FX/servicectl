#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
INSTALL_SCRIPT="$ROOT/scripts/install.sh"

grep -Fq 'dbusActivationHelperPath=/usr/local/libexec/servicectl/sys-dbusd-daemon-helper' "$INSTALL_SCRIPT"
grep -Fq '<servicehelper>/usr/local/libexec/servicectl/sys-dbusd-daemon-helper</servicehelper>' "$INSTALL_SCRIPT"
grep -Fq -- '-helper-path /usr/local/libexec/servicectl/sys-dbusd-daemon-helper' "$INSTALL_SCRIPT"

printf '%s\n' 'install path checks passed'
