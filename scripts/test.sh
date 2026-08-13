#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

usage() {
    cat <<'USAGE'
Usage: scripts/test.sh [target ...] [options] [go-test-args ...]

Runs Kent repository test targets.

Targets:
  server   Run Go tests.
  tui      Run frozen Rust TUI checks and tests only when explicitly requested.
  desktop  Run desktop frontend tests.

Any other argument is forwarded to `go test` (packages, -run, -v, ...) and
implies the server target unless targets are named explicitly.

Options:
  --full
            Run fresh repository-wide server and desktop tests. Disables Go's
            test-result cache. Intended for final verification and CI only.
  --no-wall-clock-cap
            Disable the script-level server test-runtime cap while keeping Go's own package timeouts.
  --inherit-env
            Do not sanitize KENT_* environment variables before running tests.

Environment:
  KENT_TEST_TIMEOUT_SECONDS
            Server test-runtime cap in seconds. Defaults to 300.
  KENT_TEST_GO_PACKAGE_PARALLELISM
            Maximum Go test packages to execute concurrently. Defaults to the detected CPU count,
            capped at 10, and falls back to 4 when CPU detection is unavailable.
  KENT_TEST_BASE_REF
            Git ref used as the affected-test comparison base. Defaults to origin's HEAD branch,
            then main/master fallbacks.
  KENT_TEST_TUI_TIMEOUT_SECONDS
            TUI test wall-clock cap in seconds. Defaults to 600.
  -h, --help
            Show this help.

Examples:
  scripts/test.sh
  scripts/test.sh --full
  scripts/test.sh server
  scripts/test.sh tui
  scripts/test.sh ./server/session -run TestResume
USAGE
}

inherit_env=0
full=0
affected=0
disable_wall_clock_cap="${KENT_TEST_DISABLE_WALL_CLOCK_CAP:-0}"
targets=()
go_test_args=()

while [[ $# -gt 0 ]]; do
    case "$1" in
    server | tui | desktop)
        targets+=("$1")
        shift
        ;;
    --full)
        full=1
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
    elif [ "$full" = "1" ]; then
        targets=(server desktop)
    else
        affected=1
    fi
fi

server_test_args=(./...)
if [ "${#go_test_args[@]}" -gt 0 ]; then
    server_test_args=("${go_test_args[@]}")
fi

if [ "$inherit_env" != "1" ]; then
    while IFS= read -r name; do
        case "$name" in
        KENT_SKIP_FRONTEND | KENT_TEST_DISABLE_WALL_CLOCK_CAP | KENT_TEST_FRONTEND | KENT_TEST_GO_PACKAGE_PARALLELISM | KENT_TEST_INHERIT_ENV | KENT_TEST_INSIDE_TUI_WALL_CLOCK_CAP | KENT_TEST_PTY_CACHE_ROOT | KENT_TEST_TIMEOUT_SECONDS | KENT_TEST_TUI_TIMEOUT_SECONDS)
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

case "$full" in
0 | 1)
    ;;
*)
    printf 'full test selection must be 0 or 1\n' >&2
    exit 2
    ;;
esac

if [ "$affected" = "1" ]; then
    affected_server_packages=()
    affected_desktop=0
    affected_full_server=0
    affected_output="$(go run ./tools/testaffected)"
    while IFS=$'\t' read -r kind value; do
        case "$kind" in
        server-package)
            affected_server_packages+=("$value")
            ;;
        full-server)
            affected_full_server=1
            ;;
        desktop)
            affected_desktop=1
            ;;
        none)
            ;;
        *)
            printf 'unexpected affected-test selection: %s\n' "$kind" >&2
            exit 2
            ;;
        esac
    done <<<"$affected_output"
    if [ "$affected_full_server" = "1" ]; then
        targets+=(server)
    elif [ "${#affected_server_packages[@]}" -gt 0 ]; then
        targets+=(server)
        server_test_args=("${affected_server_packages[@]}")
    fi
    if [ "$affected_desktop" = "1" ]; then
        targets+=(desktop)
    fi
    if [ "${#targets[@]}" -eq 0 ]; then
        printf 'no tests affected\n'
        exit 0
    fi
fi

timeout_seconds="${KENT_TEST_TIMEOUT_SECONDS:-300}"
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
if [ "${#server_test_args[@]}" -eq 1 ] && [ "${server_test_args[0]}" = "./..." ]; then
    server_test_command=(
        go run ./tools/testshard
        --workers "$go_test_package_parallelism"
    )
    if [ "$full" = "1" ]; then
        server_test_command+=(--fresh)
    fi
else
    server_test_command=(
        go test -json
        -p "$go_test_package_parallelism"
    )
    if [ "$full" = "1" ]; then
        server_test_command+=(-count=1)
    fi
    server_test_command+=("${server_test_args[@]}")
fi
if [ "$disable_wall_clock_cap" != "1" ]; then
    case "$timeout_seconds" in
    '' | *[!0-9]*)
        printf 'KENT_TEST_TIMEOUT_SECONDS must be a positive integer <= 300\n' >&2
        exit 2
        ;;
    esac
    if [ "$timeout_seconds" -le 0 ] || [ "$timeout_seconds" -gt 300 ]; then
        printf 'KENT_TEST_TIMEOUT_SECONDS must be a positive integer <= 300\n' >&2
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

test_pid=""
pty_fixture_lease_dir=""

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

