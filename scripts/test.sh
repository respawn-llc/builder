#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

usage() {
    cat <<'USAGE'
Usage: scripts/test.sh [target ...] [options] [go-test-args ...]

Runs Kent repository test targets.

Targets:
  server   Run Go tests. Selected by default.
  tui      Run Rust TUI checks and tests. Selected by default.
  desktop  Run desktop frontend tests. Selected by default.

Any other argument is forwarded to `go test` (packages, -run, -v, ...) and
implies the server target unless targets are named explicitly.

Options:
  --no-wall-clock-cap
            Disable the script-level server test-runtime cap while keeping Go's own package timeouts.
  --inherit-env
            Do not sanitize KENT_* environment variables before running tests.

Environment:
  KENT_TEST_TIMEOUT_SECONDS
            Server test-runtime cap in seconds. Defaults to 180.
  KENT_TEST_GO_PACKAGE_PARALLELISM
            Maximum Go test packages to execute concurrently. Defaults to the detected CPU count,
            capped at 10, and falls back to 4 when CPU detection is unavailable.
  KENT_TEST_TUI_TIMEOUT_SECONDS
            TUI test wall-clock cap in seconds. Defaults to 600.
  -h, --help
            Show this help.

Examples:
  scripts/test.sh
  scripts/test.sh server
  scripts/test.sh tui desktop
  scripts/test.sh ./server/session -run TestResume
USAGE
}

inherit_env=0
disable_wall_clock_cap="${KENT_TEST_DISABLE_WALL_CLOCK_CAP:-0}"
targets=()
go_test_args=()

while [[ $# -gt 0 ]]; do
    case "$1" in
    server | tui | desktop)
        targets+=("$1")
        shift
        ;;
    --no-wall-clock-cap)
        disable_wall_clock_cap=1
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
        while [ $# -gt 0 ]; do
            go_test_args+=("$1")
            shift
        done
        break
        ;;
    *)
        go_test_args+=("$1")
        shift
        ;;
    esac
done

if [ "${#targets[@]}" -eq 0 ]; then
    if [ "${#go_test_args[@]}" -gt 0 ]; then
        targets=(server)
    else
        targets=(server tui desktop)
    fi
fi

server_test_args=(./...)
if [ "${#go_test_args[@]}" -gt 0 ]; then
    server_test_args=("${go_test_args[@]}")
fi

server_test_requires_runtime_admission=0
for server_test_arg in "${server_test_args[@]}"; do
    if [[ "$server_test_arg" == -* ]] || ! command -v go >/dev/null 2>&1; then
        continue
    fi
    while IFS= read -r resolved_import_path; do
        if [ "$resolved_import_path" = "core/server/runtime" ]; then
            server_test_requires_runtime_admission=1
            break
        fi
    done < <(go list -f '{{.ImportPath}}' "$server_test_arg" 2>/dev/null || true)
    if [ "$server_test_requires_runtime_admission" = "1" ]; then
        break
    fi
done

if [ "$inherit_env" != "1" ]; then
    while IFS= read -r name; do
        case "$name" in
        KENT_SKIP_FRONTEND | KENT_TEST_DISABLE_WALL_CLOCK_CAP | KENT_TEST_FRONTEND | KENT_TEST_GO_PACKAGE_PARALLELISM | KENT_TEST_INHERIT_ENV | KENT_TEST_INSIDE_TUI_WALL_CLOCK_CAP | KENT_TEST_TIMEOUT_SECONDS | KENT_TEST_TUI_TIMEOUT_SECONDS)
            ;;
        KENT_*)
            unset "$name"
            ;;
        esac
    done < <(compgen -e KENT_ || true)
fi

case "$disable_wall_clock_cap" in
0 | 1)
    ;;
*)
    printf 'KENT_TEST_DISABLE_WALL_CLOCK_CAP must be 0 or 1\n' >&2
    exit 2
    ;;
esac

timeout_seconds="${KENT_TEST_TIMEOUT_SECONDS:-180}"
default_go_test_package_parallelism=4
if command -v nproc >/dev/null 2>&1; then
    default_go_test_package_parallelism="$(nproc)"
elif command -v sysctl >/dev/null 2>&1; then
    default_go_test_package_parallelism="$(sysctl -n hw.ncpu 2>/dev/null || true)"
fi
case "$default_go_test_package_parallelism" in
'' | *[!0-9]*)
    default_go_test_package_parallelism=4
    ;;
