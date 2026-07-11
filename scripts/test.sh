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
            Disable the script-level server test cap while keeping Go's own package timeouts.
  --inherit-env
            Do not sanitize KENT_* environment variables before running tests.

Environment:
  KENT_TEST_TIMEOUT_SECONDS
            Server test wall-clock cap in seconds. Defaults to 120.
  KENT_TEST_GO_PACKAGE_PARALLELISM
            Maximum Go test packages to execute concurrently. Defaults to 8.
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
        server|tui|desktop)
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

if [ "$inherit_env" != "1" ]; then
    while IFS= read -r name; do
        case "$name" in
            KENT_SKIP_FRONTEND|KENT_TEST_DISABLE_WALL_CLOCK_CAP|KENT_TEST_FRONTEND|KENT_TEST_GO_PACKAGE_PARALLELISM|KENT_TEST_INHERIT_ENV|KENT_TEST_INSIDE_TUI_WALL_CLOCK_CAP|KENT_TEST_TIMEOUT_SECONDS|KENT_TEST_TUI_TIMEOUT_SECONDS)
                ;;
            KENT_*)
                unset "$name"
                ;;
        esac
    done < <(compgen -e KENT_ || true)
fi

case "$disable_wall_clock_cap" in
    0|1)
        ;;
    *)
        printf 'KENT_TEST_DISABLE_WALL_CLOCK_CAP must be 0 or 1\n' >&2
        exit 2
        ;;
esac

timeout_seconds="${KENT_TEST_TIMEOUT_SECONDS:-120}"
go_test_package_parallelism="${KENT_TEST_GO_PACKAGE_PARALLELISM:-8}"
tui_timeout_seconds="${KENT_TEST_TUI_TIMEOUT_SECONDS:-600}"
inside_tui_wall_clock_cap="${KENT_TEST_INSIDE_TUI_WALL_CLOCK_CAP:-0}"
case "$go_test_package_parallelism" in
    ''|*[!0-9]*)
        printf 'KENT_TEST_GO_PACKAGE_PARALLELISM must be a positive integer\n' >&2
        exit 2
        ;;
esac
if [ "$go_test_package_parallelism" -le 0 ]; then
    printf 'KENT_TEST_GO_PACKAGE_PARALLELISM must be a positive integer\n' >&2
    exit 2
fi
server_go_test_args=(-p "$go_test_package_parallelism" "${server_test_args[@]}")
if [ "$disable_wall_clock_cap" != "1" ]; then
    case "$timeout_seconds" in
        ''|*[!0-9]*)
            printf 'KENT_TEST_TIMEOUT_SECONDS must be a positive integer <= 120\n' >&2
            exit 2
            ;;
    esac
    if [ "$timeout_seconds" -le 0 ] || [ "$timeout_seconds" -gt 120 ]; then
        printf 'KENT_TEST_TIMEOUT_SECONDS must be a positive integer <= 120\n' >&2
        exit 2
    fi
    case "$tui_timeout_seconds" in
        ''|*[!0-9]*)
            printf 'KENT_TEST_TUI_TIMEOUT_SECONDS must be a positive integer <= 1800\n' >&2
            exit 2
            ;;
    esac
    if [ "$tui_timeout_seconds" -le 0 ] || [ "$tui_timeout_seconds" -gt 1800 ]; then
        printf 'KENT_TEST_TUI_TIMEOUT_SECONDS must be a positive integer <= 1800\n' >&2
        exit 2
    fi
fi

go_log_file="$(mktemp -t kent-go-test.XXXXXX.log)"
tui_log_file="$(mktemp -t kent-tui-test.XXXXXX.log)"
frontend_log_file="$(mktemp -t kent-frontend-test.XXXXXX.log)"
test_pid=""
cleanup() {
    rm -f "$go_log_file" "$tui_log_file" "$frontend_log_file"
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
        if [ "$disable_wall_clock_cap" != "1" ]; then
            require_command python3 "enforce the server test wall-clock timeout"
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
        -p client-contracts \
        -p rpc-client
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
        python3 - "$tui_log_file" "$repo_root/scripts/test.sh" <<'PY' &
import os
import sys

log_file = sys.argv[1]
script = sys.argv[2]
os.setsid()
fd = os.open(log_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
try:
    os.dup2(fd, 1)
    os.dup2(fd, 2)
finally:
    os.close(fd)
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
        cat "$tui_log_file"
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
    if pnpm --dir apps install --frozen-lockfile >"$frontend_log_file" 2>&1 &&
        pnpm --dir apps test >>"$frontend_log_file" 2>&1; then
        return
    fi
    cat "$frontend_log_file"
    exit 1
}

run_server_tests() {
    if [ "$disable_wall_clock_cap" = "1" ]; then
        set +e
        go test "${server_go_test_args[@]}" >"$go_log_file" 2>&1
        status=$?
        set -e
        if [ "$status" -eq 0 ]; then
            return
        fi
        cat "$go_log_file"
        exit "$status"
    fi

    set +e
    python3 - "$go_log_file" "${server_go_test_args[@]}" <<'PY' &
import os
import sys

log_file = sys.argv[1]
test_args = sys.argv[2:]
os.setsid()
fd = os.open(log_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
try:
    os.dup2(fd, 1)
    os.dup2(fd, 2)
finally:
    os.close(fd)
os.execvp("go", ["go", "test", *test_args])
PY
    test_pid=$!
    timed_out=0
    deadline=$((SECONDS + timeout_seconds))

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
        printf 'test suite exceeded %ds wall-clock cap; simplify or speed up tests before continuing\n' "$timeout_seconds"
    elif [ "$status" -eq 143 ] || [ "$status" -eq 137 ]; then
        printf 'test process was terminated by a signal (exit status %d)\n' "$status"
    fi
    cat "$go_log_file"
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
