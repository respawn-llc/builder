use super::{Finding, relative_path};
use crate::lint_policy::{read_cargo_manifest, rust_files};
use std::fs;
use std::path::Path;
use syn::visit::{self, Visit};

pub(crate) fn check(repo_root: &Path, findings: &mut Vec<Finding>) {
    let manifest_path = repo_root.join("apps/desktop/src-tauri/Cargo.toml");
    if manifest_path.exists() {
        check_dependencies(repo_root, &manifest_path, findings);
    }
    let source_root = repo_root.join("apps/desktop/src-tauri/src");
    if !source_root.exists() {
        return;
    }
    for path in rust_files(&source_root, repo_root) {
        let source = match fs::read_to_string(&path) {
            Ok(source) => source,
            Err(error) => {
                findings.push(Finding::new(
                    "rust_source_read_failed",
                    relative_path(repo_root, &path),
                    error.to_string(),
                ));
                continue;
            }
        };
        let syntax = match syn::parse_file(&source) {
            Ok(syntax) => syntax,
            Err(error) => {
                findings.push(Finding::new(
                    "rust_source_parse_failed",
                    relative_path(repo_root, &path),
                    error.to_string(),
                ));
                continue;
            }
        };
        let mut visitor = ExecutableAdapterVisitor {
            repo_root,
            path: &path,
            findings,
        };
        visitor.visit_file(&syntax);
    }
}

fn check_dependencies(repo_root: &Path, manifest_path: &Path, findings: &mut Vec<Finding>) {
    let Some(manifest) = read_cargo_manifest(repo_root, manifest_path, findings) else {
        return;
    };
    let mut dependencies = Vec::new();
    collect_dependencies(&manifest, &mut dependencies);
    const EXECUTABLE_ADAPTER_CRATES: &[&str] = &[
        "async-process",
        "command-group",
        "duct",
        "subprocess",
        "tauri-plugin-shell",
    ];
    for dependency in dependencies {
        if EXECUTABLE_ADAPTER_CRATES.contains(&dependency.package.as_str())
            || (dependency.package == "tokio"
                && dependency
                    .features
                    .iter()
                    .any(|feature| feature == "process"))
        {
            findings.push(Finding::new(
                "desktop_lifecycle_executable_adapter",
                relative_path(repo_root, manifest_path),
                format!(
                    "desktop Rust composition must not enable process execution through {}",
                    dependency.package
                ),
            ));
        }
    }
}

struct CargoDependency {
    package: String,
    features: Vec<String>,
}

fn collect_dependencies(value: &toml::Value, dependencies: &mut Vec<CargoDependency>) {
    let Some(table) = value.as_table() else {
        return;
    };
    for (key, child) in table {
        if matches!(
            key.as_str(),
            "dependencies" | "dev-dependencies" | "build-dependencies"
        ) {
            if let Some(dependency_table) = child.as_table() {
                dependencies.extend(dependency_table.iter().map(|(name, specification)| {
                    let specification = specification.as_table();
                    let package = specification
                        .and_then(|table| table.get("package"))
                        .and_then(toml::Value::as_str)
                        .unwrap_or(name)
                        .to_string();
                    let features = specification
                        .and_then(|table| table.get("features"))
                        .and_then(toml::Value::as_array)
                        .into_iter()
                        .flatten()
                        .filter_map(toml::Value::as_str)
                        .map(str::to_string)
                        .collect();
                    CargoDependency { package, features }
                }));
            }
            continue;
        }
        collect_dependencies(child, dependencies);
    }
}

struct ExecutableAdapterVisitor<'a> {
    repo_root: &'a Path,
    path: &'a Path,
    findings: &'a mut Vec<Finding>,
}

impl ExecutableAdapterVisitor<'_> {
    fn finding(&mut self) {
        self.findings.push(Finding::new(
            "desktop_lifecycle_executable_adapter",
            relative_path(self.repo_root, self.path),
            "desktop Rust composition must not import or invoke std process execution",
        ));
    }
}

impl<'ast> Visit<'ast> for ExecutableAdapterVisitor<'_> {
    fn visit_item_use(&mut self, node: &'ast syn::ItemUse) {
        if use_tree_has_process_execution(&node.tree, &mut Vec::new()) {
            self.finding();
        }
        visit::visit_item_use(self, node);
    }

    fn visit_path(&mut self, node: &'ast syn::Path) {
        if path_has_process_execution(
            node.segments
                .iter()
                .map(|segment| segment.ident.to_string()),
        ) {
            self.finding();
        }
        visit::visit_path(self, node);
    }
}

fn use_tree_has_process_execution(tree: &syn::UseTree, segments: &mut Vec<String>) -> bool {
    match tree {
        syn::UseTree::Path(path) => {
            segments.push(path.ident.to_string());
            let found = use_tree_has_process_execution(&path.tree, segments);
            segments.pop();
            found
        }
        syn::UseTree::Name(name) => {
            segments.push(name.ident.to_string());
            let found = path_has_process_execution(segments.iter().cloned());
            segments.pop();
            found
        }
        syn::UseTree::Rename(rename) => {
            segments.push(rename.ident.to_string());
            let found = path_has_process_execution(segments.iter().cloned());
            segments.pop();
            found
        }
        syn::UseTree::Glob(_) => path_has_process_execution(segments.iter().cloned()),
        syn::UseTree::Group(group) => group
            .items
            .iter()
            .any(|item| use_tree_has_process_execution(item, segments)),
    }
}

fn path_has_process_execution(segments: impl IntoIterator<Item = String>) -> bool {
    let mut segments = segments.into_iter();
    matches!(
        (segments.next().as_deref(), segments.next().as_deref()),
        (Some("std"), Some("process"))
    )
}
