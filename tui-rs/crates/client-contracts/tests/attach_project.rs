use client_contracts::protocol::{
    AttachProjectRequest, AttachProjectWorkspace, AttachProjectWorkspaceSelection, AttachResponse,
    AttachSessionRequest, ProjectAttachment, SessionAttachment,
};
use serde_json::json;

#[test]
fn attach_project_request_serializes_typed_workspace_selection() {
    let cases = [
        (
            AttachProjectRequest {
                project_id: "project-1".to_owned(),
                workspace: None,
            },
            json!({
                "project_id": "project-1",
                "workspace": null
            }),
        ),
        (
            AttachProjectRequest {
                project_id: "project-1".to_owned(),
                workspace: Some(AttachProjectWorkspace::id("workspace-1")),
            },
            json!({
                "project_id": "project-1",
                "workspace": {
                    "kind": "workspace_id",
                    "workspace_id": "workspace-1"
                }
            }),
        ),
        (
            AttachProjectRequest {
                project_id: "project-1".to_owned(),
                workspace: Some(AttachProjectWorkspace::root("/workspace")),
            },
            json!({
                "project_id": "project-1",
                "workspace": {
                    "kind": "workspace_root",
                    "workspace_root": "/workspace"
                }
            }),
        ),
    ];

    for (request, expected) in cases {
        assert_eq!(serde_json::to_value(request).unwrap(), expected);
    }
}

#[test]
fn attach_project_request_rejects_legacy_and_malformed_workspace_selection() {
    let malformed = [
        json!({"project_id": "project-1"}),
        json!({"project_id": "project-1", "workspace_id": "workspace-1"}),
        json!({"project_id": "project-1", "workspace_root": "/workspace"}),
        json!({"project_id": "project-1", "workspace": {"kind": "unknown"}}),
        json!({"project_id": "project-1", "workspace": {"kind": "workspace_id"}}),
        json!({"project_id": "project-1", "workspace": {"kind": "workspace_id", "workspace_id": " "}}),
        json!({"project_id": " project-1 ", "workspace": null}),
        json!({
            "project_id": "project-1",
            "workspace": {
                "kind": "workspace_id",
                "workspace_id": "workspace-1",
                "workspace_root": "/workspace"
            }
        }),
        json!({"project_id": "project-1", "workspace": {"kind": "workspace_root"}}),
        json!({"project_id": "project-1", "workspace": {"kind": "workspace_root", "workspace_root": " "}}),
        json!({
            "project_id": "project-1",
            "workspace": {
                "kind": "workspace_root",
                "workspace_root": "/workspace",
                "workspace_id": "workspace-1"
            }
        }),
    ];

    for value in malformed {
        assert!(
            serde_json::from_value::<AttachProjectRequest>(value.clone()).is_err(),
            "accepted malformed request: {value}"
        );
    }
}

#[test]
fn attach_project_request_rejects_blank_present_values_before_send() {
    let requests = [
        AttachProjectRequest {
            project_id: " ".to_owned(),
            workspace: None,
        },
        AttachProjectRequest {
            project_id: "project-1".to_owned(),
            workspace: Some(AttachProjectWorkspace::id(" ")),
        },
        AttachProjectRequest {
            project_id: "project-1".to_owned(),
            workspace: Some(AttachProjectWorkspace::root(" ")),
        },
    ];

    for request in requests {
        assert!(serde_json::to_value(request).is_err());
    }
}

#[test]
fn attach_response_round_trips_strict_project_and_session_variants() {
    let responses = [
        AttachResponse::Project(ProjectAttachment {
            project_id: "project-1".to_owned(),
            workspace_id: "workspace-1".to_owned(),
            workspace_root: "/workspace".to_owned(),
            workspace_selection: None,
        }),
        AttachResponse::Project(ProjectAttachment {
            project_id: "project-1".to_owned(),
            workspace_id: "workspace-1".to_owned(),
            workspace_root: "/canonical/workspace".to_owned(),
            workspace_selection: Some(AttachProjectWorkspaceSelection::WorkspaceRoot {
                requested_root: "/workspace-alias".to_owned(),
                canonical_root: "/canonical/workspace".to_owned(),
            }),
        }),
        AttachResponse::Session(SessionAttachment {
            project_id: "project-1".to_owned(),
            workspace_id: "workspace-1".to_owned(),
            workspace_root: "/workspace".to_owned(),
            session_id: "session-1".to_owned(),
        }),
    ];

    for response in responses {
        let encoded = serde_json::to_value(&response).unwrap();
        let decoded: AttachResponse = serde_json::from_value(encoded).unwrap();
        assert_eq!(decoded, response);
    }
}

#[test]
fn attach_response_rejects_malformed_or_inconsistent_variants() {
    let malformed = [
        json!({}),
        json!({"kind": "unknown"}),
        json!({
            "kind": "project",
            "project_id": "project-1",
            "workspace_id": "workspace-1",
            "workspace_root": "/workspace"
        }),
        json!({
            "kind": "project",
            "project_id": "project-1",
            "workspace_id": "workspace-1",
            "workspace_root": "/workspace",
            "workspace_selection": {
                "kind": "workspace_id",
                "workspace_id": "workspace-2"
            }
        }),
        json!({
            "kind": "project",
            "project_id": "project-1",
            "workspace_id": "workspace-1",
            "workspace_root": "/workspace",
            "workspace_selection": {
                "kind": "workspace_root",
                "requested_root": "/alias",
                "canonical_root": "/other"
            }
        }),
        json!({
            "kind": "session",
            "project_id": "project-1",
            "workspace_id": "workspace-1",
            "workspace_root": "/workspace"
        }),
        json!({
            "kind": "session",
            "project_id": "project-1",
            "workspace_id": "workspace-1",
            "workspace_root": "/workspace",
            "session_id": " ",
        }),
        json!({
            "kind": "session",
            "project_id": "project-1",
            "workspace_id": "workspace-1",
            "workspace_root": "/workspace",
            "session_id": "session-1",
            "workspace_selection": null
        }),
    ];

    for value in malformed {
        assert!(
            serde_json::from_value::<AttachResponse>(value.clone()).is_err(),
            "accepted malformed response: {value}"
        );
    }
}

#[test]
fn attach_session_request_rejects_missing_or_blank_session_id() {
    let malformed = [
        json!({}),
        json!({"session_id": " "}),
        json!({"session_id": " session-1 "}),
        json!({"session_id": "session-1", "extra": true}),
    ];
    for value in malformed {
        assert!(serde_json::from_value::<AttachSessionRequest>(value).is_err());
    }
    assert!(
        serde_json::to_value(AttachSessionRequest {
            session_id: " ".to_owned(),
        })
        .is_err()
    );
}
