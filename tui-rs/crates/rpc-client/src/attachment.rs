use client_contracts::protocol::{
    AttachProjectRequest, AttachProjectWorkspace, AttachProjectWorkspaceSelection, AttachResponse,
    ProjectAttachment, SessionAttachment,
};

use crate::error::RpcError;

#[derive(Default)]
pub(crate) struct ProjectAttachmentAuthority {
    attachment: Option<ProjectAttachment>,
}

impl ProjectAttachmentAuthority {
    pub(crate) fn accept(
        &mut self,
        request: &AttachProjectRequest,
        response: AttachResponse,
    ) -> Result<(), RpcError> {
        let attachment = validate_project_attachment_response(request, response)?;
        if let Some(expected) = &self.attachment {
            if expected != &attachment {
                return Err(RpcError::Decode(
                    "remote reconnect project attachment changed".to_owned(),
                ));
            }
        } else {
            self.attachment = Some(attachment);
        }
        Ok(())
    }

    pub(crate) fn get(&self) -> Option<&ProjectAttachment> {
        self.attachment.as_ref()
    }
}

pub(crate) fn validate_project_attachment_response(
    request: &AttachProjectRequest,
    response: AttachResponse,
) -> Result<ProjectAttachment, RpcError> {
    let AttachResponse::Project(attachment) = response else {
        return Err(RpcError::Decode(
            "project attach returned a non-project attachment".to_owned(),
        ));
    };
    if attachment.project_id != request.project_id {
        return Err(RpcError::Decode(
            "project attach returned a different project".to_owned(),
        ));
    }
    let selector_matches = match (&request.workspace, &attachment.workspace_selection) {
        (None, None) => true,
        (
            Some(AttachProjectWorkspace::WorkspaceId {
                workspace_id: requested,
            }),
            Some(AttachProjectWorkspaceSelection::WorkspaceId {
                workspace_id: returned,
            }),
        ) => requested == returned,
        (
            Some(AttachProjectWorkspace::WorkspaceRoot {
                workspace_root: requested,
            }),
            Some(AttachProjectWorkspaceSelection::WorkspaceRoot {
                requested_root: returned,
                ..
            }),
        ) => requested == returned,
        _ => false,
    };
    if !selector_matches {
        return Err(RpcError::Decode(
            "project attach returned a different workspace selection".to_owned(),
        ));
    }
    Ok(attachment)
}

pub(crate) fn validate_session_attachment_response(
    session_id: &str,
    project_attachment: Option<&ProjectAttachment>,
    response: AttachResponse,
) -> Result<SessionAttachment, RpcError> {
    let AttachResponse::Session(attachment) = response else {
        return Err(RpcError::Decode(
            "session attach returned a non-session attachment".to_owned(),
        ));
    };
    if attachment.session_id != session_id {
        return Err(RpcError::Decode(
            "session attach returned a different session".to_owned(),
        ));
    }
    if let Some(project) = project_attachment
        && attachment.project_id != project.project_id
    {
        return Err(RpcError::Decode(
            "session attach returned a session outside the attached project".to_owned(),
        ));
    }
    Ok(attachment)
}