esac
if [ "$default_go_test_package_parallelism" -gt 10 ]; then
    default_go_test_package_parallelism=10
fi
go_test_package_parallelism="${KENT_TEST_GO_PACKAGE_PARALLELISM:-$default_go_test_package_parallelism}"
tui_timeout_seconds="${KENT_TEST_TUI_TIMEOUT_SECONDS:-600}"
inside_tui_wall_clock_cap="${KENT_TEST_INSIDE_TUI_WALL_CLOCK_CAP:-0}"
case "$go_test_package_parallelism" in
'' | *[!0-9]*)
    printf 'KENT_TEST_GO_PACKAGE_PARALLELISM must be a positive integer\n' >&2
    exit 2
    ;;
esac
if [ "$go_test_package_parallelism" -le 0 ]; then
    printf 'KENT_TEST_GO_PACKAGE_PARALLELISM must be a positive integer\n' >&2
    exit 2
fi
server_test_uses_sharder=0
if [ "${#server_test_args[@]}" -eq 1 ] && [ "${server_test_args[0]}" = "./..." ]; then
    server_test_uses_sharder=1
    server_test_command=(
        go run ./tools/testshard
        --workers "$go_test_package_parallelism"
    )
else
    server_test_command=(
        go test -json
        -count=1
        -p "$go_test_package_parallelism"
        "${server_test_args[@]}"
    )
fi
if [ "$disable_wall_clock_cap" != "1" ]; then
    case "$timeout_seconds" in
    '' | *[!0-9]*)
        printf 'KENT_TEST_TIMEOUT_SECONDS must be a positive integer <= 180\n' >&2
        exit 2
        ;;
    esac
    if [ "$timeout_seconds" -le 0 ] || [ "$timeout_seconds" -gt 180 ]; then
        printf 'KENT_TEST_TIMEOUT_SECONDS must be a positive integer <= 180\n' >&2
        exit 2
    fi
    case "$tui_timeout_seconds" in
    '' | *[!0-9]*)
        printf 'KENT_TEST_TUI_TIMEOUT_SECONDS must be a positive integer <= 1800\n' >&2
        exit 2
        ;;
    esac
    if [ "$tui_timeout_seconds" -le 0 ] || [ "$tui_timeout_seconds" -gt 1800 ]; then
        printf 'KENT_TEST_TUI_TIMEOUT_SECONDS must be a positive integer <= 1800\n' >&2
        exit 2
    fi
fi

pty_fixture_build_dir=""
test_pid=""
cleanup() {
    if [ -n "$pty_fixture_build_dir" ]; then
        unlink "$pty_fixture_build_dir/kent-pty-fixture.test" 2>/dev/null || true
        unlink "$pty_fixture_build_dir/kent" 2>/dev/null || true
        unlink "$pty_fixture_build_dir/ansi-writer" 2>/dev/null || true
        unlink "$pty_fixture_build_dir/phase-input-writer" 2>/dev/null || true
        unlink "$pty_fixture_build_dir/phase-writer" 2>/dev/null || true
        rmdir "$pty_fixture_build_dir" 2>/dev/null || true
    fi
}
trap cleanup EXIT

terminate_test_process_group() {
    if [ -z "${test_pid:-}" ] || ! kill -0 "$test_pid" 2>/dev/null; then
        return
    fi
    kill -TERM "-$test_pid" 2>/dev/null || kill -TERM "$test_pid" 2>/dev/null || true
    sleep 2
    kill -KILL "-$test_pid" 2>/dev/null || kill -KILL "$test_pid" 2>/dev/null || true
}

handle_interrupt() {
    terminate_test_process_group
    exit 130
}

handle_term() {
    terminate_test_process_group
    exit 143
}

trap handle_interrupt INT
trap handle_term TERM

target_selected() {
    local expected="$1"
    local target
    for target in "${targets[@]}"; do
        if [ "$target" = "$expected" ]; then
            return 0
        fi
    done
    return 1
}

require_command() {
    local name="$1"
    local purpose="$2"
    if command -v "$name" >/dev/null 2>&1; then
        return
    fi
    printf '%s is required to %s.\n' "$name" "$purpose" >&2
    exit 2
}

