use super::*;
use std::fs;
use syn::visit::{self, Visit};
use walkdir::WalkDir;

pub fn check(repo_root: &Path) -> Result<(), Vec<Finding>> {
    let rust_root = repo_root.join("tui-rs");
    if !rust_root.exists() {
        return Ok(());
    }

    let mut findings = Vec::new();
    let rust_files = rust_files(&rust_root, repo_root);
    for path in &rust_files {
        check_rust_file(repo_root, path, &mut findings);
    }
    check_integration_harnesses(repo_root, &rust_root, &mut findings);
    check_library_doctest_policy(repo_root, &rust_root, &mut findings);
    check_production_src_file_size(repo_root, &rust_files, &mut findings);

    if findings.is_empty() {
        Ok(())
    } else {
        Err(findings)
    }
}

fn check_rust_file(repo_root: &Path, path: &Path, findings: &mut Vec<Finding>) {
    let source = match fs::read_to_string(path) {
        Ok(source) => source,
        Err(error) => {
            findings.push(Finding::new(
                "rust_source_read_failed",
                relative_path(repo_root, path),
                error.to_string(),
            ));
            return;
        }
    };
    let syntax = match syn::parse_file(&source) {
        Ok(syntax) => syntax,
        Err(error) => {
            findings.push(Finding::new(
                "rust_source_parse_failed",
                relative_path(repo_root, path),
                error.to_string(),
            ));
            return;
        }
    };

    let relative = relative_path(repo_root, path);
    for attr in &syntax.attrs {
        check_attr(path, relative.clone(), attr, findings);
    }
    let mut visitor = LintVisitor {
        repo_root,
        path,
        findings,
    };
    visitor.visit_file(&syntax);

    if is_crate_root(path) && !has_forbid_unsafe(&syntax.attrs) {
        visitor.findings.push(Finding::new(
            "rust_crate_root_missing_forbid_unsafe",
            relative,
            "crate root must include #![forbid(unsafe_code)]",
        ));
    }
}

fn rust_files(rust_root: &Path, repo_root: &Path) -> Vec<PathBuf> {
    WalkDir::new(rust_root)
        .into_iter()
        .filter_entry(|entry| !entry.path().ends_with("target"))
        .filter_map(|entry| entry.ok())
        .filter(|entry| entry.file_type().is_file())
        .map(|entry| entry.into_path())
        .filter(|path| path.extension().is_some_and(|extension| extension == "rs"))
        .filter(|path| relative_path(repo_root, path).is_some())
        .collect()
}

struct LintVisitor<'a> {
    repo_root: &'a Path,
    path: &'a Path,
    findings: &'a mut Vec<Finding>,
}

impl<'a> LintVisitor<'a> {
    fn finding(&mut self, code: &'static str, detail: &str) {
        self.findings.push(Finding::new(
            code,
            relative_path(self.repo_root, self.path),
            detail,
        ));
    }

    fn attr_findings(&mut self, attrs: &[syn::Attribute]) {
        for attr in attrs {
            check_attr(
                self.path,
                relative_path(self.repo_root, self.path),
                attr,
                self.findings,
            );
        }
    }
}

impl<'a, 'ast> Visit<'ast> for LintVisitor<'a> {
    fn visit_expr_unsafe(&mut self, node: &'ast syn::ExprUnsafe) {
        self.finding("rust_unsafe_code", "unsafe block is forbidden");
        visit::visit_expr_unsafe(self, node);
    }

    fn visit_item_impl(&mut self, node: &'ast syn::ItemImpl) {
        self.attr_findings(&node.attrs);
        if node.unsafety.is_some() {
            self.finding("rust_unsafe_code", "unsafe impl is forbidden");
        }
        visit::visit_item_impl(self, node);
    }

