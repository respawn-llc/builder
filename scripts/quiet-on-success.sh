#!/usr/bin/env bash

set -euo pipefail

success_output="ok"
if [ "${1:-}" = "--success" ]; then
	[ "$#" -ge 3 ] || {
		echo "--success requires a value and command" >&2
		exit 2
	}
	success_output="$2"
	shift 2
fi

if [ -n "${JUST_VERBOSE:-}" ]; then
	exec "$@"
fi

log="$(mktemp -t kent-command.XXXXXX)"
trap 'unlink "$log" 2>/dev/null || true' EXIT

set +e
"$@" >"$log" 2>&1
exit_code=$?
set -e

if [ "$exit_code" -eq 0 ]; then
	printf '%s\n' "$success_output"
	exit 0
fi

cat "$log" >&2
exit "$exit_code"
