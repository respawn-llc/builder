set working-directory := ".."
set lists
set unstable

# Fix lint findings in every active non-Rust area. Pass `--dry-run` for read-only validation.
default *args:
    {{ quote(just_executable()) }} lint server {{args}}
    {{ quote(just_executable()) }} lint desktop {{args}}
    {{ quote(just_executable()) }} lint tui {{args}}
    {{ quote(just_executable()) }} lint docs {{args}}

# Fix or validate Go server code and generated output.
server *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/lint.mjs server {{args}}

# Fix or validate desktop code and policies.
desktop *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/lint.mjs desktop {{args}}

# Fix or validate the active Go TUI.
tui *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/lint.mjs tui {{args}}

# Fix or validate documentation formatting and types.
docs *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/lint.mjs docs {{args}}

# Fix or validate the frozen Rust workspace. Temporary: delete when `tui-rs/` is removed.
rust mode="" checked=assert(mode == "" || mode == "--dry-run", "lint rust accepts only --dry-run"):
    cargo fmt --manifest-path tui-rs/Cargo.toml --all {{ if mode == "--dry-run" { "-- --check" } else { "" } }}
    cargo clippy --manifest-path tui-rs/Cargo.toml --workspace --all-targets --locked -- -D warnings
    cargo run --manifest-path tui-rs/Cargo.toml --locked -p manifest-check -- check --repo-root .
    cargo metadata --manifest-path tui-rs/Cargo.toml --locked --format-version 1
    cargo deny --manifest-path tui-rs/Cargo.toml check