    fn visit_item_fn(&mut self, node: &'ast syn::ItemFn) {
        self.attr_findings(&node.attrs);
        if node.sig.unsafety.is_some() {
            self.finding("rust_unsafe_code", "unsafe function is forbidden");
        }
        visit::visit_item_fn(self, node);
    }

    fn visit_item_trait(&mut self, node: &'ast syn::ItemTrait) {
        self.attr_findings(&node.attrs);
        if node.unsafety.is_some() {
            self.finding("rust_unsafe_code", "unsafe trait is forbidden");
        }
        visit::visit_item_trait(self, node);
    }

    fn visit_item_foreign_mod(&mut self, node: &'ast syn::ItemForeignMod) {
        self.attr_findings(&node.attrs);
        if node.unsafety.is_some() {
            self.finding("rust_unsafe_code", "unsafe extern block is forbidden");
        }
        visit::visit_item_foreign_mod(self, node);
    }

    fn visit_item_mod(&mut self, node: &'ast syn::ItemMod) {
        self.attr_findings(&node.attrs);
        visit::visit_item_mod(self, node);
    }

    fn visit_expr_method_call(&mut self, node: &'ast syn::ExprMethodCall) {
        if node.method == "unwrap" && !is_actual_test_file(self.path) {
            self.finding(
                "rust_unwrap_outside_test",
                "unwrap is only allowed in Rust test files",
            );
        }
        visit::visit_expr_method_call(self, node);
    }

    fn visit_expr_call(&mut self, node: &'ast syn::ExprCall) {
        if path_ends_with_unwrap(&node.func) && !is_actual_test_file(self.path) {
            self.finding(
                "rust_unwrap_outside_test",
                "unwrap is only allowed in Rust test files",
            );
        }
        visit::visit_expr_call(self, node);
    }
}

fn check_attr(
    path: &Path,
    relative: Option<PathBuf>,
    attr: &syn::Attribute,
    findings: &mut Vec<Finding>,
) {
    let has_safety_suppression = match &attr.meta {
        syn::Meta::List(list) => {
            list_contains_ident(list, "unsafe_code") || list_contains_ident(list, "unwrap_used")
        }
        syn::Meta::NameValue(_) => false,
        syn::Meta::Path(_) => false,
    };
    if attr.path().is_ident("allow") && has_safety_suppression {
        findings.push(Finding::new(
            "rust_safety_lint_suppression",
            relative.clone(),
            "safety lint suppression is forbidden",
        ));
    }
    if is_src_file(path)
        && (attr.path().is_ident("cfg") || attr.path().is_ident("cfg_attr"))
        && match &attr.meta {
            syn::Meta::List(list) => list_contains_ident(list, "test"),
            syn::Meta::NameValue(_) | syn::Meta::Path(_) => false,
        }
    {
        findings.push(Finding::new(
            "rust_inline_tests_in_src",
            relative,
            "cfg(test) and cfg_attr(test, ...) are forbidden in src files",
        ));
    }
}

fn has_forbid_unsafe(attrs: &[syn::Attribute]) -> bool {
    attrs.iter().any(|attr| {
        matches!(attr.style, syn::AttrStyle::Inner(_))
            && attr.path().is_ident("forbid")
            && match &attr.meta {
                syn::Meta::List(list) => list_contains_ident(list, "unsafe_code"),
                syn::Meta::NameValue(_) | syn::Meta::Path(_) => false,
            }
    })
}

fn list_contains_ident(list: &syn::MetaList, ident: &str) -> bool {
    list.parse_args_with(syn::punctuated::Punctuated::<syn::Meta, syn::Token![,]>::parse_terminated)
        .map(|items| items.iter().any(|meta| meta_contains_ident(meta, ident)))
        .unwrap_or(false)
}

fn meta_contains_ident(meta: &syn::Meta, ident: &str) -> bool {
    match meta {
        syn::Meta::Path(path) => path_contains_ident(path, ident),
        syn::Meta::List(list) => {
            path_contains_ident(&list.path, ident) || list_contains_ident(list, ident)
        }
        syn::Meta::NameValue(name_value) => path_contains_ident(&name_value.path, ident),
    }
}

