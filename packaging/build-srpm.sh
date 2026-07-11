#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
OUTPUT_DIR="$ROOT_DIR/dist/srpm"
CACHE_DIR="$SCRIPT_DIR/sources"
VERSION=0.1.0
RELEASE=1
OFFLINE=0
ALLOW_DIRTY=0

usage() {
  cat <<'EOF'
Usage: packaging/build-srpm.sh [options]

Options:
  --output-dir DIR  Write the resulting SRPM to DIR
  --cache-dir DIR   Read verified third-party archives from DIR
  --version VERSION Set the servicectl package version (default: 0.1.0)
  --release RELEASE Set the RPM release number (default: 1)
  --offline         Do not download missing third-party archives
  --allow-dirty     Include current uncommitted worktree changes
  -h, --help        Show this help
EOF
}

while (($# > 0)); do
  case "$1" in
    --output-dir)
      [[ $# -ge 2 ]] || { printf '%s\n' 'missing value for --output-dir' >&2; exit 2; }
      OUTPUT_DIR=$2
      shift 2
      ;;
    --cache-dir)
      [[ $# -ge 2 ]] || { printf '%s\n' 'missing value for --cache-dir' >&2; exit 2; }
      CACHE_DIR=$2
      shift 2
      ;;
    --version)
      [[ $# -ge 2 ]] || { printf '%s\n' 'missing value for --version' >&2; exit 2; }
      VERSION=$2
      shift 2
      ;;
    --release)
      [[ $# -ge 2 ]] || { printf '%s\n' 'missing value for --release' >&2; exit 2; }
      RELEASE=$2
      shift 2
      ;;
    --offline)
      OFFLINE=1
      shift
      ;;
    --allow-dirty)
      ALLOW_DIRTY=1
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

for command in git go rpmbuild tar sha256sum; do
  command -v "$command" >/dev/null 2>&1 || {
    printf 'required command not found: %s\n' "$command" >&2
    exit 1
  }
done
if ((OFFLINE == 0)); then
  command -v curl >/dev/null 2>&1 || {
    printf '%s\n' 'required command not found: curl' >&2
    exit 1
  }
fi

case "$VERSION" in
  ''|*[!0-9A-Za-z.+~_]*)
    printf 'invalid version: %s\n' "$VERSION" >&2
    exit 2
    ;;
esac
case "$RELEASE" in
  ''|*[!0-9A-Za-z.+~_]*)
    printf 'invalid release: %s\n' "$RELEASE" >&2
    exit 2
    ;;
esac

cd -- "$ROOT_DIR"
if ((ALLOW_DIRTY == 0)) && [[ -n $(git status --porcelain --untracked-files=normal) ]]; then
  printf '%s\n' 'worktree is dirty; commit/stash changes or pass --allow-dirty' >&2
  exit 1
fi

fetch_args=(--cache-dir "$CACHE_DIR")
if ((OFFLINE == 1)); then
  fetch_args+=(--offline)
fi
"$SCRIPT_DIR/fetch-sources.sh" "${fetch_args[@]}" >/dev/null
CACHE_DIR=$(cd -- "$CACHE_DIR" && pwd)

mkdir -p -- "$OUTPUT_DIR"
OUTPUT_DIR=$(cd -- "$OUTPUT_DIR" && pwd)
work_dir=$(mktemp -d /tmp/servicectl-srpm.XXXXXX)
trap 'rm -rf -- "$work_dir"' EXIT

snapshot_name="servicectl-$VERSION"
snapshot_dir="$work_dir/$snapshot_name"
mkdir -p -- "$snapshot_dir"

while IFS= read -r -d '' path; do
  if [[ -e "$path" || -L "$path" ]]; then
    printf '%s\0' "$path"
  fi
done < <(git ls-files -co --exclude-standard -z) \
  | tar --null --files-from=- -cf - \
  | tar -xf - -C "$snapshot_dir"

(
  cd -- "$snapshot_dir"
  GOTOOLCHAIN=local go mod vendor
)

source_date_epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
project_archive="$work_dir/$snapshot_name.tar.gz"
tar --sort=name \
  --mtime="@$source_date_epoch" \
  --owner=0 \
  --group=0 \
  --numeric-owner \
  -czf "$project_archive" \
  -C "$work_dir" \
  "$snapshot_name"

topdir="$work_dir/rpmbuild"
mkdir -p -- "$topdir/BUILD" "$topdir/BUILDROOT" "$topdir/RPMS" "$topdir/SOURCES" "$topdir/SPECS" "$topdir/SRPMS"
cp -- "$project_archive" "$topdir/SOURCES/"
while IFS=$'\t' read -r component source_version digest url filename; do
  [[ "$component" == "component" || -z "$component" ]] && continue
  cp -- "$CACHE_DIR/$filename" "$topdir/SOURCES/$filename"
done < "$SCRIPT_DIR/sources.lock"
cp -- "$SCRIPT_DIR/servicectl.tmpfiles" "$topdir/SOURCES/servicectl.tmpfiles"
cp -- "$SCRIPT_DIR/sources.lock" "$topdir/SOURCES/sources.lock"
cp -- "$SCRIPT_DIR/servicectl-stack.spec" "$topdir/SPECS/servicectl-stack.spec"

rpmbuild -bs \
  --define "_topdir $topdir" \
  --define "servicectl_version $VERSION" \
  --define "stack_release $RELEASE" \
  "$topdir/SPECS/servicectl-stack.spec"

srpm=$(find "$topdir/SRPMS" -maxdepth 1 -type f -name '*.src.rpm' -print -quit)
[[ -n "$srpm" ]] || {
  printf '%s\n' 'rpmbuild completed without producing an SRPM' >&2
  exit 1
}
destination="$OUTPUT_DIR/${srpm##*/}"
cp -f -- "$srpm" "$destination"
printf '%s\n' "$destination"
