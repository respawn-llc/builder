set working-directory := ".."

desktop:
    just _node
    bash scripts/build-desktop.sh --skip-install --install

[private]
_apps-dependencies:
    just _node
    pnpm --dir apps install --frozen-lockfile

[private]
_docs-dependencies:
    just _node
    pnpm --dir docs install --frozen-lockfile

[private]
_dependencies:
    go mod download
    just install _apps-dependencies
    just install _docs-dependencies
    cargo fetch --manifest-path apps/desktop/src-tauri/Cargo.toml --locked