cleanup() {
    if [ -n "$pty_fixture_lease_dir" ]; then
        rmdir "$pty_fixture_lease_dir" 2>/dev/null || true
    fi
}

trap handle_interrupt INT
trap handle_term TERM
trap cleanup EXIT

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
        if [ "$disable_wall_clock_cap" != "1" ]; then
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
    ./scripts/generate-protobuf.sh run ts -- pnpm --dir apps test
}

server_tests_need_pty_fixtures() {
    local argument
    for argument in "${server_test_args[@]}"; do
        case "$argument" in
        ./... | core/cli/app | ./cli/app | core/server/transport | ./server/transport)
            return 0
            ;;
        esac
    done
    return 1
}

pty_fixture_cache_key() {
    {
        go version
        go env GOOS GOARCH CGO_ENABLED
        go list -test -deps -export -f '{{.ImportPath}}	{{.BuildID}}' \
            core/cli/app \
            core/cli/kent \
            core/internal/testharness/pty/testdata/cmd/ansi-writer \
            core/internal/testharness/pty/testdata/cmd/phase-input-writer \
            core/internal/testharness/pty/testdata/cmd/phase-writer
        cat VERSION 2>/dev/null || true
    } | shasum -a 256 | awk '{print $1}'
}

prune_pty_fixture_cache() {
    local cache_root="$1"
    local current="$2"
    local kept=0
    local path
    while IFS= read -r path; do
        if [ "$path" = "$current" ]; then
            continue
        fi
        if find "$cache_root" -mindepth 1 -maxdepth 1 -type d -name "$(basename "$path").lease.*" -print -quit | grep -q .; then
            continue
        fi
        kept=$((kept + 1))
        if [ "$kept" -le 7 ]; then
            continue
        fi
        find "$path" -depth -delete >/dev/null 2>&1 || true
    done < <(
        find "$cache_root" -mindepth 1 -maxdepth 1 -type d ! -name '*.lock' ! -name '*.lease.*' -print0 2>/dev/null |
            xargs -0 stat -f '%m %N' 2>/dev/null |
            sort -nr |
            sed 's/^[0-9]* //'
    )
}

prepare_pty_fixtures() {
    local cache_root="${KENT_TEST_PTY_CACHE_ROOT:-${XDG_CACHE_HOME:-$HOME/Library/Caches}/kent/test-fixtures}"
    local cache_key
    local cache_dir
    local lock_dir
    local waited=0
    cache_key="$(pty_fixture_cache_key)"
    cache_dir="$cache_root/$cache_key"
    lock_dir="$cache_dir.lock"
    mkdir -p "$cache_root"
    pty_fixture_lease_dir="$cache_root/$cache_key.lease.$$"
    if ! mkdir "$pty_fixture_lease_dir"; then
        printf 'create PTY fixture cache lease %s\n' "$pty_fixture_lease_dir" >&2
        return 1
    fi

    while [ ! -f "$cache_dir/complete" ] && ! mkdir "$lock_dir" 2>/dev/null; do
        if [ "$waited" -ge 300 ]; then
            printf 'timed out waiting for PTY fixture cache %s\n' "$cache_key" >&2
            return 1
        fi
        sleep 1
        waited=$((waited + 1))
    done

    if [ ! -f "$cache_dir/complete" ]; then
        find "$cache_dir" -depth -delete >/dev/null 2>&1 || true
        mkdir -p "$cache_dir"
        if ! go test -c -o "$cache_dir/kent-pty-fixture.test" core/cli/app ||
            ! ./scripts/build.sh server --output "$cache_dir/kent" ||
            ! go build -o "$cache_dir/ansi-writer" core/internal/testharness/pty/testdata/cmd/ansi-writer ||
            ! go build -o "$cache_dir/phase-input-writer" core/internal/testharness/pty/testdata/cmd/phase-input-writer ||
            ! go build -o "$cache_dir/phase-writer" core/internal/testharness/pty/testdata/cmd/phase-writer; then
            find "$cache_dir" -depth -delete >/dev/null 2>&1 || true
            rmdir "$lock_dir" 2>/dev/null || true
            return 1
        fi
        : >"$cache_dir/complete"
        rmdir "$lock_dir"
        prune_pty_fixture_cache "$cache_root" "$cache_dir"
    fi

    export KENT_PTY_FIXTURE_BINARY="$cache_dir/kent-pty-fixture.test"
    export KENT_PTY_KENT_BINARY="$cache_dir/kent"
    export KENT_PTY_ANSI_WRITER_BINARY="$cache_dir/ansi-writer"
    export KENT_PTY_PHASE_INPUT_WRITER_BINARY="$cache_dir/phase-input-writer"
    export KENT_PTY_PHASE_WRITER_BINARY="$cache_dir/phase-writer"
}

run_server_tests() {
    if server_tests_need_pty_fixtures; then
        prepare_pty_fixtures
    fi

    if [ "$disable_wall_clock_cap" = "1" ]; then
        set +e
        ./scripts/generate-protobuf.sh run go -- "${server_test_command[@]}"
        status=$?
        set -e
        if [ "$status" -eq 0 ]; then
            return
        fi
        exit "$status"
    fi

    set +e
    python3 - "$timeout_seconds" ./scripts/generate-protobuf.sh run go -- "${server_test_command[@]}" <<'PY' &
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
    if action in {"run", "cont"}:
        running_tests.add(test_id)
    elif action in {"pause", "pass", "fail", "skip"}:
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
