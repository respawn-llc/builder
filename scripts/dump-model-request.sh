#!/usr/bin/env bash
# dump-model-request.sh — capture the exact model-request payload for a Kent session.
#
# Builds and runs cmd/dumpmodelrequest, which resolves a session by ID, reconstructs
# the production request-prep path, and writes the literal OpenAI-compatible wire
# payload (plus the provider-agnostic llm.Request) to a JSON file. No proxy, mock,
# approximation, or live OpenAI request is involved.
#
# Usage:
#   ./scripts/dump-model-request.sh -session <id> [options]
#
# Options (forwarded to dumpmodelrequest):
#   -session <id>                 required; the session ID to capture
#   -persistence-root <path>      config/data root (overrides KENT_PERSISTENCE_ROOT and ~/.kent)
#   -provider <id>                openai | openai-compatible | chatgpt-codex
#   -output <path>                output JSON path (defaults to ./kent-<sessionID>-<unix>.json)
#   -no-tools                     build the request without tool definitions
#
# Prints the output file path to stdout.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: $0 -session <id> [-persistence-root <path>] [-provider <id>] [-output <path>] [-no-tools]" >&2
  exit 2
fi

# Resolve the repository root so the script works from any cwd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

go run ./cmd/dumpmodelrequest "$@"
