#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly repository_root

if [[ $# -ne 2 ]]; then
	echo "usage: scripts/generate-protobuf.sh <ensure|generate|verify> <go|ts|all>" >&2
	exit 2
fi

(
	cd "${repository_root}/tools/protobuf"
	go run ./cmd/protogen "$1" "$2"
)
