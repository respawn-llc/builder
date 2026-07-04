use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectBindingPlanRequest {
    pub path: String,
    #[serde(default)]
    pub mode: ProjectBindingPlanMode,
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub enum ProjectBindingPlanMode {
    #[default]
    Empty,
    Interactive,
    Headless,
    Unknown(String),
}

impl Serialize for ProjectBindingPlanMode {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        serializer.serialize_str(match self {
            Self::Empty => "",
            Self::Interactive => "interactive",
            Self::Headless => "headless",
            Self::Unknown(value) => value,
        })
    }
}

impl<'de> Deserialize<'de> for ProjectBindingPlanMode {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let value = String::deserialize(deserializer)?;
        Ok(match value.as_str() {
            "" => Self::Empty,
            "interactive" => Self::Interactive,
            "headless" => Self::Headless,
            _ => Self::Unknown(value),
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectBindingPlanResponse {
    pub kind: ProjectBindingPlanKind,
    pub canonical_root: String,
    pub path_availability: ProjectAvailability,
    #[serde(default)]
    pub binding: Option<ProjectBinding>,
    #[serde(default, deserialize_with = "crate::config::null_to_default")]
    pub projects: Vec<ProjectSummary>,
    #[serde(default)]
    pub workspace: Option<ProjectWorkspacePlanSelected>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum ProjectBindingPlanKind {
    #[serde(rename = "bound")]
    Bound,
    #[serde(rename = "local_unbound")]
    LocalUnbound,
    #[serde(rename = "server_workspace_selection")]
    ServerWorkspaceSelection,
    #[serde(rename = "headless_remote_selected")]
    HeadlessRemoteSelected,
    #[serde(rename = "headless_remote_ambiguous")]
    HeadlessRemoteAmbiguous,
    #[serde(untagged)]
    Unknown(String),
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectBinding {
    pub project_id: String,
    pub project_key: String,
    pub project_name: String,
    pub workspace_id: String,
    pub canonical_root: String,
    pub workspace_name: String,
    pub workspace_status: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectWorkspacePlanSelected {
    pub project_id: String,
    pub workspace_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectCreateRequest {
    pub display_name: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub project_key: String,
    pub workspace_root: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectCreateResponse {
    pub binding: ProjectBinding,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectAttachWorkspaceRequest {
    pub project_id: String,
    pub workspace_root: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectAttachWorkspaceResponse {
    pub binding: ProjectBinding,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectGetOverviewRequest {
    #[serde(rename = "ProjectID")]
    pub project_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectGetOverviewResponse {
    #[serde(rename = "Overview")]
    pub overview: ProjectOverview,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectOverview {
    #[serde(rename = "Project")]
    pub project: ProjectSummary,
    #[serde(rename = "Workspaces", default)]
    pub workspaces: Vec<ProjectWorkspaceSummary>,
    #[serde(rename = "Sessions", default)]
    pub sessions: Vec<SessionSummary>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectSummary {
    #[serde(rename = "ProjectID")]
    pub project_id: String,
    #[serde(rename = "ProjectKey")]
    pub project_key: String,
    #[serde(rename = "DisplayName")]
    pub display_name: String,
    #[serde(rename = "RootPath")]
    pub root_path: String,
    #[serde(rename = "Availability")]
    pub availability: ProjectAvailability,
    #[serde(rename = "SessionCount")]
    pub session_count: i32,
    #[serde(rename = "UpdatedAt")]
    pub updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProjectWorkspaceSummary {
    #[serde(rename = "WorkspaceID")]
    pub workspace_id: String,
    #[serde(rename = "DisplayName")]
    pub display_name: String,
    #[serde(rename = "RootPath")]
    pub root_path: String,
    #[serde(rename = "Availability")]
    pub availability: ProjectAvailability,
    #[serde(rename = "IsPrimary")]
    pub is_primary: bool,
    #[serde(rename = "SessionCount")]
    pub session_count: i32,
    #[serde(rename = "UpdatedAt")]
    pub updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionSummary {
    #[serde(rename = "SessionID")]
    pub session_id: String,
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "FirstPromptPreview")]
    pub first_prompt_preview: String,
    #[serde(rename = "UpdatedAt")]
    pub updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum ProjectAvailability {
    #[serde(rename = "available")]
    Available,
    #[serde(rename = "missing")]
    Missing,
    #[serde(rename = "inaccessible")]
    Inaccessible,
    #[serde(untagged)]
    Unknown(String),
}