check_dependencies() {
    if target_selected server; then
        require_command go "run server tests"
        if [ "$disable_wall_clock_cap" != "1" ] || [ "$server_test_requires_runtime_admission" = "1" ]; then
            require_command python3 "enforce the server test-runtime timeout"
        fi
    fi
    if target_selected tui && [ -f tui-rs/Cargo.toml ]; then
        require_command cargo "run Rust checks"
        require_command cargo-deny "run Rust dependency policy checks"
        if [ "$disable_wall_clock_cap" != "1" ] && [ "$inside_tui_wall_clock_cap" != "1" ]; then
            require_command python3 "enforce the TUI test wall-clock timeout"
        fi
    fi
    if target_selected desktop && [ -f apps/package.json ]; then
        require_command pnpm "run desktop tests"
    fi
}

run_rust_policy_checks() {
    if [ ! -f tui-rs/Cargo.toml ]; then
        return
    fi
    cargo run --manifest-path tui-rs/Cargo.toml --locked -p manifest-check -- check --repo-root "$repo_root"
}

run_rust_full_checks() {
    if [ ! -f tui-rs/Cargo.toml ]; then
        return
    fi
    cargo fmt --manifest-path tui-rs/Cargo.toml --all -- --check
    cargo test --manifest-path tui-rs/Cargo.toml --locked \
        --test integration \
        -p client-contracts
    cargo test --manifest-path tui-rs/Cargo.toml --locked \
        --test integration \
        -p manifest-check
    cargo metadata --manifest-path tui-rs/Cargo.toml --locked --format-version 1 >/dev/null
    cargo deny --manifest-path tui-rs/Cargo.toml check
}

run_tui_tests() {
    if [ ! -f tui-rs/Cargo.toml ]; then
        return
    fi
    if [ "$disable_wall_clock_cap" != "1" ] && [ "$inside_tui_wall_clock_cap" != "1" ]; then
        set +e
        python3 - "$repo_root/scripts/test.sh" <<'PY' &
import os
import sys

script = sys.argv[1]
os.setsid()
env = os.environ.copy()
env["KENT_TEST_INSIDE_TUI_WALL_CLOCK_CAP"] = "1"
os.execvpe("bash", ["bash", script, "tui", "--inherit-env"], env)
PY
        test_pid=$!
        timed_out=0
        deadline=$((SECONDS + tui_timeout_seconds))

        while kill -0 "$test_pid" 2>/dev/null; do
            if [ "$SECONDS" -ge "$deadline" ]; then
                timed_out=1
                terminate_test_process_group
                break
            fi
            sleep 1
        done

        set +e
        wait "$test_pid"
        status=$?
        set -e
        if [ "$status" -eq 0 ]; then
            return
        fi
        if [ "$timed_out" -eq 1 ]; then
            printf 'test suite exceeded %ds wall-clock cap; simplify or speed up tests before continuing\n' "$tui_timeout_seconds"
        elif [ "$status" -eq 143 ] || [ "$status" -eq 137 ]; then
            printf 'test process was terminated by a signal (exit status %d)\n' "$status"
        fi
        exit 1
    fi
    run_rust_policy_checks
    run_rust_full_checks
}

run_desktop_tests() {
    if [ "${KENT_SKIP_FRONTEND:-0}" = "1" ]; then
        return
    fi
    if [ ! -f apps/package.json ]; then
        return
    fi
    ./scripts/install-frontend-dependencies.sh
    pnpm --dir apps test
}

