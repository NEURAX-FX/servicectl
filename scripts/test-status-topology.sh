#!/usr/bin/env bash

set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

go test -C "$ROOT" -count=1 . -run '^TestStatusTopologyIntegration'

printf '%s\n' 'status topology integration checks passed'
