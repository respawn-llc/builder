set default-list
set lists
set positional-arguments
set unstable

mod build 'just/build.just'
[private]
mod _check 'just/check.just'
mod dev 'just/dev.just'
mod dump 'just/dump.just'
mod install 'just/install.just'
[private]
mod _lint 'just/lint.just'
mod release 'just/release.just'
mod update 'just/update.just'

# Prepare this checkout for development. Pass --apply to make changes.
setup *args: _node
    #!/usr/bin/env bash
    set -euo pipefail
    [ "$#" -le 1 ] || { echo "Usage: just setup [--apply]" >&2; exit 2; }
    mode="${1:-}"
    if [ "$mode" != "" ] && [ "$mode" != "--apply" ]; then
        echo "Usage: just setup [--apply]" >&2
        exit 2
    fi
    for tool in go pnpm cargo git jq python3 rg; do
        command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
    done
    if [ "$mode" = "" ]; then
        echo "Would download Go modules"
        echo "Would install frozen apps and docs dependencies"
        echo "Would fetch desktop Rust dependencies"
        echo "Would configure core.hooksPath=.githooks"
        exit 0
    fi
    just install _dependencies
    git config --local core.hooksPath .githooks

# Regenerate protobuf-derived Go and TypeScript sources.
gen: _node
    go run github.com/bufbuild/buf/cmd/buf@v1.72.0 lint
    just _gen-go
    just _gen-typescript

# Run active tests, or select server, desktop, tui, or explicit frozen rust.
test *args:
    @bash scripts/test.sh {{ args }}

# Apply safe lint fixes, or pass --dry-run for read-only validation.
lint *args:
    just _area-command _lint {{ args }}

# Run lint, tests, and builds for the selected area.
check *args:
    just _area-command _check {{ args }}

[private]
_area-command *args:
    #!/usr/bin/env bash
    set -euo pipefail
    command_name="$1"
    shift
    target="default"
    target_selected=0
    mode=""
    for argument in "$@"; do
        case "$argument" in
        default|server|desktop|tui|rust|docs)
            [ "$target_selected" -eq 0 ] || { echo "Select one target" >&2; exit 2; }
            target="$argument"
            target_selected=1
            ;;
        --dry-run)
            [ -z "$mode" ] || { echo "--dry-run was provided more than once" >&2; exit 2; }
            mode="--dry-run"
            ;;
        *)
            echo "Unknown ${command_name#_} argument: $argument" >&2
            exit 2
            ;;
        esac
    done
    if [ -n "$mode" ]; then
        just "$command_name" "$target" "$mode"
    else
        just "$command_name" "$target"
    fi

[private]
_gen-go:
    GOOS= GOARCH= go run github.com/bufbuild/buf/cmd/buf@v1.72.0 generate --template buf.gen.go.yaml

[private]
_gen-typescript:
    GOOS= GOARCH= go run github.com/bufbuild/buf/cmd/buf@v1.72.0 generate --template buf.gen.ts.yaml

[private]
_node:
    @command -v node >/dev/null || { echo "Node.js 22 or newer is required" >&2; exit 2; }
    @node -e 'const major=Number(process.versions.node.split(".")[0]); if (major < 22) { console.error("Node.js 22 or newer is required"); process.exit(2) }'
