#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

usage() {
  printf '%s\n' 'usage: test-cgroupd-integration.sh [--self-test|--cgroup-root PATH]'
}

case "${1:-}" in
  --self-test)
    [[ $# -eq 1 ]] || { usage >&2; exit 2; }
    command -v go >/dev/null
    bash -n "$0"
    grep -Fq 'SERVICECTL_CGROUP_TEST_ROOT' "$ROOT/cmd/sys-cgroupd/integration_test.go"
    if grep -Eq 'cgroup\.kill|cgroup\.freeze' "$ROOT/scripts/test-cgroupd-integration.sh"; then
      printf '%s\n' 'integration script contains a forbidden cgroup control path' >&2
      exit 1
    fi
    printf '%s\n' 'cgroupd integration self-test passed'
    exit 0
    ;;
  --cgroup-root)
    [[ $# -eq 2 && -n "$2" ]] || { usage >&2; exit 2; }
    [[ $EUID -eq 0 ]] || { printf '%s\n' 'real cgroupd integration requires root' >&2; exit 1; }
    [[ "$2" == /* ]] || { printf '%s\n' 'cgroup root must be absolute' >&2; exit 2; }
    [[ -d "$2" && -w "$2" ]] || { printf 'cgroup root is not a writable directory: %s\n' "$2" >&2; exit 1; }
    export SERVICECTL_CGROUP_TEST_ROOT="$2"
    exec go test ./cmd/sys-cgroupd -run '^TestCgroupV2Integration$' -count=1 -v
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
