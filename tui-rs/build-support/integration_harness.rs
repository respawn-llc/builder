#![forbid(unsafe_code)]

use std::env;
use std::ffi::OsStr;
use std::fs;
use std::path::{Path, PathBuf};

fn main() {
    if let Err(error) = run() {
        panic!("failed to generate integration test harness modules: {error}");
    }
}

fn run() -> Result<(), Box<dyn std::error::Error>> {
    let manifest_dir = PathBuf::from(env::var("CARGO_MANIFEST_DIR")?);
    let out_dir = PathBuf::from(env::var("OUT_DIR")?);
    let tests_dir = manifest_dir.join("tests");
    let generator_path = manifest_dir.join("../../build-support/integration_harness.rs");
    let protocol_version_path = manifest_dir.join("../../../shared/protocol/version.json");
    println!("cargo:rerun-if-changed={}", generator_path.display());
    println!("cargo:rerun-if-changed={}", tests_dir.display());
    println!("cargo:rerun-if-changed={}", protocol_version_path.display());
    println!(
        "cargo:rustc-env=KENT_PROTOCOL_VERSION={}",
        protocol_version(&protocol_version_path)?
    );

    let mut test_files = direct_rust_test_files(&tests_dir)?;
    test_files.sort_by(|left, right| left.file_name().cmp(&right.file_name()));

    let modules = test_files
        .iter()
        .map(|path| module_declaration(path))
        .collect::<Result<Vec<_>, _>>()?
        .join("\n");
    fs::write(out_dir.join("integration_modules.rs"), modules)?;
    Ok(())
}

fn protocol_version(path: &Path) -> Result<String, Box<dyn std::error::Error>> {
    let content = fs::read_to_string(path)?;
    let definition: serde_json::Value = serde_json::from_str(&content)?;
    let version = definition
        .get("version")
        .and_then(serde_json::Value::as_str)
        .map(str::trim)
        .filter(|version| !version.is_empty())
        .ok_or_else(|| format!("protocol version is required in {}", path.display()))?;
    Ok(version.to_owned())
}

fn direct_rust_test_files(tests_dir: &Path) -> Result<Vec<PathBuf>, Box<dyn std::error::Error>> {
    let entries = match fs::read_dir(tests_dir) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(Box::new(error)),
    };

    let mut test_files = Vec::new();
    for entry_result in entries {
        let entry = entry_result?;
        let path = entry.path();
        if path.is_file()
            && path.extension().is_some_and(|extension| extension == "rs")
            && path
                .file_name()
                .is_some_and(|file_name| file_name != "integration.rs")
        {
            println!("cargo:rerun-if-changed={}", path.display());
            test_files.push(path);
        }
    }
    Ok(test_files)
}

fn module_declaration(path: &Path) -> Result<String, Box<dyn std::error::Error>> {
    let module_name = module_name(path)?;
    let path_literal = rust_string_literal(&path.to_string_lossy());
    Ok(format!("#[path = {path_literal}]\nmod {module_name};\n"))
}

fn module_name(path: &Path) -> Result<String, Box<dyn std::error::Error>> {
    let stem = path
        .file_stem()
        .and_then(OsStr::to_str)
        .ok_or_else(|| format!("test file has no UTF-8 stem: {}", path.display()))?;
    let mut chars = stem.chars();
    let Some(first) = chars.next() else {
        return Err(format!("test file has empty stem: {}", path.display()).into());
    };
    if !is_ident_start(first) || !chars.all(is_ident_continue) {
        return Err(format!(
            "test file stem must be a Rust module identifier: {}",
            path.display()
        )
        .into());
    }
    Ok(stem.to_owned())
}

fn is_ident_start(value: char) -> bool {
    value == '_' || value.is_ascii_alphabetic()
}

fn is_ident_continue(value: char) -> bool {
    value == '_' || value.is_ascii_alphanumeric()
}

fn rust_string_literal(value: &str) -> String {
    let mut literal = String::from("\"");
    for character in value.chars() {
        match character {
            '\\' => literal.push_str("\\\\"),
            '"' => literal.push_str("\\\""),
            '\n' => literal.push_str("\\n"),
            '\r' => literal.push_str("\\r"),
            '\t' => literal.push_str("\\t"),
            character if character.is_control() => {
                literal.push_str(&format!("\\u{{{:x}}}", character as u32));
            }
            character => literal.push(character),
        }
    }
    literal.push('"');
    literal
}
