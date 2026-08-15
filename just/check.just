set working-directory := ".."

# Run lint, tests, and builds for every active non-Rust area.
default *args:
    {{ quote(just_executable()) }} check server {{args}}
    {{ quote(just_executable()) }} check desktop {{args}}
    {{ quote(just_executable()) }} check tui {{args}}
    {{ quote(just_executable()) }} check docs {{args}}

# Run the complete server suite.
server *args:
    {{ quote(just_executable()) }} lint server {{args}}
    {{ quote(just_executable()) }} test server
    {{ quote(just_executable()) }} build go

# Run the complete desktop suite, including a native bundle.
desktop *args:
    {{ quote(just_executable()) }} lint desktop {{args}}
    {{ quote(just_executable()) }} test desktop
    {{ quote(just_executable()) }} build desktop

# Run the complete active Go TUI suite.
tui *args:
    {{ quote(just_executable()) }} lint tui {{args}}
    {{ quote(just_executable()) }} test tui
    {{ quote(just_executable()) }} build tui

# Run the complete documentation suite.
docs *args:
    {{ quote(just_executable()) }} lint docs {{args}}
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/docs.mjs test
    {{ quote(just_executable()) }} build docs

# Run the frozen Rust suite. Temporary: delete when `tui-rs/` is removed.
rust *args:
    {{ quote(just_executable()) }} lint rust {{args}}
    {{ quote(just_executable()) }} test rust
    {{ quote(just_executable()) }} build rust
