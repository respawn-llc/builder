set working-directory := ".."

# Run all active server and desktop tests.
default *args:
    {{ quote(just_executable()) }} test server {{args}}
    {{ quote(just_executable()) }} test desktop

# Run the desktop Vitest suite.
desktop *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/test.mjs desktop {{args}}

# Run all Go server tests, or forward Go arguments after `--`.
server *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/test.mjs server {{args}}

# Run the active Go TUI tests.
tui *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/test.mjs tui {{args}}

# Test the frozen Rust workspace. Temporary: delete when `tui-rs/` is removed.
rust *args:
    cargo test --manifest-path tui-rs/Cargo.toml --locked --test integration -p client-contracts {{args}}
    cargo test --manifest-path tui-rs/Cargo.toml --locked --test integration -p manifest-check
