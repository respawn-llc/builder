set working-directory := ".."

desktop:
    @bash scripts/quiet-on-success.sh just install _desktop

[private]
_desktop:
    just _node
    bash scripts/build-desktop.sh --skip-install --install

[private]
_apps-dependencies:
    @bash scripts/quiet-on-success.sh just install _apps-dependencies-implementation

[private]
_apps-dependencies-implementation:
    just _node
    pnpm --dir apps install --frozen-lockfile

[private]
_docs-dependencies:
    @bash scripts/quiet-on-success.sh just install _docs-dependencies-implementation

[private]
_docs-dependencies-implementation:
    just _node
    pnpm --dir docs install --frozen-lockfile

[private]
_go-dependencies:
    @bash scripts/quiet-on-success.sh just install _go-dependencies-implementation

[private]
_go-dependencies-implementation:
    go mod download
    go mod download -modfile=tools.mod

[private]
_desktop-dependencies:
    @bash scripts/quiet-on-success.sh cargo fetch --manifest-path apps/desktop/src-tauri/Cargo.toml --locked

[private]
_dependencies:
    @bash scripts/quiet-on-success.sh just install _dependencies-implementation

[parallel]
[private]
_dependencies-implementation: _go-dependencies _apps-dependencies _docs-dependencies _desktop-dependencies
