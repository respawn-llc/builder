#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

case "$(uname -s)" in
MINGW* | MSYS* | CYGWIN*)
	echo "Local test tooling has not been built for Windows." >&2
	exit 2
	;;
esac

usage() {
	cat <<'USAGE'
Usage: just test [server|desktop|tui|rust] [--timeout SECONDS] [--workers COUNT] [--no-wall-clock-cap] [--inherit-env] [-- TOOL-ARGS...]

The timeout covers the complete command, including every selected test process.
Bare `just test` runs server and desktop tests under one timeout.
USAGE
}

target="default"
timeout_seconds=300
workers=""
wall_clock_cap=1
inherit_env=0
tool_args=()

while [ "$#" -gt 0 ]; do
	case "$1" in
	server | desktop | tui | rust)
		[ "$target" = "default" ] || { echo "Select one test target" >&2; exit 2; }
		target="$1"
		shift
		;;
	--timeout)
		[ "$#" -ge 2 ] || { echo "--timeout requires seconds" >&2; exit 2; }
		timeout_seconds="$2"
		shift 2
		;;
	--workers)
		[ "$#" -ge 2 ] || { echo "--workers requires a count" >&2; exit 2; }
		workers="$2"
		shift 2
		;;
	--no-wall-clock-cap)
		wall_clock_cap=0
		shift
		;;
	--inherit-env)
		inherit_env=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	--)
		shift
		tool_args=("$@")
		break
		;;
	*)
		echo "Unknown test argument: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

case "$timeout_seconds" in
'' | *[!0-9]*) echo "--timeout must be a positive integer" >&2; exit 2 ;;
esac
[ "$timeout_seconds" -gt 0 ] || { echo "--timeout must be a positive integer" >&2; exit 2; }

if [ -z "$workers" ]; then
	workers=4
	if command -v nproc >/dev/null 2>&1; then
		workers="$(nproc)"
	elif command -v sysctl >/dev/null 2>&1; then
		workers="$(sysctl -n hw.ncpu 2>/dev/null || echo 4)"
	fi
	[ "$workers" -le 10 ] || workers=10
fi
case "$workers" in
'' | *[!0-9]*) echo "--workers must be a positive integer" >&2; exit 2 ;;
esac
[ "$workers" -gt 0 ] || { echo "--workers must be a positive integer" >&2; exit 2; }

if [ "$inherit_env" -eq 0 ]; then
	while IFS= read -r name; do
		case "$name" in
		KENT_*) unset "$name" ;;
		esac
	done < <(compgen -e KENT_ || true)
fi

[ "$target" != "default" ] || [ "${#tool_args[@]}" -eq 0 ] || {
	echo "Tool arguments require an explicit test target" >&2
	exit 2
}

case "$target" in
default | desktop) just _node ;;
esac

started="$SECONDS"
failure_labels=()
failure_logs=()
failure_statuses=()
logs=()

cleanup() {
	local log
	for log in "${logs[@]}"; do
		unlink "$log" 2>/dev/null || true
	done
}
trap cleanup EXIT

run_suite() {
	local label="$1"
	shift
	local suite_started="$SECONDS"
	local log remaining status
	log="$(mktemp -t "kent-${label}-tests.XXXXXX.log")"
	logs+=("$log")

	if [ "$wall_clock_cap" -eq 1 ]; then
		remaining=$((timeout_seconds - (SECONDS - started)))
		if [ "$remaining" -le 0 ]; then
			status=124
		else
			set +e
			python3 - "$remaining" "$log" "$@" <<'PY'
import os
import signal
import subprocess
import sys

timeout = int(sys.argv[1])
with open(sys.argv[2], "wb") as output:
    process = None

    def terminate(signum):
        if process is not None and process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        raise SystemExit(128 + signum)

    signal.signal(signal.SIGINT, lambda signum, _frame: terminate(signum))
    signal.signal(signal.SIGTERM, lambda signum, _frame: terminate(signum))
    try:
        process = subprocess.Popen(
            sys.argv[3:],
            stdout=output,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    except OSError as error:
        print(error, file=output)
        raise SystemExit(127)
    try:
        raise SystemExit(process.wait(timeout=timeout))
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
        raise SystemExit(124)
PY
			status=$?
			set -e
		fi
	else
		set +e
		"$@" >"$log" 2>&1
		status=$?
		set -e
	fi

	if [ "$status" -eq 0 ]; then
		printf '✓ %s tests passed in %ds\n' "$label" "$((SECONDS - suite_started))"
		return
	fi
	failure_labels+=("$label")
	failure_logs+=("$log")
	failure_statuses+=("$status")
}

run_server() {
	local args=("${tool_args[@]}")
	[ "${#args[@]}" -gt 0 ] || args=(./...)
	run_suite server go test -p "$workers" -timeout "${timeout_seconds}s" "${args[@]}"
}

run_desktop() {
	run_suite desktop pnpm --dir apps/desktop exec vitest --config tooling/vite.config.ts run "${tool_args[@]}"
}

run_tui() {
	run_suite tui go test -p "$workers" -timeout "${timeout_seconds}s" ./cli/tui/... "${tool_args[@]}"
}

run_rust() {
	run_suite rust cargo test --manifest-path tui-rs/Cargo.toml --workspace --locked "${tool_args[@]}"
}

case "$target" in
default)
	run_server
	run_desktop
	;;
server) run_server ;;
desktop) run_desktop ;;
tui) run_tui ;;
rust) run_rust ;;
esac

if [ "${#failure_labels[@]}" -eq 0 ]; then
	[ "$target" != "default" ] || printf '✓ all tests passed in %ds\n' "$((SECONDS - started))"
	exit 0
fi

for index in "${!failure_labels[@]}"; do
	label="${failure_labels[$index]}"
	status="${failure_statuses[$index]}"
	printf '\n✗ %s tests %s in %ds\n' \
		"$label" \
		"$([ "$status" -eq 124 ] && printf 'timed out' || printf 'failed')" \
		"$((SECONDS - started))" >&2
	printf '──── %s test output ────\n' "$label" >&2
	cat "${failure_logs[$index]}" >&2
	printf '──── end %s test output ────\n' "$label" >&2
done

printf '\n✗ %d test runner(s) failed in %ds\n' "${#failure_labels[@]}" "$((SECONDS - started))" >&2
exit 1
