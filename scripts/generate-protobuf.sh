#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly repository_root

if [[ $# -lt 2 ]]; then
	echo "usage: scripts/generate-protobuf.sh <ensure|generate|verify|clean> <go|ts|all>" >&2
	echo "       scripts/generate-protobuf.sh run <go|ts|all> -- <command> [argument ...]" >&2
	exit 2
fi

run_directory="$PWD"
run_goos="${GOOS-}"
run_goarch="${GOARCH-}"
(
	cd "${repository_root}/tools/protobuf"
	unset GOOS GOARCH
	KENT_PROTOBUF_RUN_DIR="${run_directory}" \
		KENT_PROTOBUF_RUN_GOOS="${run_goos}" \
		KENT_PROTOBUF_RUN_GOARCH="${run_goarch}" \
		go run ./cmd/protogen "$@"
)
