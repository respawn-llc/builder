set working-directory := ".."

[parallel]
default mode="": (server mode) (desktop mode) (docs mode)

server mode="":
    just _lint server {{mode}}
    just test server
    just build go

desktop mode="":
    if [ "{{mode}}" = "--dry-run" ]; then just _gen-typescript; fi
    just _lint desktop {{mode}}
    just test desktop
    just build desktop

tui mode="":
    just _lint tui {{mode}}
    just test tui
    just build tui

# Frozen Rust is explicit-only while tui-rs/ remains frozen.
rust mode="":
    just _lint rust {{mode}}
    just test rust
    just build rust

docs mode="":
    just _lint docs {{mode}}
    node --test docs/scripts/sync-mirrored-docs.test.mjs
    just build docs
