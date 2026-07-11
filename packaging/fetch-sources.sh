#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
LOCK_FILE="$SCRIPT_DIR/sources.lock"
CACHE_DIR="$SCRIPT_DIR/sources"
OFFLINE=0

usage() {
  cat <<'EOF'
Usage: packaging/fetch-sources.sh [options]

Options:
  --cache-dir DIR  Store verified archives in DIR
  --offline        Verify the cache without downloading
  -h, --help       Show this help
EOF
}

while (($# > 0)); do
  case "$1" in
    --cache-dir)
      [[ $# -ge 2 ]] || { printf '%s\n' 'missing value for --cache-dir' >&2; exit 2; }
      CACHE_DIR=$2
      shift 2
      ;;
    --offline)
      OFFLINE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown option: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

command -v sha256sum >/dev/null 2>&1 || {
  printf '%s\n' 'required command not found: sha256sum' >&2
  exit 1
}
if ((OFFLINE == 0)); then
  command -v curl >/dev/null 2>&1 || {
    printf '%s\n' 'required command not found: curl' >&2
    exit 1
  }
fi

mkdir -p -- "$CACHE_DIR"
CACHE_DIR=$(cd -- "$CACHE_DIR" && pwd)

verify_archive() {
  local path=$1
  local expected=$2
  local actual
  [[ -f "$path" ]] || return 1
  actual=$(sha256sum -- "$path")
  actual=${actual%% *}
  [[ "$actual" == "$expected" ]]
}

while IFS=$'\t' read -r component source_version digest url filename; do
  [[ "$component" == "component" || -z "$component" ]] && continue
  destination="$CACHE_DIR/$filename"
  if verify_archive "$destination" "$digest"; then
    printf 'verified %s %s\n' "$component" "$source_version"
    continue
  fi

  if ((OFFLINE == 1)); then
    printf 'missing or invalid cached source: %s\n' "$destination" >&2
    exit 1
  fi

  rm -f -- "$destination"
  temporary=$(mktemp "$CACHE_DIR/.${filename}.XXXXXX")
  if ! curl --fail --location --retry 3 --output "$temporary" "$url"; then
    rm -f -- "$temporary"
    exit 1
  fi
  if ! verify_archive "$temporary" "$digest"; then
    printf 'checksum mismatch for downloaded source: %s\n' "$filename" >&2
    rm -f -- "$temporary"
    exit 1
  fi
  mv -f -- "$temporary" "$destination"
  printf 'downloaded and verified %s %s\n' "$component" "$source_version"
done < "$LOCK_FILE"

printf '%s\n' "$CACHE_DIR"
