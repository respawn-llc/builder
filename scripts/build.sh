#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

output="./bin/kent"
version="${KENT_VERSION:-}"
package="./cli/kent"

[ "${1:-}" != "server" ] || shift
while [ "$#" -gt 0 ]; do
	case "$1" in
	--output)
		[ "$#" -ge 2 ] || { echo "--output requires a path" >&2; exit 2; }
		output="$2"
		shift 2
		;;
	--version)
		[ "$#" -ge 2 ] || { echo "--version requires a value" >&2; exit 2; }
		version="$2"
		shift 2
		;;
	--package)
		[ "$#" -ge 2 ] || { echo "--package requires a path" >&2; exit 2; }
		package="$2"
		shift 2
		;;
	*)
		echo "Unknown build argument: $1" >&2
		exit 2
		;;
	esac
done

[ -n "$version" ] || version="$(tr -d '[:space:]' < VERSION)"
version="${version#v}"
mkdir -p "$(dirname "$output")"

CGO_ENABLED="${CGO_ENABLED:-0}" go build \
	-trimpath \
	-buildvcs=false \
	-ldflags "-s -w -X core/shared/config.Version=${version}" \
	-o "$output" \
	"$package"
