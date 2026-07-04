# Rust TUI Tests

Run the Rust TUI suite from `/tui-rs`:

```sh
cargo test --workspace --all-targets
```

Packages with root-level integration tests use one generated `integration` harness. Cargo auto-discovery is disabled with `autotests = false`, and each package points `package.build` at `../../build-support/integration_harness.rs` to generate module declarations for direct `tests/*.rs` files into `OUT_DIR`.

Run a single test through the generated harness by naming its module path:

```sh
cargo test -p rpc-client --test integration <module>::<test_name>
```

Direct `tests/*.rs` filenames must be valid Rust module identifiers, such as `path_reference_search_controller.rs`. Nested support files under `tests/support/` are regular modules and are not generated as root integration tests.

## Dependency Policy

`cargo deny --manifest-path tui-rs/Cargo.toml check` is part of the TUI test gate. Advisory ignores in `tui-rs/deny.toml` must stay paired with a guard when the underlying tool cannot express the intended scope: add a `manifest-check` test that pins the dependency chain the ignore is scoped to, and record the rationale here.
