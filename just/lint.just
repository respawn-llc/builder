set working-directory := ".."

[parallel]
default mode="": (server mode) (desktop mode) (docs mode)

server mode="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ "{{mode}}" = "--dry-run" ]; then
        files="$(gofmt -l .)"
        [ -z "$files" ] || { echo "$files"; exit 1; }
    elif [ -z "{{mode}}" ]; then
        just _gen-go
        gofmt -w .
    else
        echo "Usage: just lint server [--dry-run]" >&2
        exit 2
    fi
    just _lint-protobuf
    go run ./cmd/architectureguard .
    go vet ./...

desktop mode="":
    #!/usr/bin/env bash
    set -euo pipefail
    just _node
    node apps/scripts/check-dependency-policy.mjs
    node apps/scripts/check-typescript-policy.mjs
    node --test apps/scripts/check-eslint-architecture.test.mjs
    if [ "{{mode}}" = "--dry-run" ]; then
        [ -d apps/desktop/packages/server-api-contract/src/gen ] || {
            echo "Generated TypeScript sources are required. Run 'just gen' first." >&2
            exit 2
        }
        pnpm --dir apps exec prettier --check .
        pnpm --dir apps/desktop exec eslint .
    elif [ -z "{{mode}}" ]; then
        just _gen-typescript
        pnpm --dir apps exec prettier --write .
        pnpm --dir apps/desktop exec eslint . --fix
    else
        echo "Usage: just lint desktop [--dry-run]" >&2
        exit 2
    fi
    node apps/desktop/node_modules/typescript7/bin/tsc -b apps/desktop/tsconfig.json --pretty false

tui mode="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ "{{mode}}" = "--dry-run" ]; then
        files="$(gofmt -l cli/tui)"
        [ -z "$files" ] || { echo "$files"; exit 1; }
    elif [ -z "{{mode}}" ]; then
        gofmt -w cli/tui
    else
        echo "Usage: just lint tui [--dry-run]" >&2
        exit 2
    fi
    go vet ./cli/tui/...

# Frozen Rust is explicit-only while tui-rs/ remains frozen.
rust mode="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ "{{mode}}" = "--dry-run" ]; then
        cargo fmt --manifest-path tui-rs/Cargo.toml --all -- --check
    elif [ -z "{{mode}}" ]; then
        cargo fmt --manifest-path tui-rs/Cargo.toml --all
    else
        echo "Usage: just lint rust [--dry-run]" >&2
        exit 2
    fi
    cargo clippy --manifest-path tui-rs/Cargo.toml --workspace --all-targets --locked -- -D warnings
    cargo run --manifest-path tui-rs/Cargo.toml --locked -p manifest-check -- check --repo-root .

docs mode="":
    #!/usr/bin/env bash
    set -euo pipefail
    just _node
    if [ "{{mode}}" = "--dry-run" ]; then
        pnpm --dir docs exec prettier --check .
    elif [ -z "{{mode}}" ]; then
        pnpm --dir docs exec prettier --write .
    else
        echo "Usage: just lint docs [--dry-run]" >&2
        exit 2
    fi
    pnpm --dir docs exec astro check

[private]
_desktop-native:
    cargo check --manifest-path apps/desktop/src-tauri/Cargo.toml --locked
