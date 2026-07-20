#![forbid(unsafe_code)]

use std::fmt;
use std::path::{Component, Path, PathBuf};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Finding {
    pub code: &'static str,
    pub path: Option<PathBuf>,
    pub detail: String,
}

impl Finding {
    fn new(
        code: &'static str,
        path: impl Into<Option<PathBuf>>,
        detail: impl Into<String>,
    ) -> Self {
        Self {
            code,
            path: path.into(),
            detail: detail.into(),
        }
    }
}

impl fmt::Display for Finding {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match &self.path {
            Some(path) => write!(
                formatter,
                "{}: {}: {}",
                self.code,
                path.display(),
                self.detail
            ),
            None => write!(formatter, "{}: {}", self.code, self.detail),
        }
    }
}

pub fn check_repository(repo_root: &Path) -> Result<(), Vec<Finding>> {
    lint_policy::check(repo_root)
}

pub mod lint_policy;

fn relative_path(repo_root: &Path, path: &Path) -> Option<PathBuf> {
    path.strip_prefix(repo_root).map(Path::to_path_buf).ok()
}

fn is_src_file(path: &Path) -> bool {
    path.components().any(|component| match component {
        Component::Normal(value) => value == "src",
        _ => false,
    })
}

fn is_actual_test_file(path: &Path) -> bool {
    path.components().any(|component| match component {
        Component::Normal(value) => value == "tests",
        _ => false,
    })
}