fn path_contains_ident(path: &syn::Path, ident: &str) -> bool {
    path.segments.iter().any(|segment| segment.ident == ident)
}

fn path_ends_with_unwrap(expr: &syn::Expr) -> bool {
    match expr {
        syn::Expr::Path(path) => path
            .path
            .segments
            .last()
            .is_some_and(|segment| segment.ident == "unwrap"),
        _ => false,
    }
}

fn is_crate_root(path: &Path) -> bool {
    let file_name = path.file_name().and_then(|name| name.to_str());
    let parent_name = path
        .parent()
        .and_then(|parent| parent.file_name())
        .and_then(|name| name.to_str());
    (file_name.is_some_and(|name| name == "lib.rs" || name == "main.rs")
        && parent_name.is_some_and(|name| name == "src"))
        || (file_name.is_some_and(|name| name == "integration.rs")
            && parent_name.is_some_and(|name| name == "tests"))
}

fn check_integration_harnesses(repo_root: &Path, rust_root: &Path, findings: &mut Vec<Finding>) {
    for manifest_path in cargo_manifest_files(rust_root) {
        check_integration_harness(repo_root, &manifest_path, findings);
    }
}

fn cargo_manifest_files(rust_root: &Path) -> Vec<PathBuf> {
    WalkDir::new(rust_root)
        .into_iter()
        .filter_entry(|entry| !entry.path().ends_with("target"))
        .filter_map(|entry| entry.ok())
        .filter(|entry| entry.file_type().is_file())
        .map(|entry| entry.into_path())
        .filter(|path| path.file_name().is_some_and(|name| name == "Cargo.toml"))
        .collect()
}

fn check_integration_harness(repo_root: &Path, manifest_path: &Path, findings: &mut Vec<Finding>) {
    let Some(package_root) = manifest_path.parent() else {
        return;
    };
    let tests_dir = package_root.join("tests");
    let Ok(test_entries) = direct_rust_test_files(&tests_dir) else {
        return;
    };
    if test_entries.is_empty() {
        return;
    }

    let Some(manifest) = read_cargo_manifest(repo_root, manifest_path, findings) else {
        return;
    };
    let Some(package) = manifest.get("package").and_then(toml::Value::as_table) else {
        return;
    };

    let autotests_disabled = package.get("autotests").and_then(toml::Value::as_bool) == Some(false);
    let shared_build_script = package.get("build").and_then(toml::Value::as_str)
        == Some("../../build-support/integration_harness.rs");
    let explicit_tests = manifest
        .get("test")
        .and_then(toml::Value::as_array)
        .cloned()
        .unwrap_or_default();
    let integration_harness_count = explicit_tests
        .iter()
        .filter(|entry| {
            entry.get("name").and_then(toml::Value::as_str) == Some("integration")
                && entry.get("path").and_then(toml::Value::as_str) == Some("tests/integration.rs")
        })
        .count();

    if !autotests_disabled
        || !shared_build_script
        || explicit_tests.len() != 1
        || integration_harness_count != 1
    {
        findings.push(Finding::new(
            "rust_integration_harness_policy",
            relative_path(repo_root, manifest_path),
            "packages with integration tests must set package.autotests = false, package.build = \"../../build-support/integration_harness.rs\", and declare exactly one [[test]] integration harness at tests/integration.rs; see docs/dev/rust-tui-tests.md",
        ));
        return;
    }

    check_integration_harness_uses_generated_modules(repo_root, &tests_dir, findings);
}

