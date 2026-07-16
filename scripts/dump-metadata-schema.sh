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

go run ./cmd/dumpmetadataschema "$@"
