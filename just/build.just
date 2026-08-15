set working-directory := ".."

# Build the Kent Go CLI/TUI binary.
default *args:
    {{ quote(just_executable()) }} build _go {{args}}

# Build the Kent Go CLI/TUI binary.
go *args:
    {{ quote(just_executable()) }} build _go {{args}}

# Build the native desktop application bundle.
desktop *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/desktop.mjs build {{args}}

# Build the active Go TUI as part of the Kent CLI binary.
tui *args:
    {{ quote(just_executable()) }} build _go {{args}}

# Build the frozen Rust workspace. Temporary: delete when `tui-rs/` is removed.
rust *args:
    cargo build --manifest-path tui-rs/Cargo.toml --workspace --locked {{args}}

# Build the documentation site.
docs *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/docs.mjs build {{args}}

[private]
_go *args:
    go run ./tools/devcmd/gobuild {{ args }}

[private]
_desktop-web *args:
    {{ quote(just_executable()) }} _node-preflight
    {{ quote(just_executable()) }} _generate-typescript
    node tools/devcmd/desktop.mjs web-build {{args}}

[private]
_desktop-smoke-macos *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/desktop.mjs smoke-macos {{args}}
