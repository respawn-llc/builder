#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

client=""
server="./bin/kent"
scenario="internal/testharness/pty/blackbox/testdata/go-model-boundary.json"
profile="go"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--client)
		client="$2"
		shift 2
		;;
	--server)
		server="$2"
		shift 2
		;;
	--scenario)
		scenario="$2"
		shift 2
		;;
	--profile)
		profile="$2"
		shift 2
		;;
	-h|--help)
		echo "Usage: scripts/tui-qa.sh --client /path/to/client [--server ./bin/kent] [--scenario path] [--profile go]" >&2
		exit 0
		;;
	*)
		echo "Unknown argument: $1" >&2
		exit 2
		;;
	esac
done

if [[ -z "$client" ]]; then
	echo "--client is required" >&2
	exit 2
fi
if [[ ! -x "$server" ]]; then
	echo "server binary is not executable: $server" >&2
	exit 2
fi

go run ./internal/testharness/pty/blackbox/cmd/tuiqa \
	--client "$client" \
	--server "$server" \
	--scenario "$scenario" \
	--profile "$profile"