fn check_integration_harness_uses_generated_modules(
    repo_root: &Path,
    tests_dir: &Path,
    findings: &mut Vec<Finding>,
) {
    let harness_path = tests_dir.join("integration.rs");
    let source = match fs::read_to_string(&harness_path) {
        Ok(source) => source,
        Err(error) => {
            findings.push(Finding::new(
                "rust_integration_harness_policy",
                relative_path(repo_root, &harness_path),
                format!("integration harness could not be read: {error}"),
            ));
            return;
        }
    };
    let syntax = match syn::parse_file(&source) {
        Ok(syntax) => syntax,
        Err(error) => {
            findings.push(Finding::new(
                "rust_integration_harness_policy",
                relative_path(repo_root, &harness_path),
                format!("integration harness could not be parsed: {error}"),
            ));
            return;
        }
    };

    let has_generated_modules_include = syntax
        .items
        .iter()
        .any(integration_harness_item_includes_generated_modules);
    if !has_generated_modules_include {
        findings.push(Finding::new(
            "rust_integration_harness_policy",
            relative_path(repo_root, &harness_path),
            "integration harness must include generated modules from OUT_DIR/integration_modules.rs; see docs/dev/rust-tui-tests.md",
        ));
    }
}

fn integration_harness_item_includes_generated_modules(item: &syn::Item) -> bool {
    let syn::Item::Macro(item_macro) = item else {
        return false;
    };
    item_macro.mac.path.is_ident("include") && include_macro_uses_generated_modules(&item_macro.mac)
}

fn include_macro_uses_generated_modules(include_macro: &syn::Macro) -> bool {
    let Ok(expr) = include_macro.parse_body::<syn::Expr>() else {
        return false;
    };
    let syn::Expr::Macro(expr_macro) = expr else {
        return false;
    };
    expr_macro.mac.path.is_ident("concat") && concat_macro_uses_out_dir_modules(&expr_macro.mac)
}

fn concat_macro_uses_out_dir_modules(concat_macro: &syn::Macro) -> bool {
    let Ok(args) = concat_macro.parse_body_with(
        syn::punctuated::Punctuated::<syn::Expr, syn::Token![,]>::parse_terminated,
    ) else {
        return false;
    };
    let mut args = args.iter();
    let Some(out_dir_arg) = args.next() else {
        return false;
    };
    let Some(modules_arg) = args.next() else {
        return false;
    };
    args.next().is_none()
        && expr_is_env_out_dir(out_dir_arg)
        && expr_is_string_literal(modules_arg, "/integration_modules.rs")
}

fn expr_is_env_out_dir(expr: &syn::Expr) -> bool {
    let syn::Expr::Macro(expr_macro) = expr else {
        return false;
    };
    expr_macro.mac.path.is_ident("env")
        && expr_macro
            .mac
            .parse_body::<syn::LitStr>()
            .is_ok_and(|literal| literal.value() == "OUT_DIR")
}

fn expr_is_string_literal(expr: &syn::Expr, expected: &str) -> bool {
    let syn::Expr::Lit(expr_lit) = expr else {
        return false;
    };
    let syn::Lit::Str(literal) = &expr_lit.lit else {
        return false;
    };
    literal.value() == expected
}

fn check_library_doctest_policy(repo_root: &Path, rust_root: &Path, findings: &mut Vec<Finding>) {
    for manifest_path in cargo_manifest_files(rust_root) {
        check_library_doctest(repo_root, &manifest_path, findings);
    }
}

fn check_library_doctest(repo_root: &Path, manifest_path: &Path, findings: &mut Vec<Finding>) {
    let Some(package_root) = manifest_path.parent() else {
        return;
    };
    if !package_root.join("src/lib.rs").is_file() {
        return;
    }

    let Some(manifest) = read_cargo_manifest(repo_root, manifest_path, findings) else {
        return;
    };
    let Some(_package) = manifest.get("package").and_then(toml::Value::as_table) else {
        return;
    };

    let doctests_disabled = manifest
        .get("lib")
        .and_then(toml::Value::as_table)
        .and_then(|lib| lib.get("doctest"))
        .and_then(toml::Value::as_bool)
        == Some(false);
    if !doctests_disabled {
        findings.push(Finding::new(
            "rust_library_doctest_policy",
            relative_path(repo_root, manifest_path),
            "library packages must set [lib] doctest = false; use integration tests for executable examples",
        ));
    }
}

