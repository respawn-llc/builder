use std::fs;
use std::path::Path;

use tempfile::TempDir;

fn write(path: &Path, contents: &str) {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).unwrap();
    }
    fs::write(path, contents).unwrap();
}

fn repo() -> TempDir {
    let dir = tempfile::tempdir().unwrap();
    write(
        &dir.path().join("tui-rs/Cargo.toml"),
        "[workspace]\nmembers = []\n",
    );
    dir
}

#[test]
fn lint_policy_rejects_unsafe_anywhere() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/src/lib.rs"),
        "#![forbid(unsafe_code)]\npub fn f() { unsafe {} }\n",
    );

    let findings = manifest_check::lint_policy::check(repo.path()).unwrap_err();

    assert!(
        findings
            .iter()
            .any(|finding| finding.code == "rust_unsafe_code")
    );
}

#[test]
fn lint_policy_rejects_unwrap_outside_actual_test_files() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/test-support/src/lib.rs"),
        "#![forbid(unsafe_code)]\npub fn f(value: Option<&str>) -> &str { value.unwrap() }\n",
    );
    write(
        &repo
            .path()
            .join("tui-rs/crates/sample-crate/tests/allows_unwrap.rs"),
        "#[test]\nfn f() { Some(1).unwrap(); }\n",
    );

    let findings = manifest_check::lint_policy::check(repo.path()).unwrap_err();

    assert!(
        findings
            .iter()
            .any(|finding| finding.code == "rust_unwrap_outside_test")
    );
    assert_eq!(
        findings
            .iter()
            .filter(|finding| finding.code == "rust_unwrap_outside_test")
            .count(),
        1
    );
}

#[test]
fn lint_policy_rejects_suppression_attempts() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/tools/manifest-check/src/main.rs"),
        "#![allow(unsafe_code)]\n#[allow(clippy::unwrap_used)]\nfn main() {}\n",
    );

    let findings = manifest_check::lint_policy::check(repo.path()).unwrap_err();

    assert!(
        findings
            .iter()
            .any(|finding| finding.code == "rust_safety_lint_suppression")
    );
}

#[test]
fn lint_policy_rejects_inline_tests_in_src() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/src/lib.rs"),
        "#![forbid(unsafe_code)]\n#[cfg(test)]\nmod tests {}\n",
    );

    let findings = manifest_check::lint_policy::check(repo.path()).unwrap_err();

    assert!(
        findings
            .iter()
            .any(|finding| finding.code == "rust_inline_tests_in_src")
    );
}

#[test]
fn lint_policy_rejects_auto_discovered_integration_tests() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/Cargo.toml"),
        r#"
[package]
name = "sample-crate"
version = "0.0.0"
edition = "2024"
"#,
    );
    write(
        &repo
            .path()
            .join("tui-rs/crates/sample-crate/tests/sample_behavior.rs"),
        "#[test]\nfn sample_behavior() {}\n",
    );

    let findings = manifest_check::lint_policy::check(repo.path()).unwrap_err();

    assert!(
        findings
            .iter()
            .any(|finding| finding.code == "rust_integration_harness_policy")
    );
}

#[test]
fn lint_policy_accepts_consolidated_integration_harness() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/Cargo.toml"),
        r#"
[package]
name = "sample-crate"
version = "0.0.0"
build = "../../build-support/integration_harness.rs"
edition = "2024"
autotests = false

[lib]
doctest = false

[[test]]
name = "integration"
path = "tests/integration.rs"
"#,
    );
    write(
        &repo
            .path()
            .join("tui-rs/crates/sample-crate/tests/sample_behavior.rs"),
        "#[test]\nfn sample_behavior() {}\n",
    );
    write(
        &repo
            .path()
            .join("tui-rs/crates/sample-crate/tests/integration.rs"),
        "#![forbid(unsafe_code)]\ninclude!(concat!(env!(\"OUT_DIR\"), \"/integration_modules.rs\"));\n",
    );
    manifest_check::lint_policy::check(repo.path()).unwrap();
}

#[test]
fn lint_policy_rejects_manual_consolidated_harness() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/Cargo.toml"),
        r#"
[package]
name = "sample-crate"
version = "0.0.0"
build = "../../build-support/integration_harness.rs"
edition = "2024"
autotests = false

[lib]
doctest = false

[[test]]
name = "integration"
path = "tests/integration.rs"
"#,
    );
    write(
        &repo
            .path()
            .join("tui-rs/crates/sample-crate/tests/sample_behavior.rs"),
        "#[test]\nfn sample_behavior() {}\n",
    );
    write(
        &repo
            .path()
            .join("tui-rs/crates/sample-crate/tests/integration.rs"),
        "#![forbid(unsafe_code)]\n#[path = \"sample_behavior.rs\"]\nmod sample_behavior;\n",
    );
    let findings = manifest_check::lint_policy::check(repo.path()).unwrap_err();

    assert!(findings.iter().any(|finding| {
        finding.code == "rust_integration_harness_policy"
            && finding.detail.contains("generated modules")
    }));
}

#[test]
fn lint_policy_rejects_missing_integration_harness_generator() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/Cargo.toml"),
        r#"
[package]
name = "sample-crate"
version = "0.0.0"
edition = "2024"
autotests = false

[lib]
doctest = false

[[test]]
name = "integration"
path = "tests/integration.rs"
"#,
    );
    write(
        &repo
            .path()
            .join("tui-rs/crates/sample-crate/tests/sample_behavior.rs"),
        "#[test]\nfn sample_behavior() {}\n",
    );
    write(
        &repo
            .path()
            .join("tui-rs/crates/sample-crate/tests/integration.rs"),
        "#![forbid(unsafe_code)]\ninclude!(concat!(env!(\"OUT_DIR\"), \"/integration_modules.rs\"));\n",
    );
    let findings = manifest_check::lint_policy::check(repo.path()).unwrap_err();

    assert!(findings.iter().any(|finding| {
        finding.code == "rust_integration_harness_policy"
            && finding.detail.contains("package.build")
    }));
}

#[test]
fn lint_policy_rejects_library_doctests_by_default() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/Cargo.toml"),
        r#"
[package]
name = "sample-crate"
version = "0.0.0"
edition = "2024"
"#,
    );
    write(
        &repo.path().join("tui-rs/crates/sample-crate/src/lib.rs"),
        "#![forbid(unsafe_code)]\n",
    );

    let findings = manifest_check::lint_policy::check(repo.path()).unwrap_err();

    assert!(
        findings
            .iter()
            .any(|finding| finding.code == "rust_library_doctest_policy")
    );
}

#[test]
fn lint_policy_accepts_library_doctests_disabled() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/Cargo.toml"),
        r#"
[package]
name = "sample-crate"
version = "0.0.0"
edition = "2024"

[lib]
doctest = false
"#,
    );
    write(
        &repo.path().join("tui-rs/crates/sample-crate/src/lib.rs"),
        "#![forbid(unsafe_code)]\n",
    );

    manifest_check::lint_policy::check(repo.path()).unwrap();
}

#[test]
fn lint_policy_allows_expect_and_explicit_panics_initially() {
    let repo = repo();
    write(
        &repo.path().join("tui-rs/crates/sample-crate/src/lib.rs"),
        "#![forbid(unsafe_code)]\npub fn f(value: Option<&str>) -> &str { value.expect(\"invariant\") }\npub fn g() { panic!(\"invariant\") }\n",
    );

    manifest_check::lint_policy::check(repo.path()).unwrap();
}