run_server_tests() {
    local pty_fixture_binary=""
    ./scripts/build_test.sh
    if [ "${#server_test_args[@]}" -eq 1 ] && [ "${server_test_args[0]}" = "./..." ]; then
        pty_fixture_build_dir="$(mktemp -d -t kent-pty-fixture.XXXXXX)"
        pty_fixture_binary="$pty_fixture_build_dir/kent-pty-fixture.test"
        go test -c -o "$pty_fixture_binary" core/cli/app
        ./scripts/build.sh server --output "$pty_fixture_build_dir/kent"
        go build -o "$pty_fixture_build_dir/ansi-writer" core/internal/testharness/pty/testdata/cmd/ansi-writer
        go build -o "$pty_fixture_build_dir/phase-input-writer" core/internal/testharness/pty/testdata/cmd/phase-input-writer
        go build -o "$pty_fixture_build_dir/phase-writer" core/internal/testharness/pty/testdata/cmd/phase-writer
        export KENT_PTY_FIXTURE_BINARY="$pty_fixture_binary"
        export KENT_PTY_KENT_BINARY="$pty_fixture_build_dir/kent"
        export KENT_PTY_ANSI_WRITER_BINARY="$pty_fixture_build_dir/ansi-writer"
        export KENT_PTY_PHASE_INPUT_WRITER_BINARY="$pty_fixture_build_dir/phase-input-writer"
        export KENT_PTY_PHASE_WRITER_BINARY="$pty_fixture_build_dir/phase-writer"
    fi

    # The sharder acquires one admission for its complete job graph when it
    # plans core/server/runtime. Wrapping it here would recurse through its
    # script-integration test, which invokes this script.
    if [ "$server_test_requires_runtime_admission" = "1" ] && [ "$server_test_uses_sharder" != "1" ]; then
        server_test_command=(python3 "$repo_root/scripts/runtime-test-lock.py" "${server_test_command[@]}")
    fi

    if [ "$disable_wall_clock_cap" = "1" ]; then
        set +e
        "${server_test_command[@]}"
        status=$?
        set -e
        if [ "$status" -eq 0 ]; then
            return
        fi
        exit "$status"
    fi

    set +e
    python3 - "$timeout_seconds" "${server_test_command[@]}" <<'PY' &
import json
import os
import selectors
import signal
import subprocess
import sys
import time

runtime_limit_seconds = float(sys.argv[1])
test_command = sys.argv[2:]
test_env = os.environ.copy()
process = subprocess.Popen(
    test_command,
    stdout=subprocess.PIPE,
    stderr=subprocess.STDOUT,
    bufsize=0,
    start_new_session=True,
    env=test_env,
)

def terminate_test_process():
    if process.poll() is not None:
        return
    os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()

def forward_signal(signum, _frame):
    terminate_test_process()
    raise SystemExit(128 + signum)

signal.signal(signal.SIGINT, forward_signal)
signal.signal(signal.SIGTERM, forward_signal)

selector = selectors.DefaultSelector()
selector.register(process.stdout, selectors.EVENT_READ)
os.set_blocking(process.stdout.fileno(), False)
running_tests = set()
test_runtime_seconds = 0.0
last_observation = time.monotonic()
timed_out = False
pending_output = b""

def emit(output):
    sys.stdout.write(output)
    sys.stdout.flush()

def observe_line(line):
    global running_tests
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        emit(line)
        return

    output = event.get("Output")
    if output is not None:
        emit(output)

    package = event.get("Package")
    action = event.get("Action")
    test = event.get("Test")
    if package is None or test is None:
        return
    test_id = (package, test)
    if action == "run":
        running_tests.add(test_id)
    elif action in {"pass", "fail", "skip"}:
        running_tests.discard(test_id)

def consume_output():
    global pending_output
    while True:
        try:
            chunk = os.read(process.stdout.fileno(), 65536)
        except BlockingIOError:
            return
        if not chunk:
            return
        pending_output += chunk
        while b"\n" in pending_output:
            raw_line, pending_output = pending_output.split(b"\n", 1)
            observe_line(raw_line.decode("utf-8", errors="replace") + "\n")

while process.poll() is None:
    now = time.monotonic()
    if running_tests:
        test_runtime_seconds += now - last_observation
    last_observation = now
    if test_runtime_seconds >= runtime_limit_seconds:
        timed_out = True
        terminate_test_process()
        break

    timeout = 0.1 if running_tests else 1.0
    for _, _ in selector.select(timeout):
        consume_output()

consume_output()
if pending_output:
    observe_line(pending_output.decode("utf-8", errors="replace"))

status = process.wait()
if timed_out:
    raise SystemExit(124)
if status < 0:
    raise SystemExit(128 - status)
raise SystemExit(status)
PY
    test_pid=$!
    wait "$test_pid"
    status=$?
    set -e
    if [ "$status" -eq 0 ]; then
        return
    fi
    if [ "$status" -eq 124 ]; then
        printf 'test runtime exceeded %ds cap; simplify or speed up tests before continuing\n' "$timeout_seconds"
    elif [ "$status" -eq 143 ] || [ "$status" -eq 137 ]; then
        printf 'test process was terminated by a signal (exit status %d)\n' "$status"
    fi
    exit 1
}

check_dependencies

if target_selected tui; then
    run_tui_tests
fi

if target_selected server; then
    run_server_tests
fi

if target_selected desktop; then
    run_desktop_tests
fi

printf 'pass\n'
