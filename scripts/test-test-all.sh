#!/usr/bin/env bash

set -euo pipefail

ROOT="/root/servicectl"
TMPDIR="$(mktemp -d /tmp/test-all-script.XXXXXX)"
FAKEBIN="$TMPDIR/bin"
CALLS="$TMPDIR/calls.log"
OUTPUT="$TMPDIR/output.log"

cleanup() {
  rm -rf "$TMPDIR"
}
trap cleanup EXIT

mkdir -p "$FAKEBIN"

cat >"$FAKEBIN/go" <<EOF
#!/bin/bash
printf 'go %s\n' "\$*" >>"$CALLS"
exit 0
EOF

cat >"$FAKEBIN/bash" <<EOF
#!/bin/bash
printf 'bash %s\n' "\$*" >>"$CALLS"
exit 0
EOF

cat >"$FAKEBIN/sleep" <<EOF
#!/bin/bash
printf 'sleep %s\n' "\$*" >>"$CALLS"
exit 0
EOF

chmod +x "$FAKEBIN/go" "$FAKEBIN/bash" "$FAKEBIN/sleep"

assert_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Fq -- "$pattern" "$file"; then
    printf 'assertion failed: %s missing %s\n' "$file" "$pattern" >&2
    exit 1
  fi
}

PATH="$FAKEBIN:$PATH" /bin/bash "$ROOT/scripts/test-all.sh" >"$OUTPUT" 2>&1

assert_contains "$OUTPUT" 'WARNING: running host integration suites against this machine.'
assert_contains "$OUTPUT" 'Continuing in 5 seconds. Press Ctrl-C to abort.'
assert_contains "$CALLS" 'sleep 5'
assert_contains "$CALLS" 'bash /root/servicectl/scripts/test-property-targets.sh'
assert_contains "$CALLS" 'bash /root/servicectl/scripts/test-s6-orchestrd.sh'

printf 'test-all warning integration test passed.\n'