fn read_cargo_manifest(
    repo_root: &Path,
    manifest_path: &Path,
    findings: &mut Vec<Finding>,
) -> Option<toml::Value> {
    let source = match fs::read_to_string(manifest_path) {
        Ok(source) => source,
        Err(error) => {
            findings.push(Finding::new(
                "rust_cargo_manifest_read_failed",
                relative_path(repo_root, manifest_path),
                error.to_string(),
            ));
            return None;
        }
    };
    match toml::from_str::<toml::Value>(&source) {
        Ok(manifest) => Some(manifest),
        Err(error) => {
            findings.push(Finding::new(
                "rust_cargo_manifest_parse_failed",
                relative_path(repo_root, manifest_path),
                error.to_string(),
            ));
            None
        }
    }
}

fn direct_rust_test_files(tests_dir: &Path) -> Result<Vec<PathBuf>, ()> {
    let entries = match fs::read_dir(tests_dir) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(_error) => return Err(()),
    };
    let mut rust_files = Vec::new();
    for entry_result in entries {
        let entry = entry_result.map_err(|_error| ())?;
        let path = entry.path();
        if path.is_file() && path.extension().is_some_and(|extension| extension == "rs") {
            rust_files.push(path);
        }
    }
    Ok(rust_files)
}

const MAX_PRODUCTION_SRC_FILE_LINES: usize = 700;

// Shrink-only baseline recorded when the file-size guard was introduced: every
// production src file that already exceeded MAX_PRODUCTION_SRC_FILE_LINES at the
// time, paired with its line count then. Baseline files may shrink freely but must
// never grow past their recorded count; a file not listed here must stay at or
// under the limit.
const OVERSIZED_SRC_FILE_BASELINE: &[(&str, usize)] = &[
    ("tui-rs/crates/client-contracts/src/routes.rs", 709),
    ("tui-rs/crates/rpc-client/src/api.rs", 1039),
];

fn check_production_src_file_size(
    repo_root: &Path,
    rust_files: &[PathBuf],
    findings: &mut Vec<Finding>,
) {
    for path in rust_files {
        if !is_src_file(path) {
            continue;
        }
        let Some(relative) = relative_path(repo_root, path) else {
            continue;
        };
        let line_count = match fs::read_to_string(path) {
            Ok(source) => source.lines().count(),
            Err(error) => {
                findings.push(Finding::new(
                    "rust_source_read_failed",
                    Some(relative),
                    error.to_string(),
                ));
                continue;
            }
        };
        let baseline = relative.to_str().and_then(|relative| {
            OVERSIZED_SRC_FILE_BASELINE
                .iter()
                .find(|(baseline_path, _)| *baseline_path == relative)
                .map(|(_, baseline_count)| *baseline_count)
        });
        match baseline {
            Some(baseline_count) => {
                if line_count > baseline_count {
                    findings.push(Finding::new(
                        "rust_src_file_exceeds_baseline",
                        Some(relative),
                        format!(
                            "src file has {line_count} lines, exceeding its shrink-only baseline of {baseline_count} lines"
                        ),
                    ));
                }
            }
            None => {
                if line_count > MAX_PRODUCTION_SRC_FILE_LINES {
                    findings.push(Finding::new(
                        "rust_src_file_too_large",
                        Some(relative),
                        format!(
                            "src file has {line_count} lines, exceeding the {MAX_PRODUCTION_SRC_FILE_LINES}-line limit; split it or add it to the shrink-only OVERSIZED_SRC_FILE_BASELINE"
                        ),
                    ));
                }
            }
        }
    }
}
