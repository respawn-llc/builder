set working-directory := ".."

default:
    just build go
    just build docs
    just build desktop

go *args:
    just build _go {{args}}

tui *args:
    just build go {{args}}

# Frozen Rust is explicit-only while tui-rs/ remains frozen.
rust *args:
    cargo build --manifest-path tui-rs/Cargo.toml --workspace --locked {{args}}

desktop *args:
    just _node
    bash scripts/build-desktop.sh --skip-install {{args}}

docs *args:
    just _node
    pnpm --dir docs exec astro build {{args}}
    node docs/scripts/emit-deployment-files.mjs

[private]
_go *args:
    just _gen-go
    bash scripts/build.sh server {{args}}

[private]
_desktop-web:
    just _node
    just _gen-typescript
    node apps/desktop/node_modules/typescript7/bin/tsc -b apps/desktop/tsconfig.json --pretty false
    pnpm --dir apps/desktop exec vite --config tooling/vite.config.ts build
    node apps/scripts/check-desktop-font-assets.mjs

[private]
_desktop-smoke-macos bundle_dir:
    #!/usr/bin/env bash
    set -euo pipefail
    shopt -s nullglob
    dmgs=({{bundle_dir}}/*.dmg)
    [ "${#dmgs[@]}" -eq 1 ] || { echo "Expected exactly one macOS DMG, found ${#dmgs[@]}." >&2; exit 1; }
    mount_dir="$(mktemp -d)"
    printf 'Y\n' | hdiutil attach "${dmgs[0]}" -nobrowse -readonly -mountpoint "$mount_dir"
    trap 'hdiutil detach "$mount_dir"' EXIT
    app="$mount_dir/Kent.app"
    executable="$app/Contents/MacOS/kent-desktop"
    [ "$(plutil -extract LSMinimumSystemVersion raw "$app/Contents/Info.plist")" = "15.0" ]
    lipo "$executable" -verify_arch arm64
    "$executable" >"$TMPDIR/kent-desktop.log" 2>&1 &
    app_pid=$!
    sleep 10
    kill -0 "$app_pid" || { cat "$TMPDIR/kent-desktop.log" >&2; wait "$app_pid"; }
    kill -TERM "$app_pid"
    wait "$app_pid" || true

[private]
_desktop-ci-debug key:
    just _node
    pnpm --dir apps/desktop exec tauri signer generate -f -w {{key}} -p ""
    TAURI_SIGNING_PRIVATE_KEY="$(cat {{key}})" TAURI_SIGNING_PRIVATE_KEY_PASSWORD="" bash scripts/build-desktop.sh --skip-install -- --debug
