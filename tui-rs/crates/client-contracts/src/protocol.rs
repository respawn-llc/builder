use serde::de::Error as _;
use serde::ser::Error as _;
use serde::{Deserialize, Deserializer, Serialize, Serializer};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct HandshakeRequest {
    #[serde(default)]
    pub protocol_version: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct HandshakeResponse {
    pub identity: ServerIdentity,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ServerIdentity {
    pub protocol_version: String,
    pub server_id: String,
    pub pid: i32,
    pub capabilities: CapabilityFlags,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub persistence_root_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct CapabilityFlags {
    pub jsonrpc_websocket: bool,
    pub auth_bootstrap: bool,
    pub project_attach: bool,
    pub session_attach: bool,
    pub health_endpoint: bool,
    pub readiness_endpoint: bool,
    pub run_prompt: bool,
    pub session_plan: bool,
    pub session_lifecycle: bool,
    pub session_transcript_paging: bool,
    pub session_runtime: bool,
    pub runtime_control: bool,
    pub prompt_control: bool,
    pub process_output: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AttachProjectWorkspace {
    WorkspaceId { workspace_id: String },
    WorkspaceRoot { workspace_root: String },
}

#[derive(Deserialize, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
enum AttachProjectWorkspaceWire {
    WorkspaceId { workspace_id: String },
    WorkspaceRoot { workspace_root: String },
}

impl AttachProjectWorkspace {
    pub fn id(workspace_id: &str) -> Self {
        Self::WorkspaceId {
            workspace_id: workspace_id.to_owned(),
        }
    }

    pub fn root(workspace_root: &str) -> Self {
        Self::WorkspaceRoot {
            workspace_root: workspace_root.to_owned(),
        }
    }
}

impl Serialize for AttachProjectWorkspace {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        let wire = match self {
            Self::WorkspaceId { workspace_id } => {
                validate_attach_field("workspace_id", workspace_id).map_err(S::Error::custom)?;
                AttachProjectWorkspaceWire::WorkspaceId {
                    workspace_id: workspace_id.clone(),
                }
            }
            Self::WorkspaceRoot { workspace_root } => {
                validate_attach_field("workspace_root", workspace_root)
                    .map_err(S::Error::custom)?;
                AttachProjectWorkspaceWire::WorkspaceRoot {
                    workspace_root: workspace_root.clone(),
                }
            }
        };
        wire.serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for AttachProjectWorkspace {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        match AttachProjectWorkspaceWire::deserialize(deserializer)? {
            AttachProjectWorkspaceWire::WorkspaceId { workspace_id } => {
                validate_attach_field("workspace_id", &workspace_id).map_err(D::Error::custom)?;
                Ok(Self::WorkspaceId { workspace_id })
            }
            AttachProjectWorkspaceWire::WorkspaceRoot { workspace_root } => {
                validate_attach_field("workspace_root", &workspace_root)
                    .map_err(D::Error::custom)?;
                Ok(Self::WorkspaceRoot { workspace_root })
            }
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AttachProjectRequest {
    pub project_id: String,
    pub workspace: Option<AttachProjectWorkspace>,
}

impl Serialize for AttachProjectRequest {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        validate_attach_field("project_id", &self.project_id).map_err(S::Error::custom)?;
        #[derive(Serialize)]
        struct Wire<'a> {
            project_id: &'a str,
            workspace: &'a Option<AttachProjectWorkspace>,
        }
        Wire {
            project_id: &self.project_id,
            workspace: &self.workspace,
        }
        .serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for AttachProjectRequest {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        #[derive(Deserialize)]
        #[serde(deny_unknown_fields)]
        struct Wire {
            project_id: String,
            #[serde(deserialize_with = "deserialize_required_attach_project_workspace")]
            workspace: Option<AttachProjectWorkspace>,
        }

        let wire = Wire::deserialize(deserializer)?;
        validate_attach_field("project_id", &wire.project_id).map_err(D::Error::custom)?;
        Ok(Self {
            project_id: wire.project_id,
            workspace: wire.workspace,
        })
    }
}

fn deserialize_required_attach_project_workspace<'de, D>(
    deserializer: D,
) -> Result<Option<AttachProjectWorkspace>, D::Error>
where
    D: Deserializer<'de>,
{
    Option::<AttachProjectWorkspace>::deserialize(deserializer)
}

fn validate_attach_field(name: &str, value: &str) -> Result<(), String> {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        return Err(format!("{name} is required"));
    }
    if trimmed != value {
        return Err(format!(
            "{name} must not have leading or trailing whitespace"
        ));
    }
    Ok(())
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AttachSessionRequest {
    pub session_id: String,
}

impl Serialize for AttachSessionRequest {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        validate_attach_field("session_id", &self.session_id).map_err(S::Error::custom)?;
        #[derive(Serialize)]
        struct Wire<'a> {
            session_id: &'a str,
        }
        Wire {
            session_id: &self.session_id,
        }
        .serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for AttachSessionRequest {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        #[derive(Deserialize)]
        #[serde(deny_unknown_fields)]
        struct Wire {
            session_id: String,
        }
        let wire = Wire::deserialize(deserializer)?;
        validate_attach_field("session_id", &wire.session_id).map_err(D::Error::custom)?;
        Ok(Self {
            session_id: wire.session_id,
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AttachProjectWorkspaceSelection {
    WorkspaceId {
        workspace_id: String,
    },
    WorkspaceRoot {
        requested_root: String,
        canonical_root: String,
    },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProjectAttachment {
    pub project_id: String,
    pub workspace_id: String,
    pub workspace_root: String,
    pub workspace_selection: Option<AttachProjectWorkspaceSelection>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SessionAttachment {
    pub project_id: String,
    pub workspace_id: String,
    pub workspace_root: String,
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AttachResponse {
    Project(ProjectAttachment),
    Session(SessionAttachment),
}

#[derive(Deserialize, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
enum AttachProjectWorkspaceSelectionWire {
    WorkspaceId {
        workspace_id: String,
    },
    WorkspaceRoot {
        requested_root: String,
        canonical_root: String,
    },
}

#[derive(Deserialize, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
enum AttachResponseWire {
    Project {
        project_id: String,
        workspace_id: String,
        workspace_root: String,
        #[serde(deserialize_with = "deserialize_required_attach_project_workspace_selection")]
        workspace_selection: Option<AttachProjectWorkspaceSelectionWire>,
    },
    Session {
        project_id: String,
        workspace_id: String,
        workspace_root: String,
        session_id: String,
    },
}

impl Serialize for AttachProjectWorkspaceSelection {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        let wire = match self {
            Self::WorkspaceId { workspace_id } => {
                validate_attach_field("workspace_id", workspace_id).map_err(S::Error::custom)?;
                AttachProjectWorkspaceSelectionWire::WorkspaceId {
                    workspace_id: workspace_id.clone(),
                }
            }
            Self::WorkspaceRoot {
                requested_root,
                canonical_root,
            } => {
                validate_attach_field("requested_root", requested_root)
                    .map_err(S::Error::custom)?;
                validate_attach_field("canonical_root", canonical_root)
                    .map_err(S::Error::custom)?;
                AttachProjectWorkspaceSelectionWire::WorkspaceRoot {
                    requested_root: requested_root.clone(),
                    canonical_root: canonical_root.clone(),
                }
            }
        };
        wire.serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for AttachProjectWorkspaceSelection {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        let wire = AttachProjectWorkspaceSelectionWire::deserialize(deserializer)?;
        let selection =
            attach_project_workspace_selection_from_wire(wire).map_err(D::Error::custom)?;
        Ok(selection)
    }
}

impl Serialize for AttachResponse {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        attach_response_to_wire(self)
            .map_err(S::Error::custom)?
            .serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for AttachResponse {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        attach_response_from_wire(AttachResponseWire::deserialize(deserializer)?)
            .map_err(D::Error::custom)
    }
}

fn deserialize_required_attach_project_workspace_selection<'de, D>(
    deserializer: D,
) -> Result<Option<AttachProjectWorkspaceSelectionWire>, D::Error>
where
    D: Deserializer<'de>,
{
    Option::<AttachProjectWorkspaceSelectionWire>::deserialize(deserializer)
}

fn attach_project_workspace_selection_from_wire(
    wire: AttachProjectWorkspaceSelectionWire,
) -> Result<AttachProjectWorkspaceSelection, String> {
    match wire {
        AttachProjectWorkspaceSelectionWire::WorkspaceId { workspace_id } => {
            validate_attach_field("workspace_id", &workspace_id)?;
            Ok(AttachProjectWorkspaceSelection::WorkspaceId { workspace_id })
        }
        AttachProjectWorkspaceSelectionWire::WorkspaceRoot {
            requested_root,
            canonical_root,
        } => {
            validate_attach_field("requested_root", &requested_root)?;
            validate_attach_field("canonical_root", &canonical_root)?;
            Ok(AttachProjectWorkspaceSelection::WorkspaceRoot {
                requested_root,
                canonical_root,
            })
        }
    }
}

fn attach_response_from_wire(wire: AttachResponseWire) -> Result<AttachResponse, String> {
    match wire {
        AttachResponseWire::Project {
            project_id,
            workspace_id,
            workspace_root,
            workspace_selection,
        } => {
            validate_attachment_binding(&project_id, &workspace_id, &workspace_root)?;
            let workspace_selection = workspace_selection
                .map(attach_project_workspace_selection_from_wire)
                .transpose()?;
            validate_project_attachment_selection(
                &workspace_id,
                &workspace_root,
                workspace_selection.as_ref(),
            )?;
            Ok(AttachResponse::Project(ProjectAttachment {
                project_id,
                workspace_id,
                workspace_root,
                workspace_selection,
            }))
        }
        AttachResponseWire::Session {
            project_id,
            workspace_id,
            workspace_root,
            session_id,
        } => {
            validate_attachment_binding(&project_id, &workspace_id, &workspace_root)?;
            validate_attach_field("session_id", &session_id)?;
            Ok(AttachResponse::Session(SessionAttachment {
                project_id,
                workspace_id,
                workspace_root,
                session_id,
            }))
        }
    }
}

fn attach_response_to_wire(response: &AttachResponse) -> Result<AttachResponseWire, String> {
    match response {
        AttachResponse::Project(project) => {
            validate_attachment_binding(
                &project.project_id,
                &project.workspace_id,
                &project.workspace_root,
            )?;
            validate_project_attachment_selection(
                &project.workspace_id,
                &project.workspace_root,
                project.workspace_selection.as_ref(),
            )?;
            let workspace_selection =
                project
                    .workspace_selection
                    .as_ref()
                    .map(|selection| match selection {
                        AttachProjectWorkspaceSelection::WorkspaceId { workspace_id } => {
                            AttachProjectWorkspaceSelectionWire::WorkspaceId {
                                workspace_id: workspace_id.clone(),
                            }
                        }
                        AttachProjectWorkspaceSelection::WorkspaceRoot {
                            requested_root,
                            canonical_root,
                        } => AttachProjectWorkspaceSelectionWire::WorkspaceRoot {
                            requested_root: requested_root.clone(),
                            canonical_root: canonical_root.clone(),
                        },
                    });
            Ok(AttachResponseWire::Project {
                project_id: project.project_id.clone(),
                workspace_id: project.workspace_id.clone(),
                workspace_root: project.workspace_root.clone(),
                workspace_selection,
            })
        }
        AttachResponse::Session(session) => {
            validate_attachment_binding(
                &session.project_id,
                &session.workspace_id,
                &session.workspace_root,
            )?;
            validate_attach_field("session_id", &session.session_id)?;
            Ok(AttachResponseWire::Session {
                project_id: session.project_id.clone(),
                workspace_id: session.workspace_id.clone(),
                workspace_root: session.workspace_root.clone(),
                session_id: session.session_id.clone(),
            })
        }
    }
}

fn validate_attachment_binding(
    project_id: &str,
    workspace_id: &str,
    workspace_root: &str,
) -> Result<(), String> {
    validate_attach_field("project_id", project_id)?;
    validate_attach_field("workspace_id", workspace_id)?;
    validate_attach_field("workspace_root", workspace_root)
}

fn validate_project_attachment_selection(
    workspace_id: &str,
    workspace_root: &str,
    selection: Option<&AttachProjectWorkspaceSelection>,
) -> Result<(), String> {
    match selection {
        None => Ok(()),
        Some(AttachProjectWorkspaceSelection::WorkspaceId {
            workspace_id: selected_workspace_id,
        }) => {
            validate_attach_field("workspace_id", selected_workspace_id)?;
            if selected_workspace_id == workspace_id {
                Ok(())
            } else {
                Err("project attachment workspace selection does not match workspace_id".to_owned())
            }
        }
        Some(AttachProjectWorkspaceSelection::WorkspaceRoot {
            requested_root,
            canonical_root,
        }) => {
            validate_attach_field("requested_root", requested_root)?;
            validate_attach_field("canonical_root", canonical_root)?;
            if canonical_root == workspace_root {
                Ok(())
            } else {
                Err(
                    "project attachment workspace selection does not match workspace_root"
                        .to_owned(),
                )
            }
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SubscribeResponse {
    pub stream: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct StreamCompleteParams {
    #[serde(default)]
    pub code: i32,
    #[serde(default)]
    pub message: String,
}
