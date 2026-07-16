#!/usr/bin/env bash
# dump-metadata-schema.sh — print the latest effective Kent metadata SQLite DDL.
#
# Usage:
#   ./scripts/dump-metadata-schema.sh
#
# The command migrates an isolated temporary database through the application's
# authoritative metadata path and writes executable schema-only SQL to stdout.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# Execute a built binary so command exit statuses pass through unchanged.
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/kent-dump-metadata-schema.XXXXXX")"
cleanup() {
  local command_status=$?
  trap - EXIT
  if ! find "$BUILD_DIR" -depth -delete; then
    printf 'dump-metadata-schema.sh: remove temporary build directory: %s\n' "$BUILD_DIR" >&2
    if [[ $command_status -eq 0 ]]; then
      command_status=1
    fi
  fi
  exit "$command_status"
}
trap cleanup EXIT

go build -o "$BUILD_DIR/dumpmetadataschema" ./cmd/dumpmetadataschema
"$BUILD_DIR/dumpmetadataschema" "$@"
