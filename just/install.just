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
_go-dependencies:
    go mod download

[private]
_desktop-dependencies:
    cargo fetch --manifest-path apps/desktop/src-tauri/Cargo.toml --locked

[parallel]
[private]
_dependencies: _go-dependencies _apps-dependencies _docs-dependencies _desktop-dependencies
