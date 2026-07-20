use std::cell::{Cell, RefCell};
use std::collections::VecDeque;
use std::rc::Rc;

use client_contracts::auth::{
    AuthBootstrapMode, AuthCompleteBootstrapRequest, AuthCompleteBootstrapResponse,
    AuthGetBootstrapStatusResponse, AuthStatusResponse,
};
use client_contracts::clientui::SessionExecutionTarget;
use client_contracts::project::{
    ProjectAttachWorkspaceRequest, ProjectAttachWorkspaceResponse, ProjectAvailability,
    ProjectBindingPlanMode, ProjectBindingPlanRequest, ProjectBindingPlanResponse, ProjectBinding,
    ProjectCreateRequest, ProjectCreateResponse, ProjectGetOverviewResponse, ProjectOverview,
    ProjectSummary,
};
use client_contracts::process::{
    ProcessInlineOutputRequest, ProcessInlineOutputResponse, ProcessKillRequest,
    ProcessListRequest, ProcessListResponse,
};
use client_contracts::prompt::{ApprovalAnswerRequest, AskAnswerRequest};
use client_contracts::protocol::{
    AttachProjectRequest, AttachProjectWorkspace, CapabilityFlags, HandshakeRequest,
    HandshakeResponse, ServerIdentity,
};
use client_contracts::runtime_control::{
    RuntimeAppendCommittedEntryRequest, RuntimeCompactContextRequest,
    RuntimeDiscardQueuedUserMessageRequest,
    RuntimeDiscardQueuedUserMessageResponse, RuntimeHasQueuedUserWorkRequest,
    RuntimeHasQueuedUserWorkResponse, RuntimeGoal, RuntimeGoalClearRequest,
    RuntimeGoalSetRequest, RuntimeGoalShowRequest, RuntimeGoalShowResponse,
    RuntimeGoalStatusRequest, RuntimeInterruptRequest, RuntimeQueueUserMessageRequest,
    RuntimeQueueUserMessageResponse, RuntimeSubmitQueuedUserMessagesRequest,
    RuntimeSubmitQueuedUserMessagesResponse,
    RuntimeRecordPromptHistoryRequest, RuntimeSetAutoCompactionEnabledRequest,
    RuntimeSetAutoCompactionEnabledResponse, RuntimeSetFastModeEnabledRequest,
    RuntimeSetFastModeEnabledResponse, RuntimeSetQuestionsEnabledRequest,
    RuntimeSetQuestionsEnabledResponse, RuntimeSetReviewerEnabledRequest,
    RuntimeSetReviewerEnabledResponse, RuntimeSetSessionNameRequest,
    RuntimeSetThinkingLevelRequest, RuntimeSubmitUserShellCommandRequest,
    RuntimeSubmitUserTurnRequest, RuntimeSubmitUserTurnResponse,
};
use client_contracts::session::{
    RunPromptOverrides, SessionAuthPreparation, SessionCreateOrigin, SessionInitialInputPolicy,
    SessionInitialInputRequest, SessionInitialInputResponse, SessionLaunchIntent,
    SessionLaunchMode, SessionLaunchPreparation, SessionMainViewRequest, SessionMainViewResponse,
    SessionNavigationBinding, SessionPersistInputDraftRequest, SessionPersistInputDraftResponse,
    SessionPlanRequest, SessionResolveTransitionRequest, SessionResolveTransitionResponse,
    SessionRetargetWorkspaceRequest, SessionRetargetWorkspaceResponse,
    SessionRuntimeActivateRequest, SessionRuntimeActivateResponse, SessionRuntimeAttachment,
    SessionRuntimeReleaseClosePolicy, SessionRuntimeReleaseRequest, SessionRuntimeReleaseResponse,
    SessionTransition, SessionTransitionAction, SessionTranscriptPageRequest,
    SessionTranscriptPageResponse,
};
use client_contracts::worktree::{
    WorktreeCreateRequest, WorktreeCreateResponse, WorktreeCreateTargetResolution,
    WorktreeCreateTargetResolutionKind, WorktreeCreateTargetResolveRequest,
    WorktreeCreateTargetResolveResponse, WorktreeDeleteRequest, WorktreeDeleteResponse,
    WorktreeListRequest, WorktreeListResponse, WorktreeSwitchRequest, WorktreeSwitchResponse,
    WorktreeView,
};
use rpc_client::api::{Client, RemoteClient, RemoteContext};
use rpc_client::error::RpcError;
use rpc_client::json_rpc::{CallCancellation, JsonRpcConnection};
use rpc_client::transport::{ConnectionFactory, ConnectionKind, FrameConnection, TransportError};
use rpc_client::wire::{ErrorCode, Frame, JSONRPC_VERSION, Response};
use serde_json::json;

#[test]
fn handshake_uses_fixed_request_id_and_decodes_response() {
    let response = HandshakeResponse {
        identity: ServerIdentity {
            protocol_version: "2026-05-31".to_owned(),
            server_id: "server-1".to_owned(),
            pid: 123,
            persistence_root_id: String::new(),
            capabilities: capabilities(),
        },
    };
    let connection = ScriptedConnection::new(vec![Frame::from_response(Response {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: "handshake".to_owned(),
        result: Some(serde_json::to_value(&response).unwrap()),
        error: None,
    })]);
    let mut client = Client::new(connection);

    let actual = client
        .handshake(HandshakeRequest {
            protocol_version: "2026-05-31".to_owned(),
        })
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(actual, response);
    assert_eq!(connection.sent.len(), 1);
    assert_eq!(
        serde_json::to_value(connection.sent[0].request()).unwrap(),
        json!({
            "jsonrpc": "2.0",
            "id": "handshake",
            "method": "protocol.handshake",
            "params": {
                "protocol_version": "2026-05-31"
            }
        })
    );
}

#[test]
fn project_scoped_connection_setup_orders_handshake_before_attach() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-project",
            project_attach_response_for_root(
                "/tmp/workspace",
                "workspace-1",
                "/tmp/workspace",
            ),
        ),
    ]);
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::project("project-1", Some(AttachProjectWorkspace::root("/tmp/workspace"))),
    );

    let connection = remote.open_project_connection().unwrap();
    let factory = remote.into_factory();

    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &connection.sent,
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
        ],
    );
    assert_eq!(
        connection.sent[1].request().params.unwrap(),
        json!({
            "project_id": "project-1",
            "workspace": {
                "kind": "workspace_root",
                "workspace_root": "/tmp/workspace"
            }
        })
    );
}

#[test]
fn project_scoped_connection_selects_workspace_by_id() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-project",
            project_attach_response_for_id("workspace-1", "/tmp/workspace"),
        ),
    ]);
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::project("project-1", Some(AttachProjectWorkspace::id("workspace-1"))),
    );

    let connection = remote.open_project_connection().unwrap();

    assert_eq!(
        connection.sent[1].request().params.unwrap(),
        json!({
            "project_id": "project-1",
            "workspace": {
                "kind": "workspace_id",
                "workspace_id": "workspace-1"
            }
        })
    );
}

#[test]
fn project_scoped_connection_rejects_substituted_root_attachment() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-project",
            project_attach_response_for_root(
                "/workspace-b",
                "workspace-b",
                "/canonical/workspace-b",
            ),
        ),
    ]);
    let close_count = connection.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::project(
            "project-1",
            Some(AttachProjectWorkspace::root("/workspace-a")),
        ),
    );

    assert!(matches!(
        remote.open_project_connection(),
        Err(RpcError::Decode(_))
    ));
    assert_eq!(close_count.get(), 1);
}

#[test]
fn project_scoped_connection_rejects_changed_reconnect_binding() {
    let first = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-project",
            project_attach_response_for_root(
                "/workspace-a",
                "workspace-a",
                "/canonical/workspace-a",
            ),
        ),
    ]);
    let second = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-project",
            project_attach_response_for_root(
                "/workspace-a",
                "workspace-b",
                "/canonical/workspace-b",
            ),
        ),
    ]);
    let second_close_count = second.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![first, second]),
        RemoteContext::project(
            "project-1",
            Some(AttachProjectWorkspace::root("/workspace-a")),
        ),
    );

    let mut first_connection = remote.open_project_connection().unwrap();
    first_connection.close().unwrap();
    assert!(matches!(
        remote.open_project_connection(),
        Err(RpcError::Decode(_))
    ));
    assert_eq!(second_close_count.get(), 1);
}

#[test]
fn project_scoped_connection_rejects_malformed_or_wrong_kind_attachment() {
    let malformed = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-project",
            json!({
                "kind": "project",
                "project_id": "project-1",
                "workspace_id": "workspace-1",
                "workspace_root": "/workspace"
            }),
        ),
    ]);
    let wrong_kind = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response("attach-project", session_attach_response("session-1")),
    ]);
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![malformed, wrong_kind]),
        RemoteContext::project("project-1", None),
    );

    assert!(matches!(
        remote.open_project_connection(),
        Err(RpcError::Decode(_))
    ));
    assert!(matches!(
        remote.open_project_connection(),
        Err(RpcError::Decode(_))
    ));
}

#[test]
fn session_connection_rejects_substituted_session_attachment() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response("attach-session", session_attach_response("session-2")),
    ]);
    let close_count = connection.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::unscoped(),
    );

    assert!(matches!(
        remote.subscribe_raw(
            "session-1",
            subscription_route(),
            json!({"session_id": "session-1"}),
        ),
        Err(RpcError::Decode(_))
    ));
    assert_eq!(close_count.get(), 1);
}

#[test]
fn session_connection_rejects_session_outside_attached_project() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response("attach-project", project_attach_response()),
        success_response(
            "attach-session",
            json!({
                "kind": "session",
                "project_id": "project-2",
                "workspace_id": "workspace-2",
                "workspace_root": "/workspace-2",
                "session_id": "session-1"
            }),
        ),
    ]);
    let close_count = connection.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::project("project-1", None),
    );

    assert!(matches!(
        remote.subscribe_raw(
            "session-1",
            subscription_route(),
            json!({"session_id": "session-1"}),
        ),
        Err(RpcError::Decode(_))
    ));
    assert_eq!(close_count.get(), 1);
}

#[test]
fn session_subscription_setup_attaches_session_before_subscribe() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-project",
            project_attach_response(),
        ),
        success_response(
            "attach-session",
            session_attach_response("session-1"),
        ),
        success_response("subscribe-sample", json!({"stream":"session-1"})),
    ]);
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::project("project-1", None),
    );

    let (connection, stream_id) = remote
        .open_session_subscription(
            "session-1",
            "subscribe-sample",
            "sample.subscribe",
            json!({"session_id":"session-1"}),
        )
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(stream_id, "session-1");
    assert_eq!(factory.opened, vec![ConnectionKind::Subscription]);
    assert_sent_methods(
        &connection.sent,
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("attach-session", "session.attach"),
            ("subscribe-sample", "sample.subscribe"),
        ],
    );
    assert_eq!(
        connection.sent[1].request().params,
        Some(json!({
            "project_id": "project-1",
            "workspace": null
        }))
    );
    assert_eq!(
        connection.sent[2].request().params.unwrap(),
        json!({"session_id":"session-1"})
    );
}

#[test]
fn dedicated_call_uses_fixed_route_request_id_on_dedicated_connection() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-project",
            project_attach_response(),
        ),
        success_response("runtime-interrupt", json!({})),
    ]);
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::project("project-1", None),
    );

    let (response, connection) = remote
        .call_dedicated(
            "runtime-interrupt",
            "runtime.interrupt",
            json!({"session_id":"session-1"}),
        )
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(response, json!({}));
    assert_eq!(factory.opened, vec![ConnectionKind::Dedicated]);
    assert_sent_methods(
        &connection.sent,
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("runtime-interrupt", "runtime.interrupt"),
        ],
    );
}

#[test]
fn dedicated_call_ignores_unrelated_response_ids_before_fixed_response() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response("other-id", json!({"ignored":true})),
        success_response("runtime-interrupt", json!({})),
    ]);
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::unscoped(),
    );

    let (response, connection) = remote
        .call_dedicated("runtime-interrupt", "runtime.interrupt", json!({}))
        .unwrap();

    assert_eq!(response, json!({}));
    assert_sent_methods(
        &connection.sent,
        &[
            ("handshake", "protocol.handshake"),
            ("runtime-interrupt", "runtime.interrupt"),
        ],
    );
}

#[test]
fn runtime_interrupt_uses_dedicated_connection_and_fixed_request_id() {
    let interrupt_log = Rc::new(RefCell::new(Vec::new()));
    let interrupt_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("runtime-interrupt", json!({})),
        ],
        interrupt_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![interrupt_connection]),
        RemoteContext::project("project-1", None),
    );

    remote
        .interrupt_runtime(RuntimeInterruptRequest {
            client_request_id: "request-interrupt".to_owned(),
            session_id: "session-1".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(factory.opened, vec![ConnectionKind::Dedicated]);
    assert_sent_methods(
        &interrupt_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("runtime-interrupt", "runtime.interrupt"),
        ],
    );
}

#[test]
fn startup_auth_wrappers_use_phase_four_contract_dtos_and_method_names() {
    let connection = ScriptedConnection::new(vec![
        success_response(
            "rpc-1",
            AuthGetBootstrapStatusResponse {
                auth_ready: false,
                auth_required: true,
                auth_bootstrap_supported: true,
                allowed_pre_auth_methods: vec!["auth.completeBootstrap".to_owned()],
                supported_modes: vec![AuthBootstrapMode::ApiKey],
                oauth: Default::default(),
            },
        ),
        success_response(
            "rpc-2",
            AuthCompleteBootstrapResponse {
                auth_ready: true,
                method_type: "api_key".to_owned(),
                account_id: "acct-1".to_owned(),
                email: "nek@example.com".to_owned(),
            },
        ),
    ]);
    let mut client = Client::new(connection);

    let status = client.get_auth_bootstrap_status().unwrap();
    let complete = client
        .complete_auth_bootstrap(AuthCompleteBootstrapRequest {
            mode: AuthBootstrapMode::ApiKey,
            force: false,
            api_key: "sk-test".to_owned(),
            callback_input: String::new(),
            redirect_uri: String::new(),
            oauth_state: String::new(),
            oauth_code_verifier: String::new(),
            device_authorization_code: String::new(),
            device_code_verifier: String::new(),
        })
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(status.supported_modes, vec![AuthBootstrapMode::ApiKey]);
    assert!(complete.auth_ready);
    assert_sent_methods(
        &connection.sent,
        &[
            ("rpc-1", "auth.getBootstrapStatus"),
            ("rpc-2", "auth.completeBootstrap"),
        ],
    );
    assert_eq!(connection.sent[0].request().params.unwrap(), json!({}));
    assert_eq!(
        connection.sent[1].request().params.unwrap(),
        json!({
            "mode": "api_key",
            "api_key": "sk-test"
        })
    );
}

#[test]
fn auth_status_remote_call_uses_unscoped_control_even_with_project_context() {
    let sent_log = Rc::new(RefCell::new(Vec::new()));
    let connection = ScriptedConnection::with_sent_log(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "rpc-1",
            AuthStatusResponse {
                auth: client_contracts::auth::AuthStatusInfo {
                    summary: "user@example.com".to_owned(),
                    visible: true,
                    ..Default::default()
                },
                ..Default::default()
            },
        ),
    ], sent_log.clone());
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::project("project-1", Some(AttachProjectWorkspace::id("workspace-1"))),
    );

    let response = remote.get_auth_status().unwrap();
    let factory = remote.into_factory();

    assert_eq!(response.auth.summary, "user@example.com");
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    let sent = sent_log.borrow();
    assert_sent_methods(
        &sent,
        &[
            ("handshake", "protocol.handshake"),
            ("rpc-1", "auth.getStatus"),
        ],
    );
    assert_eq!(sent[1].request().params, Some(json!({})));
}

#[test]
fn startup_attach_wrappers_use_phase_four_contract_dtos_and_method_names() {
    let connection = ScriptedConnection::new(vec![
        success_response(
            "attach-project",
            project_attach_response_for_root(
                "/tmp/workspace",
                "workspace-1",
                "/tmp/workspace",
            ),
        ),
        success_response(
            "attach-session",
            session_attach_response("session-1"),
        ),
    ]);
    let mut client = Client::new(connection);

    let project_request = AttachProjectRequest {
        project_id: "project-1".to_owned(),
        workspace: Some(AttachProjectWorkspace::root("/tmp/workspace")),
    };
    let project = client.attach_project(&project_request).unwrap();
    let session = client.attach_session("session-1").unwrap();
    let connection = client.into_connection();

    assert_eq!(project.project_id, "project-1");
    assert_eq!(session.session_id, "session-1");
    assert_sent_methods(
        &connection.sent,
        &[
            ("attach-project", "project.attach"),
            ("attach-session", "session.attach"),
        ],
    );
}

#[test]
fn project_overview_wrapper_sends_contract_dto_and_method_name() {
    let response = ProjectGetOverviewResponse {
        overview: ProjectOverview {
            project: ProjectSummary {
                project_id: "project-1".to_owned(),
                project_key: "builder-core".to_owned(),
                display_name: "Builder Core".to_owned(),
                root_path: "/workspace".to_owned(),
                availability: ProjectAvailability::Available,
                session_count: 0,
                updated_at: "2026-05-31T12:00:00Z".to_owned(),
            },
            workspaces: Vec::new(),
            sessions: Vec::new(),
        },
    };
    let connection = ScriptedConnection::new(vec![success_response("rpc-1", response.clone())]);
    let mut client = Client::new(connection);

    let actual = client.get_project_overview(" project-1 ").unwrap();
    let connection = client.into_connection();

    assert_eq!(actual, response);
    assert_sent_methods(&connection.sent, &[("rpc-1", "project.getOverview")]);
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({"ProjectID":"project-1"})
    );
}

#[test]
fn project_binding_plan_wrapper_uses_startup_binding_route() {
    let response: ProjectBindingPlanResponse = serde_json::from_value(json!({
        "kind": "local_unbound",
        "canonical_root": "/work/builder",
        "path_availability": "available",
        "projects": []
    }))
    .unwrap();
    let connection = ScriptedConnection::new(vec![success_response("rpc-1", response.clone())]);
    let mut client = Client::new(connection);

    let actual = client
        .plan_workspace_binding(ProjectBindingPlanRequest {
            path: " /work/builder ".to_owned(),
            mode: ProjectBindingPlanMode::Interactive,
        })
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(actual, response);
    assert_sent_methods(
        &connection.sent,
        &[("rpc-1", "project.planWorkspaceBinding")],
    );
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({
            "path": " /work/builder ",
            "mode": "interactive"
        })
    );
}

#[test]
fn project_binding_mutation_wrappers_use_startup_binding_routes() {
    let binding = ProjectBinding {
        project_id: "project-1".to_owned(),
        project_key: "BLD".to_owned(),
        project_name: "Builder Core".to_owned(),
        workspace_id: "workspace-1".to_owned(),
        canonical_root: "/work/builder".to_owned(),
        workspace_name: "builder-main".to_owned(),
        workspace_status: "available".to_owned(),
    };
    let connection = ScriptedConnection::new(vec![
        success_response(
            "rpc-1",
            ProjectCreateResponse {
                binding: binding.clone(),
            },
        ),
        success_response(
            "rpc-2",
            ProjectAttachWorkspaceResponse {
                binding: binding.clone(),
            },
        ),
    ]);
    let mut client = Client::new(connection);

    let created = client
        .create_project(ProjectCreateRequest {
            display_name: "Builder Core".to_owned(),
            project_key: String::new(),
            workspace_root: "/work/builder".to_owned(),
        })
        .unwrap();
    let attached = client
        .attach_workspace_to_project(ProjectAttachWorkspaceRequest {
            project_id: "project-1".to_owned(),
            workspace_root: "/work/builder".to_owned(),
        })
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(created.binding, binding);
    assert_eq!(attached.binding.project_id, "project-1");
    assert_sent_methods(
        &connection.sent,
        &[
            ("rpc-1", "project.create"),
            ("rpc-2", "project.attachWorkspace"),
        ],
    );
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({
            "display_name": "Builder Core",
            "workspace_root": "/work/builder"
        })
    );
    assert_eq!(
        connection.sent[1].request().params.unwrap(),
        json!({
            "project_id": "project-1",
            "workspace_root": "/work/builder"
        })
    );
}

#[test]
fn session_plan_wrapper_sends_contract_dto_and_method_name() {
    let response = json!({
        "plan": {
            "session_id": "session-1",
            "active_settings": contract_settings_json(),
            "enabled_tool_ids": ["shell", "patch"],
            "source": contract_source_report_json()
        }
    });
    let connection = ScriptedConnection::new(vec![success_response("rpc-1", response)]);
    let mut client = Client::new(connection);

    let actual = client
        .plan_session(SessionPlanRequest {
            client_request_id: "request-1".to_owned(),
            mode: SessionLaunchMode::Interactive,
            intent: SessionLaunchIntent::OpenExisting {
                session_id: "session-1".to_owned(),
            },
            caller_session_id: None,
            overrides: RunPromptOverrides::default(),
        })
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(actual.plan.session_id, "session-1");
    assert_eq!(actual.plan.enabled_tool_ids, ["shell", "patch"]);
    assert_eq!(actual.plan.active_settings.max_subagent_depth, 2);
    assert_sent_methods(&connection.sent, &[("rpc-1", "session.plan")]);
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({
            "client_request_id": "request-1",
            "mode": "interactive",
            "intent": {
                "kind": "open_existing",
                "session_id": "session-1"
            },
            "overrides": {
                "model": "",
                "provider_override": "",
                "thinking_level": "",
                "theme": "",
                "model_timeout_seconds": 0,
                "tools": "",
                "openai_base_url": ""
            }
        })
    );
}

#[test]
fn session_plan_wrapper_serializes_parent_agent_origin_and_selector() {
    let connection = ScriptedConnection::new(vec![success_response(
        "rpc-1",
        json!({"plan": {
            "session_id": "session-1",
            "active_settings": contract_settings_json(),
            "source": contract_source_report_json()
        }}),
    )]);
    let mut client = Client::new(connection);

    client
        .plan_session(SessionPlanRequest {
            client_request_id: "request-1".to_owned(),
            mode: SessionLaunchMode::Headless,
            intent: SessionLaunchIntent::CreateNew {
                origin: SessionCreateOrigin::ParentAgent {
                    session_id: "parent-session".to_owned(),
                },
            },
            caller_session_id: Some("caller-session".to_owned()),
            overrides: RunPromptOverrides {
                agent_role: Some("worker".to_owned()),
                ..RunPromptOverrides::default()
            },
        })
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({
            "client_request_id": "request-1",
            "mode": "headless",
            "intent": {
                "kind": "create_new",
                "origin": {
                    "kind": "parent_agent",
                    "session_id": "parent-session"
                }
            },
            "caller_session_id": "caller-session",
            "overrides": {
                "agent_role": "worker",
                "model": "",
                "provider_override": "",
                "thinking_level": "",
                "theme": "",
                "model_timeout_seconds": 0,
                "tools": "",
                "openai_base_url": ""
            }
        })
    );
}

#[test]
fn session_plan_contract_rejects_malformed_unknown_mixed_and_legacy_intents() {
    let invalid = [
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"create_new"},"overrides":{}}),
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"unknown"},"overrides":{}}),
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"create_new","origin":{"kind":"independent","session_id":"mixed"}},"overrides":{}}),
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"create_new","origin":{"kind":"previous_session"}},"overrides":{}}),
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"open_existing","session_id":"target","origin":{"kind":"independent"}},"overrides":{}}),
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"create_new","parent_id":"legacy-parent"},"overrides":{}}),
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"create_new","origin":{"kind":"independent"}},"selected_session_id":"legacy","overrides":{}}),
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"create_new","origin":{"kind":"independent"}},"force_new_session":true,"overrides":{}}),
        json!({"client_request_id":"request-1","mode":"interactive","intent":{"kind":"create_new","origin":{"kind":"independent"}},"parent_session_id":"legacy","overrides":{}}),
    ];
    for raw in invalid {
        if serde_json::from_value::<SessionPlanRequest>(raw.clone()).is_ok() {
            panic!("session plan contract accepted invalid payload {raw}");
        }
    }
}

#[test]
fn session_launch_intent_serializes_every_typed_origin_variant() {
    let cases = [
        (
            SessionLaunchIntent::CreateNew {
                origin: SessionCreateOrigin::Independent,
            },
            json!({"kind":"create_new","origin":{"kind":"independent"}}),
        ),
        (
            SessionLaunchIntent::CreateNew {
                origin: SessionCreateOrigin::PreviousSession {
                    session_id: "previous-session".to_owned(),
                },
            },
            json!({"kind":"create_new","origin":{"kind":"previous_session","session_id":"previous-session"}}),
        ),
        (
            SessionLaunchIntent::CreateNew {
                origin: SessionCreateOrigin::ParentAgent {
                    session_id: "parent-agent-session".to_owned(),
                },
            },
            json!({"kind":"create_new","origin":{"kind":"parent_agent","session_id":"parent-agent-session"}}),
        ),
        (
            SessionLaunchIntent::OpenExisting {
                session_id: "target-session".to_owned(),
            },
            json!({"kind":"open_existing","session_id":"target-session"}),
        ),
    ];
    for (intent, expected) in cases {
        assert_eq!(serde_json::to_value(intent).unwrap(), expected);
    }
}

#[test]
fn session_lifecycle_contract_rejects_flat_unknown_and_malformed_shapes() {
    let invalid = [
        json!({"kind":"unknown"}),
        json!({"kind":"stop","should_continue":true}),
        json!({"kind":"select_session"}),
        json!({"kind":"select_session","auth":"unknown"}),
        json!({"kind":"launch"}),
        json!({"kind":"launch","intent":{"kind":"create_new","origin":{"kind":"independent"}}}),
        json!({"kind":"launch","intent":{"kind":"open_existing","session_id":"target"},"preparation":{"input_policy":{"kind":"unknown"},"auth":"keep_current_auth"}}),
        json!({"kind":"launch","intent":{"kind":"open_existing","session_id":"target"},"preparation":{"input_policy":{"kind":"override_stored_draft"},"auth":"keep_current_auth"}}),
        json!({"kind":"launch","intent":{"kind":"open_existing","session_id":"target"},"preparation":{"input_policy":{"kind":"restore_stored_draft","text":"mixed"},"auth":"keep_current_auth"}}),
        json!({"kind":"launch","intent":{"kind":"open_existing","session_id":"target"},"preparation":{"input_policy":{"kind":"restore_stored_draft"},"auth":"keep_current_auth","parent_session_id":"legacy"}}),
    ];
    for raw in invalid {
        if serde_json::from_value::<SessionResolveTransitionResponse>(raw.clone()).is_ok() {
            panic!("session lifecycle contract accepted invalid payload {raw}");
        }
    }

    assert_eq!(
        serde_json::from_value::<SessionResolveTransitionResponse>(json!({"kind":"stop"}))
            .unwrap(),
        SessionResolveTransitionResponse::Stop {}
    );
    assert_eq!(
        serde_json::from_value::<SessionResolveTransitionResponse>(
            json!({"kind":"select_session","auth":"reauthenticate"})
        )
        .unwrap(),
        SessionResolveTransitionResponse::SelectSession {
            auth: SessionAuthPreparation::Reauthenticate,
        }
    );
    assert_eq!(
        serde_json::from_value::<SessionInitialInputPolicy>(
            json!({"kind":"override_stored_draft","text":""})
        )
        .unwrap(),
        SessionInitialInputPolicy::OverrideStoredDraft {
            text: String::new(),
        }
    );
}

#[test]
fn runtime_activate_wrapper_sends_contract_dto_and_method_name() {
    let request_params = json!({
        "client_request_id": "request-1",
        "session_id": "session-1",
        "owner_id": "owner-1",
        "active_settings": contract_settings_json(),
        "enabled_tool_ids": ["shell", "patch"],
        "source": contract_source_report_json()
    });
    let request: SessionRuntimeActivateRequest =
        serde_json::from_value(request_params.clone()).unwrap();
    let connection = ScriptedConnection::new(vec![success_response(
        "rpc-1",
        json!({
            "attachment": {
                "session_id": "session-1",
                "generation": 7
            }
        }),
    )]);
    let mut client = Client::new(connection);

    let actual = client.activate_session_runtime(request).unwrap();
    let connection = client.into_connection();

    assert_eq!(
        actual,
        SessionRuntimeActivateResponse {
            attachment: SessionRuntimeAttachment {
                session_id: "session-1".to_owned(),
                generation: 7.try_into().unwrap(),
            },
        }
    );
    assert_sent_methods(&connection.sent, &[("rpc-1", "session.runtime.activate")]);
    assert_eq!(connection.sent[0].request().params.unwrap(), request_params);
    assert!(
        serde_json::from_value::<SessionRuntimeActivateResponse>(json!({
            "attachment": {
                "session_id": "session-1",
                "generation": 0
            }
        }))
        .is_err()
    );
}

#[test]
fn runtime_release_wrapper_sends_contract_dto_and_method_name() {
    let request_params = json!({
        "client_request_id": "request-1",
        "attachment": {
            "session_id": "session-1",
            "generation": 7
        },
        "drop_owner": true,
        "close_policy": "close_if_idle",
        "owner_id": "owner-1"
    });
    let request: SessionRuntimeReleaseRequest =
        serde_json::from_value(request_params.clone()).unwrap();
    let connection = ScriptedConnection::new(vec![success_response(
        "rpc-1",
        json!({"released": true, "active": false}),
    )]);
    let mut client = Client::new(connection);

    let actual = client.release_session_runtime(request).unwrap();
    let connection = client.into_connection();

    assert_eq!(
        actual,
        SessionRuntimeReleaseResponse {
            released: true,
            active: false,
        }
    );
    assert_sent_methods(&connection.sent, &[("rpc-1", "session.runtime.release")]);
    assert_eq!(connection.sent[0].request().params.unwrap(), request_params);
    assert_eq!(
        serde_json::from_value::<SessionRuntimeReleaseClosePolicy>(json!("detach_only")).unwrap(),
        SessionRuntimeReleaseClosePolicy::DetachOnly
    );
    let detach_only_request: SessionRuntimeReleaseRequest = serde_json::from_value(json!({
        "client_request_id": "request-2",
        "attachment": {
            "session_id": "session-1",
            "generation": 8
        }
    }))
    .unwrap();
    assert_eq!(detach_only_request.close_policy, None);
    assert_eq!(
        serde_json::to_value(detach_only_request).unwrap(),
        json!({
            "client_request_id": "request-2",
            "attachment": {
                "session_id": "session-1",
                "generation": 8
            }
        })
    );
    assert!(
        serde_json::from_value::<SessionRuntimeReleaseRequest>(json!({
            "client_request_id": "request-1",
            "attachment": {
                "session_id": "session-1",
                "generation": 7
            },
            "only_if_idle": true
        }))
        .is_err()
    );
    assert!(
        serde_json::from_value::<SessionRuntimeReleaseResponse>(json!({
            "released": true,
            "attachment": {
                "session_id": "session-1",
                "generation": 7
            }
        }))
        .is_err()
    );
}

#[test]
fn session_retarget_workspace_wrapper_uses_startup_binding_route() {
    let binding = ProjectBinding {
        project_id: "project-1".to_owned(),
        project_key: "BLD".to_owned(),
        project_name: "Builder Core".to_owned(),
        workspace_id: "workspace-1".to_owned(),
        canonical_root: "/work/builder".to_owned(),
        workspace_name: "builder-main".to_owned(),
        workspace_status: "available".to_owned(),
    };
    let connection = ScriptedConnection::new(vec![success_response(
        "rpc-1",
        SessionRetargetWorkspaceResponse {
            binding: binding.clone(),
            workspace_binding_created: true,
        },
    )]);
    let mut client = Client::new(connection);

    let actual = client
        .retarget_session_workspace(SessionRetargetWorkspaceRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            workspace_root: "/work/builder".to_owned(),
            project_id: Some("project-1".to_owned()),
        })
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(actual.binding, binding);
    assert!(actual.workspace_binding_created);
    assert_sent_methods(&connection.sent, &[("rpc-1", "session.retargetWorkspace")]);
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({
            "client_request_id": "request-1",
            "session_id": "session-1",
            "workspace_root": "/work/builder",
            "project_id": "project-1"
        })
    );
}

#[test]
fn session_resolve_transition_uses_route_method_and_preserves_fork_fields() {
    let response = SessionResolveTransitionResponse::Launch {
        intent: SessionLaunchIntent::OpenExisting {
            session_id: "session-fork-1".to_owned(),
        },
        preparation: SessionLaunchPreparation {
            initial_prompt: Some(client_contracts::session::SessionInitialPromptMetadata {
                text: "edited rollback prompt".to_owned(),
                history_recorded: false,
            }),
            input_policy: SessionInitialInputPolicy::RestoreStoredDraft {},
            auth: SessionAuthPreparation::KeepCurrentAuth,
            navigation_binding: Some(SessionNavigationBinding {
                project_id: "project-target".to_owned(),
                workspace_id: "workspace-target".to_owned(),
            }),
        },
    };
    let connection = ScriptedConnection::new(vec![success_response("rpc-1", response.clone())]);
    let mut client = Client::new(connection);

    let actual = client
        .resolve_session_transition(fork_rollback_transition_request())
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(actual, response);
    assert_sent_methods(&connection.sent, &[("rpc-1", "session.resolveTransition")]);
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({
            "client_request_id": "request-1",
            "session_id": "session-1",
            "transition": {
                "action": "fork_rollback",
                "initial_prompt": "edited rollback prompt",
                "fork_rollback_target_id": "rollback-target-2"
            }
        })
    );
}

#[test]
fn session_initial_input_and_persist_draft_wrappers_use_lifecycle_routes() {
    let connection = ScriptedConnection::new(vec![
        success_response(
            "rpc-1",
            SessionInitialInputResponse {
                input: "saved draft".to_owned(),
            },
        ),
        success_response("rpc-2", SessionPersistInputDraftResponse {}),
    ]);
    let mut client = Client::new(connection);

    let initial = client
        .get_session_initial_input(SessionInitialInputRequest {
            session_id: "session-1".to_owned(),
            transition_input: "fallback draft".to_owned(),
        })
        .unwrap();
    client
        .persist_session_input_draft(SessionPersistInputDraftRequest {
            client_request_id: "persist-1".to_owned(),
            session_id: "session-1".to_owned(),
            input: String::new(),
        })
        .unwrap();
    let connection = client.into_connection();

    assert_eq!(initial.input, "saved draft");
    assert_sent_methods(
        &connection.sent,
        &[
            ("rpc-1", "session.getInitialInput"),
            ("rpc-2", "session.persistInputDraft"),
        ],
    );
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({
            "session_id": "session-1",
            "transition_input": "fallback draft"
        })
    );
    assert_eq!(
        connection.sent[1].request().params.unwrap(),
        json!({
            "client_request_id": "persist-1",
            "session_id": "session-1",
        })
    );
}

#[test]
fn remote_resolve_session_transition_uses_control_connection_and_typed_shape() {
    let sent_log = Rc::new(RefCell::new(Vec::new()));
    let response = SessionResolveTransitionResponse::Launch {
        intent: SessionLaunchIntent::OpenExisting {
            session_id: "session-fork-1".to_owned(),
        },
        preparation: SessionLaunchPreparation {
            initial_prompt: Some(client_contracts::session::SessionInitialPromptMetadata {
                text: "edited rollback prompt".to_owned(),
                history_recorded: false,
            }),
            input_policy: SessionInitialInputPolicy::RestoreStoredDraft {},
            auth: SessionAuthPreparation::KeepCurrentAuth,
            navigation_binding: None,
        },
    };
    let connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", response.clone()),
        ],
        sent_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::project("project-1", None),
    );

    let actual = remote
        .resolve_session_transition(fork_rollback_transition_request())
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(actual, response);
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    let sent = sent_log.borrow();
    assert_sent_methods(
        &sent,
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "session.resolveTransition"),
        ],
    );
    assert_eq!(
        sent[2].request().params.unwrap(),
        json!({
            "client_request_id": "request-1",
            "session_id": "session-1",
            "transition": {
                "action": "fork_rollback",
                "initial_prompt": "edited rollback prompt",
                "fork_rollback_target_id": "rollback-target-2"
            }
        })
    );
}

#[test]
fn transcript_page_wrapper_uses_contract_dto_and_method_name() {
    let response: SessionTranscriptPageResponse = serde_json::from_value(json!({
        "transcript": {
            "SessionID": "session-1",
            "SessionName": "Demo Session",
            "ConversationFreshness": 1,
            "OlderCursor": 4096,
            "HasMoreAbove": true,
            "NewerCursor": 8192,
            "HasMoreBelow": true,
            "Entries": contract_transcript_rows_json()
        }
    }))
    .unwrap();
    let connection = ScriptedConnection::new(vec![success_response("rpc-1", response.clone())]);
    let mut client = Client::new(connection);
    let request = SessionTranscriptPageRequest {
        session_id: "session-1".to_owned(),
        cursor: Some(42),
        newer_cursor: None,
    };

    let actual = client.get_transcript_page(request).unwrap();
    let connection = client.into_connection();

    assert_eq!(actual, response);
    assert_sent_methods(&connection.sent, &[("rpc-1", "session.getTranscriptPage")]);
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({"session_id": "session-1", "cursor": 42})
    );
}

#[test]
fn main_view_wrapper_uses_contract_dto_and_method_name() {
    let response: SessionMainViewResponse =
        serde_json::from_value(zero_main_view_json()).unwrap();
    let connection = ScriptedConnection::new(vec![success_response("rpc-1", response.clone())]);
    let mut client = Client::new(connection);
    let request = SessionMainViewRequest {
        session_id: "session-1".to_owned(),
    };

    let actual = client.get_main_view(request).unwrap();
    let connection = client.into_connection();

    assert_eq!(actual, response);
    assert!(!actual.main_view.status.questions_enabled);
    assert_sent_methods(&connection.sent, &[("rpc-1", "session.getMainView")]);
    assert_eq!(
        connection.sent[0].request().params.unwrap(),
        json!({"SessionID": "session-1"})
    );
}

#[test]
fn runtime_control_submit_routes_use_dedicated_connections_and_fixed_ids() {
    let turn_log = Rc::new(RefCell::new(Vec::new()));
    let shell_log = Rc::new(RefCell::new(Vec::new()));
    let turn_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response(
                "runtime-submit-user-turn",
                RuntimeSubmitUserTurnResponse {
                    message: "assistant reply".to_owned(),
                    compacted: true,
                },
            ),
        ],
        turn_log.clone(),
    );
    let shell_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("runtime-submit-user-shell-command", json!({})),
        ],
        shell_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![turn_connection, shell_connection]),
        RemoteContext::project("project-1", None),
    );

    let turn_response = remote
        .submit_runtime_user_turn(RuntimeSubmitUserTurnRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            text: "hello".to_owned(),
            prompt_history_recorded: true,
        })
        .unwrap();
    remote
        .submit_runtime_user_shell_command(RuntimeSubmitUserShellCommandRequest {
            client_request_id: "request-2".to_owned(),
            session_id: "session-1".to_owned(),
            command: "pwd".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(
        turn_response,
        RuntimeSubmitUserTurnResponse {
            message: "assistant reply".to_owned(),
            compacted: true,
        }
    );
    assert_eq!(
        factory.opened,
        vec![ConnectionKind::Dedicated, ConnectionKind::Dedicated]
    );
    assert_sent_methods(
        &turn_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("runtime-submit-user-turn", "runtime.submitUserTurn"),
        ],
    );
    assert_eq!(
        turn_log.borrow()[2].request().params.unwrap(),
        json!({
            "client_request_id": "request-1",
            "session_id": "session-1",
            "text": "hello",
            "prompt_history_recorded": true,
        })
    );
    assert_sent_methods(
        &shell_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            (
                "runtime-submit-user-shell-command",
                "runtime.submitUserShellCommand",
            ),
        ],
    );
}

#[test]
fn runtime_compact_context_uses_dedicated_connection_and_fixed_request_id() {
    let compact_log = Rc::new(RefCell::new(Vec::new()));
    let compact_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("runtime-compact-context", json!({})),
        ],
        compact_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![compact_connection]),
        RemoteContext::project("project-1", None),
    );

    remote
        .compact_runtime_context(RuntimeCompactContextRequest {
            client_request_id: "request-compact".to_owned(),
            session_id: "session-1".to_owned(),
            args: "keep API details".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(factory.opened, vec![ConnectionKind::Dedicated]);
    assert_sent_methods(
        &compact_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("runtime-compact-context", "runtime.compactContext"),
        ],
    );
    assert_eq!(
        compact_log.borrow()[2].request().params.unwrap(),
        json!({
            "client_request_id": "request-compact",
            "session_id": "session-1",
            "args": "keep API details",
        })
    );
}

#[test]
fn runtime_control_queue_routes_use_control_connections_and_generated_ids() {
    let queue_log = Rc::new(RefCell::new(Vec::new()));
    let discard_log = Rc::new(RefCell::new(Vec::new()));
    let history_log = Rc::new(RefCell::new(Vec::new()));
    let queue_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response(
                "rpc-1",
                RuntimeQueueUserMessageResponse {
                    queue_item_id: "queue-1".to_owned(),
                    text: "queued".to_owned(),
                },
            ),
        ],
        queue_log.clone(),
    );
    let discard_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response(
                "rpc-1",
                RuntimeDiscardQueuedUserMessageResponse { discarded: true },
            ),
        ],
        discard_log.clone(),
    );
    let history_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({})),
        ],
        history_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![
            queue_connection,
            discard_connection,
            history_connection,
        ]),
        RemoteContext::project("project-1", None),
    );

    let queued = remote
        .queue_runtime_user_message(RuntimeQueueUserMessageRequest {
            client_request_id: "request-3".to_owned(),
            session_id: "session-1".to_owned(),
            text: "queued".to_owned(),
        })
        .unwrap();
    let discarded = remote
        .discard_runtime_queued_user_message(RuntimeDiscardQueuedUserMessageRequest {
            client_request_id: "request-4".to_owned(),
            session_id: "session-1".to_owned(),
            queue_item_id: "queue-1".to_owned(),
        })
        .unwrap();
    remote
        .record_runtime_prompt_history(RuntimeRecordPromptHistoryRequest {
            client_request_id: "request-5".to_owned(),
            session_id: "session-1".to_owned(),
            text: "/status".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(
        queued,
        RuntimeQueueUserMessageResponse {
            queue_item_id: "queue-1".to_owned(),
            text: "queued".to_owned(),
        }
    );
    assert!(discarded.discarded);
    assert_eq!(
        factory.opened,
        vec![
            ConnectionKind::Control,
            ConnectionKind::Control,
            ConnectionKind::Control,
        ]
    );
    assert_sent_methods(
        &queue_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.queueUserMessage"),
        ],
    );
    assert_sent_methods(
        &discard_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.discardQueuedUserMessage"),
        ],
    );
    assert_sent_methods(
        &history_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.recordPromptHistory"),
        ],
    );
}

#[test]
fn prompt_answer_routes_use_control_connections_and_generated_ids() {
    let ask_log = Rc::new(RefCell::new(Vec::new()));
    let approval_log = Rc::new(RefCell::new(Vec::new()));
    let ask_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({})),
        ],
        ask_log.clone(),
    );
    let approval_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({})),
        ],
        approval_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![ask_connection, approval_connection]),
        RemoteContext::project("project-1", None),
    );

    remote
        .answer_ask(AskAnswerRequest {
            client_request_id: "request-ask".to_owned(),
            session_id: "session-1".to_owned(),
            ask_id: "ask-1".to_owned(),
            error_message: String::new(),
            answer: "Use cached data".to_owned(),
            selected_option_number: 1,
            freeform_answer: String::new(),
        })
        .unwrap();
    remote
        .answer_approval(ApprovalAnswerRequest {
            client_request_id: "request-approval".to_owned(),
            session_id: "session-1".to_owned(),
            approval_id: "approval-1".to_owned(),
            error_message: String::new(),
            decision: Some(client_contracts::clientui::ApprovalDecision::AllowOnce),
            commentary: "safe for this command".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(
        factory.opened,
        vec![ConnectionKind::Control, ConnectionKind::Control]
    );
    assert_sent_methods(
        &ask_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "ask.answer"),
        ],
    );
    assert_eq!(
        ask_log.borrow()[2].request().params.unwrap(),
        json!({
            "client_request_id": "request-ask",
            "session_id": "session-1",
            "ask_id": "ask-1",
            "answer": "Use cached data",
            "selected_option_number": 1
        })
    );
    assert_sent_methods(
        &approval_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "approval.answer"),
        ],
    );
    assert_eq!(
        approval_log.borrow()[2].request().params.unwrap(),
        json!({
            "client_request_id": "request-approval",
            "session_id": "session-1",
            "approval_id": "approval-1",
            "decision": "allow_once",
            "commentary": "safe for this command"
        })
    );
}

#[test]
fn runtime_has_queued_user_work_uses_control_connection() {
    let has_queued_log = Rc::new(RefCell::new(Vec::new()));
    let has_queued_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response(
                "rpc-1",
                RuntimeHasQueuedUserWorkResponse {
                    has_queued_user_work: true,
                },
            ),
        ],
        has_queued_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![has_queued_connection]),
        RemoteContext::project("project-1", None),
    );

    let response = remote
        .has_runtime_queued_user_work(RuntimeHasQueuedUserWorkRequest {
            session_id: "session-1".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert!(response.has_queued_user_work);
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &has_queued_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.hasQueuedUserWork"),
        ],
    );
    assert_eq!(
        has_queued_log.borrow()[2].request().params.unwrap(),
        json!({
            "session_id": "session-1",
        })
    );
}

#[test]
fn runtime_submit_queued_user_messages_uses_dedicated_connection() {
    let submit_queued_log = Rc::new(RefCell::new(Vec::new()));
    let submit_queued_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response(
                "runtime-submit-queued-user-messages",
                RuntimeSubmitQueuedUserMessagesResponse {
                    message: "assistant reply".to_owned(),
                },
            ),
        ],
        submit_queued_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![submit_queued_connection]),
        RemoteContext::project("project-1", None),
    );

    let response = remote
        .submit_runtime_queued_user_messages(RuntimeSubmitQueuedUserMessagesRequest {
            client_request_id: "request-submit-queued".to_owned(),
            session_id: "session-1".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(response.message, "assistant reply");
    assert_eq!(factory.opened, vec![ConnectionKind::Dedicated]);
    assert_sent_methods(
        &submit_queued_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            (
                "runtime-submit-queued-user-messages",
                "runtime.submitQueuedUserMessages",
            ),
        ],
    );
    assert_eq!(
        submit_queued_log.borrow()[2].request().params.unwrap(),
        json!({
            "client_request_id": "request-submit-queued",
            "session_id": "session-1",
        })
    );
}

#[test]
fn runtime_goal_routes_use_control_connections() {
    let scenarios = [
        GoalRouteScenario::show(),
        GoalRouteScenario::set(),
        GoalRouteScenario::pause(),
        GoalRouteScenario::resume(),
        GoalRouteScenario::clear(),
    ];

    for scenario in scenarios {
        let sent_log = Rc::new(RefCell::new(Vec::new()));
        let connection = ScriptedConnection::with_sent_log(
            vec![
                success_response("handshake", handshake_response()),
                success_response(
                    "attach-project",
                    project_attach_response(),
                ),
                success_response("rpc-1", scenario.response()),
            ],
            sent_log.clone(),
        );
        let mut remote = RemoteClient::new(
            ScriptedFactory::new(vec![connection]),
            RemoteContext::project("project-1", None),
        );

        let response = scenario.call(&mut remote).unwrap();
        let factory = remote.into_factory();

        assert_eq!(
            response.goal.as_ref().map(|goal| goal.id.as_str()),
            Some("goal-1")
        );
        assert_eq!(
            factory.opened,
            vec![ConnectionKind::Control],
            "{}",
            scenario.method
        );
        assert_sent_methods(
            &sent_log.borrow(),
            &[
                ("handshake", "protocol.handshake"),
                ("attach-project", "project.attach"),
                ("rpc-1", scenario.method),
            ],
        );
        assert_eq!(
            sent_log.borrow()[2].request().params.clone().unwrap(),
            scenario.expected_params(),
            "{}",
            scenario.method
        );
    }
}

#[derive(Clone, Copy)]
struct GoalRouteScenario {
    method: &'static str,
    action: GoalRouteAction,
}

#[derive(Clone, Copy)]
enum GoalRouteAction {
    Show,
    Set,
    Pause,
    Resume,
    Clear,
}

impl GoalRouteScenario {
    const fn show() -> Self {
        Self {
            method: "runtime.goal.show",
            action: GoalRouteAction::Show,
        }
    }

    const fn set() -> Self {
        Self {
            method: "runtime.goal.set",
            action: GoalRouteAction::Set,
        }
    }

    const fn pause() -> Self {
        Self {
            method: "runtime.goal.pause",
            action: GoalRouteAction::Pause,
        }
    }

    const fn resume() -> Self {
        Self {
            method: "runtime.goal.resume",
            action: GoalRouteAction::Resume,
        }
    }

    const fn clear() -> Self {
        Self {
            method: "runtime.goal.clear",
            action: GoalRouteAction::Clear,
        }
    }

    fn response(self) -> RuntimeGoalShowResponse {
        RuntimeGoalShowResponse {
            goal: Some(RuntimeGoal {
                id: "goal-1".to_owned(),
                objective: "ship the rust tui".to_owned(),
                status: "active".to_owned(),
                suspended: false,
                created_at: "2026-06-15T10:00:00Z".to_owned(),
                updated_at: "2026-06-15T10:05:00Z".to_owned(),
            }),
        }
    }

    fn call<F: ConnectionFactory>(
        self,
        remote: &mut RemoteClient<F>,
    ) -> Result<RuntimeGoalShowResponse, RpcError> {
        match self.action {
            GoalRouteAction::Show => remote.show_runtime_goal(RuntimeGoalShowRequest {
                session_id: "session-1".to_owned(),
            }),
            GoalRouteAction::Set => remote.set_runtime_goal(RuntimeGoalSetRequest {
                client_request_id: "request-goal-set".to_owned(),
                session_id: "session-1".to_owned(),
                objective: "ship the rust tui".to_owned(),
                actor: "user".to_owned(),
            }),
            GoalRouteAction::Pause => remote.pause_runtime_goal(RuntimeGoalStatusRequest {
                client_request_id: "request-goal-pause".to_owned(),
                session_id: "session-1".to_owned(),
                actor: "user".to_owned(),
            }),
            GoalRouteAction::Resume => remote.resume_runtime_goal(RuntimeGoalStatusRequest {
                client_request_id: "request-goal-resume".to_owned(),
                session_id: "session-1".to_owned(),
                actor: "user".to_owned(),
            }),
            GoalRouteAction::Clear => remote.clear_runtime_goal(RuntimeGoalClearRequest {
                client_request_id: "request-goal-clear".to_owned(),
                session_id: "session-1".to_owned(),
                actor: "user".to_owned(),
            }),
        }
    }

    fn expected_params(self) -> serde_json::Value {
        match self.action {
            GoalRouteAction::Show => json!({
                "session_id": "session-1",
            }),
            GoalRouteAction::Set => json!({
                "client_request_id": "request-goal-set",
                "session_id": "session-1",
                "objective": "ship the rust tui",
                "actor": "user",
            }),
            GoalRouteAction::Pause => json!({
                "client_request_id": "request-goal-pause",
                "session_id": "session-1",
                "actor": "user",
            }),
            GoalRouteAction::Resume => json!({
                "client_request_id": "request-goal-resume",
                "session_id": "session-1",
                "actor": "user",
            }),
            GoalRouteAction::Clear => json!({
                "client_request_id": "request-goal-clear",
                "session_id": "session-1",
                "actor": "user",
            }),
        }
    }
}

#[test]
fn worktree_list_uses_control_connection_and_worktree_list_method() {
    let worktree_log = Rc::new(RefCell::new(Vec::new()));
    let response = WorktreeListResponse {
        target: session_execution_target(),
        worktrees: vec![WorktreeView {
            worktree_id: "worktree-main".to_owned(),
            display_name: "main".to_owned(),
            canonical_root: "/repo".to_owned(),
            availability: "available".to_owned(),
            branch_ref: "refs/heads/main".to_owned(),
            branch_name: "main".to_owned(),
            detached: false,
            locked_reason: String::new(),
            prunable_reason: String::new(),
            dirty_file_count: 0,
            is_main: true,
            is_current: true,
            builder_managed: true,
            created_branch: false,
            origin_session_id: String::new(),
        }],
    };
    let worktree_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", response.clone()),
        ],
        worktree_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![worktree_connection]),
        RemoteContext::project("project-1", None),
    );

    let actual = remote
        .list_worktrees(WorktreeListRequest {
            session_id: "session-1".to_owned(),
            include_dirty_count: false,
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(actual, response);
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &worktree_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "worktree.list"),
        ],
    );
    assert_eq!(
        worktree_log.borrow()[2].request().params.unwrap(),
        json!({
            "session_id": "session-1",
        })
    );
}

#[test]
fn process_routes_use_control_connection_and_process_methods() {
    let process_log = Rc::new(RefCell::new(Vec::new()));
    let list_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response(
                "rpc-1",
                ProcessListResponse {
                    processes: Vec::new(),
                },
            ),
        ],
        process_log.clone(),
    );
    let kill_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({})),
        ],
        process_log.clone(),
    );
    let inline_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response(
                "rpc-1",
                ProcessInlineOutputResponse {
                    output: "stdout".to_owned(),
                    log_path: "/tmp/proc.log".to_owned(),
                },
            ),
        ],
        process_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![list_connection, kill_connection, inline_connection]),
        RemoteContext::project("project-1", None),
    );

    let list = remote
        .list_processes(ProcessListRequest {
            owner_session_id: "session-1".to_owned(),
            owner_run_id: "run-1".to_owned(),
        })
        .unwrap();
    remote
        .kill_process(ProcessKillRequest {
            client_request_id: "client-kill-1".to_owned(),
            process_id: "proc-1".to_owned(),
        })
        .unwrap();
    let inline = remote
        .inline_process_output(ProcessInlineOutputRequest {
            process_id: "proc-1".to_owned(),
            max_chars: 12_000,
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(list.processes, Vec::new());
    assert_eq!(inline.output, "stdout");
    assert_eq!(
        factory.opened,
        vec![
            ConnectionKind::Control,
            ConnectionKind::Control,
            ConnectionKind::Control,
        ]
    );
    assert_sent_methods(
        &process_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "process.list"),
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "process.kill"),
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "process.inlineOutput"),
        ],
    );
    assert_eq!(
        process_log.borrow()[2].request().params.unwrap(),
        json!({
            "OwnerSessionID": "session-1",
            "OwnerRunID": "run-1"
        })
    );
    assert_eq!(
        process_log.borrow()[5].request().params.unwrap(),
        json!({
            "ClientRequestID": "client-kill-1",
            "ProcessID": "proc-1"
        })
    );
    assert_eq!(
        process_log.borrow()[8].request().params.unwrap(),
        json!({
            "ProcessID": "proc-1",
            "MaxChars": 12000
        })
    );
}

#[test]
fn worktree_switch_uses_control_connection_and_worktree_switch_method() {
    let worktree_log = Rc::new(RefCell::new(Vec::new()));
    let response = WorktreeSwitchResponse {
        target: session_execution_target(),
        worktree: WorktreeView {
            worktree_id: "worktree-rust".to_owned(),
            display_name: "rust-tui".to_owned(),
            canonical_root: "/repo-rust".to_owned(),
            availability: "available".to_owned(),
            branch_ref: "refs/heads/rust-tui".to_owned(),
            branch_name: "rust-tui".to_owned(),
            detached: false,
            locked_reason: String::new(),
            prunable_reason: String::new(),
            dirty_file_count: 0,
            is_main: false,
            is_current: true,
            builder_managed: true,
            created_branch: false,
            origin_session_id: String::new(),
        },
    };
    let worktree_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", response.clone()),
        ],
        worktree_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![worktree_connection]),
        RemoteContext::project("project-1", None),
    );

    let actual = remote
        .switch_worktree(WorktreeSwitchRequest {
            client_request_id: "client-request-1".to_owned(),
            session_id: "session-1".to_owned(),
            worktree_id: "worktree-rust".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(actual, response);
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &worktree_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "worktree.switch"),
        ],
    );
    assert_eq!(
        worktree_log.borrow()[2].request().params.unwrap(),
        json!({
            "client_request_id": "client-request-1",
            "session_id": "session-1",
            "worktree_id": "worktree-rust"
        })
    );
}

#[test]
fn worktree_delete_uses_control_connection_and_worktree_delete_method() {
    let worktree_log = Rc::new(RefCell::new(Vec::new()));
    let response = WorktreeDeleteResponse {
        target: session_execution_target(),
        worktree: WorktreeView {
            worktree_id: "worktree-rust".to_owned(),
            display_name: "rust-tui".to_owned(),
            canonical_root: "/repo-rust".to_owned(),
            availability: "available".to_owned(),
            branch_ref: "refs/heads/rust-tui".to_owned(),
            branch_name: "rust-tui".to_owned(),
            detached: false,
            locked_reason: String::new(),
            prunable_reason: String::new(),
            dirty_file_count: 0,
            is_main: false,
            is_current: false,
            builder_managed: true,
            created_branch: false,
            origin_session_id: String::new(),
        },
        branch_deleted: false,
        branch_cleanup_message: "Kept branch rust-tui".to_owned(),
    };
    let worktree_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", response.clone()),
        ],
        worktree_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![worktree_connection]),
        RemoteContext::project("project-1", None),
    );

    let actual = remote
        .delete_worktree(WorktreeDeleteRequest {
            client_request_id: "client-request-1".to_owned(),
            session_id: "session-1".to_owned(),
            worktree_id: "worktree-rust".to_owned(),
            delete_branch: false,
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(actual, response);
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &worktree_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "worktree.delete"),
        ],
    );
    assert_eq!(
        worktree_log.borrow()[2].request().params.unwrap(),
        json!({
            "client_request_id": "client-request-1",
            "session_id": "session-1",
            "worktree_id": "worktree-rust"
        })
    );
}

#[test]
fn worktree_create_target_resolve_uses_control_connection_and_worktree_create_target_resolve_method()
{
    let worktree_log = Rc::new(RefCell::new(Vec::new()));
    let response = WorktreeCreateTargetResolveResponse {
        resolution: WorktreeCreateTargetResolution {
            input: "HEAD~1".to_owned(),
            kind: WorktreeCreateTargetResolutionKind::DetachedRef,
            resolved_ref: "abc123".to_owned(),
        },
    };
    let worktree_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", response.clone()),
        ],
        worktree_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![worktree_connection]),
        RemoteContext::project("project-1", None),
    );

    let actual = remote
        .resolve_worktree_create_target(WorktreeCreateTargetResolveRequest {
            session_id: "session-1".to_owned(),
            target: "HEAD~1".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(actual, response);
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &worktree_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "worktree.create_target.resolve"),
        ],
    );
    assert_eq!(
        worktree_log.borrow()[2].request().params.unwrap(),
        json!({
            "session_id": "session-1",
            "target": "HEAD~1"
        })
    );
}

#[test]
fn worktree_create_uses_control_connection_and_worktree_create_method() {
    let worktree_log = Rc::new(RefCell::new(Vec::new()));
    let response = WorktreeCreateResponse {
        target: session_execution_target(),
        worktree: WorktreeView {
            worktree_id: "worktree-new".to_owned(),
            display_name: "new-feature".to_owned(),
            canonical_root: "/wt/new-feature".to_owned(),
            availability: "available".to_owned(),
            branch_ref: "refs/heads/feature/new".to_owned(),
            branch_name: "feature/new".to_owned(),
            detached: false,
            locked_reason: String::new(),
            prunable_reason: String::new(),
            dirty_file_count: 0,
            is_main: false,
            is_current: true,
            builder_managed: true,
            created_branch: true,
            origin_session_id: String::new(),
        },
        created_branch: true,
        setup_scheduled: true,
    };
    let worktree_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", response.clone()),
        ],
        worktree_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![worktree_connection]),
        RemoteContext::project("project-1", None),
    );

    let actual = remote
        .create_worktree(WorktreeCreateRequest {
            client_request_id: "client-request-1".to_owned(),
            session_id: "session-1".to_owned(),
            base_ref: "main".to_owned(),
            create_branch: false,
            branch_name: String::new(),
            root_path: String::new(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(actual, response);
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &worktree_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "worktree.create"),
        ],
    );
    assert_eq!(
        worktree_log.borrow()[2].request().params.unwrap(),
        json!({
            "client_request_id": "client-request-1",
            "session_id": "session-1",
            "base_ref": "main"
        })
    );
}

#[test]
fn runtime_set_session_name_uses_control_connection() {
    let set_name_log = Rc::new(RefCell::new(Vec::new()));
    let set_name_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({})),
        ],
        set_name_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![set_name_connection]),
        RemoteContext::project("project-1", None),
    );

    remote
        .set_runtime_session_name(RuntimeSetSessionNameRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            name: "incident triage".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &set_name_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.setSessionName"),
        ],
    );
}

#[test]
fn runtime_set_thinking_level_uses_control_connection() {
    let set_thinking_log = Rc::new(RefCell::new(Vec::new()));
    let set_thinking_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({})),
        ],
        set_thinking_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![set_thinking_connection]),
        RemoteContext::project("project-1", None),
    );

    remote
        .set_runtime_thinking_level(RuntimeSetThinkingLevelRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            level: "high".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &set_thinking_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.setThinkingLevel"),
        ],
    );
}

#[test]
fn runtime_set_fast_mode_enabled_uses_control_connection_and_decodes_changed_response() {
    let set_fast_log = Rc::new(RefCell::new(Vec::new()));
    let set_fast_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({"changed":false})),
        ],
        set_fast_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![set_fast_connection]),
        RemoteContext::project("project-1", None),
    );

    let response = remote
        .set_runtime_fast_mode_enabled(RuntimeSetFastModeEnabledRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            enabled: true,
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(
        response,
        RuntimeSetFastModeEnabledResponse { changed: false }
    );
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &set_fast_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.setFastModeEnabled"),
        ],
    );
}

#[test]
fn runtime_append_local_entry_uses_control_connection_and_decodes_empty_response() {
    let append_log = Rc::new(RefCell::new(Vec::new()));
    let append_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({})),
        ],
        append_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![append_connection]),
        RemoteContext::project("project-1", None),
    );

    remote
        .append_runtime_committed_entry(RuntimeAppendCommittedEntryRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            role: "error".to_owned(),
            text: "local feedback".to_owned(),
            visibility: String::new(),
            notice_id: String::new(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &append_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.appendCommittedEntry"),
        ],
    );
}

#[test]
fn runtime_set_reviewer_enabled_uses_control_connection_and_decodes_changed_mode_response() {
    let set_reviewer_log = Rc::new(RefCell::new(Vec::new()));
    let set_reviewer_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({"changed":false,"mode":"edits"})),
        ],
        set_reviewer_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![set_reviewer_connection]),
        RemoteContext::project("project-1", None),
    );

    let response = remote
        .set_runtime_reviewer_enabled(RuntimeSetReviewerEnabledRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            enabled: true,
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(
        response,
        RuntimeSetReviewerEnabledResponse {
            changed: false,
            mode: "edits".to_owned(),
        }
    );
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &set_reviewer_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.setReviewerEnabled"),
        ],
    );
}

#[test]
fn runtime_set_auto_compaction_enabled_uses_control_connection_and_decodes_changed_enabled_response()
{
    let set_auto_compaction_log = Rc::new(RefCell::new(Vec::new()));
    let set_auto_compaction_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({"changed":false,"enabled":false})),
        ],
        set_auto_compaction_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![set_auto_compaction_connection]),
        RemoteContext::project("project-1", None),
    );

    let response = remote
        .set_runtime_auto_compaction_enabled(RuntimeSetAutoCompactionEnabledRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            enabled: true,
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(
        response,
        RuntimeSetAutoCompactionEnabledResponse {
            changed: false,
            enabled: false,
        }
    );
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &set_auto_compaction_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.setAutoCompactionEnabled"),
        ],
    );
}

#[test]
fn runtime_set_questions_enabled_uses_control_connection_and_decodes_changed_enabled_response() {
    let set_questions_log = Rc::new(RefCell::new(Vec::new()));
    let set_questions_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", json!({"changed":false,"enabled":true})),
        ],
        set_questions_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![set_questions_connection]),
        RemoteContext::project("project-1", None),
    );

    let response = remote
        .set_runtime_questions_enabled(RuntimeSetQuestionsEnabledRequest {
            client_request_id: "request-1".to_owned(),
            session_id: "session-1".to_owned(),
            enabled: false,
        })
        .unwrap();
    let factory = remote.into_factory();

    assert_eq!(
        response,
        RuntimeSetQuestionsEnabledResponse {
            changed: false,
            enabled: true,
        }
    );
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &set_questions_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "runtime.setQuestionsEnabled"),
        ],
    );
}

#[test]
fn remote_main_view_uses_control_connection_and_decodes_questions_status() {
    let main_view_log = Rc::new(RefCell::new(Vec::new()));
    let mut response: SessionMainViewResponse =
        serde_json::from_value(zero_main_view_json()).unwrap();
    response.main_view.status.questions_enabled = true;
    response.main_view.status.previous_session_id = Some("previous-session".to_owned());
    response.main_view.status.parent_agent_session_id = Some("parent-agent-session".to_owned());
    response.main_view.status.navigation_target_session_id =
        Some("server-navigation-target".to_owned());
    let main_view_connection = ScriptedConnection::with_sent_log(
        vec![
            success_response("handshake", handshake_response()),
            success_response(
                "attach-project",
                project_attach_response(),
            ),
            success_response("rpc-1", response.clone()),
        ],
        main_view_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![main_view_connection]),
        RemoteContext::project("project-1", None),
    );

    let actual = remote
        .get_session_main_view(SessionMainViewRequest {
            session_id: "session-1".to_owned(),
        })
        .unwrap();
    let factory = remote.into_factory();

    assert!(actual.main_view.status.questions_enabled);
    assert_eq!(
        actual.main_view.status.previous_session_id.as_deref(),
        Some("previous-session")
    );
    assert_eq!(
        actual.main_view.status.parent_agent_session_id.as_deref(),
        Some("parent-agent-session")
    );
    assert_eq!(
        actual.main_view.status.navigation_target_session_id.as_deref(),
        Some("server-navigation-target")
    );
    assert_eq!(factory.opened, vec![ConnectionKind::Control]);
    assert_sent_methods(
        &main_view_log.borrow(),
        &[
            ("handshake", "protocol.handshake"),
            ("attach-project", "project.attach"),
            ("rpc-1", "session.getMainView"),
        ],
    );
}

#[test]
fn control_calls_allocate_monotonic_ids_and_match_reordered_responses() {
    let connection = ScriptedConnection::new(vec![
        success_response("rpc-2", json!({"second":true})),
        Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: String::new(),
            result: Some(json!({"ignored":"blank"})),
            error: None,
        }),
        success_response("rpc-1", json!({"first":true})),
    ]);
    let mut rpc = JsonRpcConnection::new(connection);

    let first_pending = rpc.start_call("first.method", json!({"index":1})).unwrap();
    let second_pending = rpc.start_call("second.method", json!({"index":2})).unwrap();
    let second = rpc.receive_pending(&second_pending).unwrap();
    let first = rpc.receive_pending(&first_pending).unwrap();
    let connection = rpc.into_connection();

    assert_eq!(first, json!({"first":true}));
    assert_eq!(second, json!({"second":true}));
    assert_sent_methods(
        &connection.sent,
        &[("rpc-1", "first.method"), ("rpc-2", "second.method")],
    );
}

#[test]
fn local_cancellation_removes_pending_without_sending_cancel_or_closing_connection() {
    let connection = ScriptedConnection::new(vec![
        success_response("rpc-1", json!({"late":true})),
        success_response("rpc-2", json!({"next":true})),
    ]);
    let mut rpc = JsonRpcConnection::new(connection);

    let canceled = rpc.start_call("first.method", json!({"index":1})).unwrap();
    rpc.cancel_pending(&canceled, CallCancellation::Local)
        .unwrap();
    assert_eq!(
        rpc.receive_pending(&canceled).unwrap_err(),
        RpcError::RequestCanceledLocally
    );
    let next_pending = rpc.start_call("second.method", json!({"index":2})).unwrap();
    let next = rpc.receive_pending(&next_pending).unwrap();
    let connection = rpc.into_connection();

    assert_eq!(next, json!({"next":true}));
    assert_sent_methods(
        &connection.sent,
        &[("rpc-1", "first.method"), ("rpc-2", "second.method")],
    );
    assert_eq!(connection.close_count.get(), 0);
}

#[test]
fn local_cancellation_removes_buffered_ready_response() {
    let connection = ScriptedConnection::new(vec![
        success_response("rpc-2", json!({"second":true})),
        success_response("rpc-1", json!({"first":true})),
    ]);
    let mut rpc = JsonRpcConnection::new(connection);

    let first = rpc.start_call("first.method", json!({})).unwrap();
    let second = rpc.start_call("second.method", json!({})).unwrap();
    assert_eq!(rpc.receive_pending(&first).unwrap(), json!({"first":true}));
    assert_eq!(rpc.ready_count(), 1);
    rpc.cancel_pending(&second, CallCancellation::Local)
        .unwrap();

    assert_eq!(rpc.ready_count(), 0);
    assert_eq!(
        rpc.receive_pending(&second).unwrap_err(),
        RpcError::RequestCanceledLocally
    );
}

#[test]
fn closing_connection_fails_all_pending_and_later_calls_with_typed_close() {
    let connection = ScriptedConnection::new(Vec::new());
    let mut rpc = JsonRpcConnection::new(connection);

    let first = rpc.start_call("first.method", json!({})).unwrap();
    let second = rpc.start_call("second.method", json!({})).unwrap();
    rpc.close().unwrap();

    assert_eq!(rpc.receive_pending(&first).unwrap_err(), RpcError::Closed);
    assert_eq!(rpc.receive_pending(&second).unwrap_err(), RpcError::Closed);
    assert_eq!(
        rpc.start_call("third.method", json!({})).unwrap_err(),
        RpcError::Closed
    );
    let connection = rpc.into_connection();
    assert_eq!(connection.close_count.get(), 1);
}

#[test]
fn inbound_transport_error_fails_all_pending_and_clears_state() {
    let connection =
        ScriptedConnection::with_error(TransportError::ReceiveFailed("read failed".to_owned()));
    let mut rpc = JsonRpcConnection::new(connection);

    let first = rpc.start_call("first.method", json!({})).unwrap();
    let second = rpc.start_call("second.method", json!({})).unwrap();

    assert_eq!(
        rpc.receive_pending(&first).unwrap_err(),
        RpcError::Transport(TransportError::ReceiveFailed("read failed".to_owned()))
    );
    assert_eq!(
        rpc.receive_pending(&second).unwrap_err(),
        RpcError::Transport(TransportError::ReceiveFailed("read failed".to_owned()))
    );
    assert_eq!(
        rpc.start_call("third.method", json!({})).unwrap_err(),
        RpcError::Transport(TransportError::ReceiveFailed("read failed".to_owned()))
    );
}

#[test]
fn send_failure_and_backpressure_remove_pending_without_reusing_request_ids() {
    let mut rpc = JsonRpcConnection::new(ScriptedConnection::with_send_errors(vec![
        TransportError::SendFailed("write failed".to_owned()),
        TransportError::Backpressure,
    ]));

    assert_eq!(
        rpc.start_call("first.method", json!({})).unwrap_err(),
        RpcError::Transport(TransportError::SendFailed("write failed".to_owned()))
    );
    assert_eq!(rpc.pending_count(), 0);
    assert_eq!(
        rpc.start_call("second.method", json!({})).unwrap_err(),
        RpcError::Transport(TransportError::Backpressure)
    );
    assert_eq!(rpc.pending_count(), 0);
    let connection = rpc.into_connection();

    assert_sent_methods(
        &connection.sent,
        &[("rpc-1", "first.method"), ("rpc-2", "second.method")],
    );
}

#[test]
fn setup_and_dedicated_failures_close_new_connections() {
    let handshake_transport = ScriptedConnection::new(Vec::new());
    let handshake_transport_closed = handshake_transport.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![handshake_transport]),
        RemoteContext::project("project-1", None),
    );
    assert!(remote.open_project_connection().is_err());
    assert_eq!(handshake_transport_closed.get(), 1);

    let handshake = ScriptedConnection::new(vec![error_response("handshake")]);
    let handshake_closed = handshake.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![handshake]),
        RemoteContext::project("project-1", None),
    );
    assert!(remote.open_project_connection().is_err());
    assert_eq!(handshake_closed.get(), 1);

    let project_attach = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        error_response("attach-project"),
    ]);
    let project_attach_closed = project_attach.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![project_attach]),
        RemoteContext::project("project-1", None),
    );
    assert!(remote.open_project_connection().is_err());
    assert_eq!(project_attach_closed.get(), 1);

    let session_attach = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        error_response("attach-session"),
    ]);
    let session_attach_closed = session_attach.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![session_attach]),
        RemoteContext::unscoped(),
    );
    assert!(
        remote
            .subscribe_raw(
                "session-1",
                subscription_route(),
                json!({"session_id":"session-1"}),
            )
            .is_err()
    );
    assert_eq!(session_attach_closed.get(), 1);

    let subscribe = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-session",
            session_attach_response("session-1"),
        ),
        error_response("subscribe-sample"),
    ]);
    let subscribe_closed = subscribe.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![subscribe]),
        RemoteContext::unscoped(),
    );
    assert!(
        remote
            .subscribe_raw(
                "session-1",
                subscription_route(),
                json!({"session_id":"session-1"}),
            )
            .is_err()
    );
    assert_eq!(subscribe_closed.get(), 1);

    let subscribe_transport = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-session",
            session_attach_response("session-1"),
        ),
    ]);
    let subscribe_transport_closed = subscribe_transport.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![subscribe_transport]),
        RemoteContext::unscoped(),
    );
    assert!(
        remote
            .subscribe_raw(
                "session-1",
                subscription_route(),
                json!({"session_id":"session-1"}),
            )
            .is_err()
    );
    assert_eq!(subscribe_transport_closed.get(), 1);

    let dedicated = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        error_response("runtime-interrupt"),
    ]);
    let dedicated_closed = dedicated.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![dedicated]),
        RemoteContext::unscoped(),
    );
    assert!(
        remote
            .call_dedicated("runtime-interrupt", "runtime.interrupt", json!({}))
            .is_err()
    );
    assert_eq!(dedicated_closed.get(), 1);

    let dedicated_transport =
        ScriptedConnection::new(vec![success_response("handshake", handshake_response())]);
    let dedicated_transport_closed = dedicated_transport.close_count.clone();
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![dedicated_transport]),
        RemoteContext::unscoped(),
    );
    assert!(
        remote
            .call_dedicated("runtime-interrupt", "runtime.interrupt", json!({}))
            .is_err()
    );
    assert_eq!(dedicated_transport_closed.get(), 1);
}

struct ScriptedConnection {
    sent: Vec<Frame>,
    incoming: VecDeque<Frame>,
    close_count: Rc<Cell<usize>>,
    sent_log: Option<Rc<RefCell<Vec<Frame>>>>,
    receive_error: Option<TransportError>,
    send_errors: VecDeque<TransportError>,
}

struct ScriptedFactory {
    opened: Vec<ConnectionKind>,
    connections: VecDeque<ScriptedConnection>,
}

impl ScriptedFactory {
    fn new(connections: Vec<ScriptedConnection>) -> Self {
        Self {
            opened: Vec::new(),
            connections: connections.into(),
        }
    }
}

impl ConnectionFactory for ScriptedFactory {
    type Connection = ScriptedConnection;

    fn open(&mut self, kind: ConnectionKind) -> Result<Self::Connection, TransportError> {
        self.opened.push(kind);
        self.connections.pop_front().ok_or(TransportError::Closed)
    }
}

impl ScriptedConnection {
    fn new(incoming: Vec<Frame>) -> Self {
        Self {
            sent: Vec::new(),
            incoming: incoming.into(),
            close_count: Rc::new(Cell::new(0)),
            sent_log: None,
            receive_error: None,
            send_errors: VecDeque::new(),
        }
    }

    fn with_sent_log(incoming: Vec<Frame>, sent_log: Rc<RefCell<Vec<Frame>>>) -> Self {
        Self {
            sent: Vec::new(),
            incoming: incoming.into(),
            close_count: Rc::new(Cell::new(0)),
            sent_log: Some(sent_log),
            receive_error: None,
            send_errors: VecDeque::new(),
        }
    }

    fn with_error(error: TransportError) -> Self {
        Self {
            sent: Vec::new(),
            incoming: VecDeque::new(),
            close_count: Rc::new(Cell::new(0)),
            sent_log: None,
            receive_error: Some(error),
            send_errors: VecDeque::new(),
        }
    }

    fn with_send_errors(errors: Vec<TransportError>) -> Self {
        Self {
            sent: Vec::new(),
            incoming: VecDeque::new(),
            close_count: Rc::new(Cell::new(0)),
            sent_log: None,
            receive_error: None,
            send_errors: errors.into(),
        }
    }
}

impl FrameConnection for ScriptedConnection {
    fn send(&mut self, frame: Frame) -> Result<(), TransportError> {
        if let Some(sent_log) = &self.sent_log {
            sent_log.borrow_mut().push(frame.clone());
        }
        self.sent.push(frame);
        if let Some(error) = self.send_errors.pop_front() {
            return Err(error);
        }
        Ok(())
    }

    fn receive(&mut self) -> Result<Frame, TransportError> {
        if let Some(error) = &self.receive_error {
            return Err(error.clone());
        }
        self.incoming.pop_front().ok_or(TransportError::Closed)
    }

    fn receive_with_timeout(
        &mut self,
        _timeout: std::time::Duration,
    ) -> Result<Frame, TransportError> {
        self.receive()
    }

    fn close(&mut self) -> Result<(), TransportError> {
        self.close_count.set(self.close_count.get() + 1);
        Ok(())
    }
}

fn capabilities() -> CapabilityFlags {
    CapabilityFlags {
        jsonrpc_websocket: true,
        auth_bootstrap: true,
        project_attach: true,
        session_attach: true,
        health_endpoint: true,
        readiness_endpoint: true,
        run_prompt: true,
        session_plan: true,
        session_lifecycle: true,
        session_transcript_paging: true,
        session_runtime: true,
        runtime_control: true,
        prompt_control: true,
        process_output: true,
    }
}

fn handshake_response() -> HandshakeResponse {
    HandshakeResponse {
        identity: ServerIdentity {
            protocol_version: "16".to_owned(),
            server_id: "server-1".to_owned(),
            pid: 123,
            persistence_root_id: String::new(),
            capabilities: capabilities(),
        },
    }
}

fn session_execution_target() -> SessionExecutionTarget {
    SessionExecutionTarget {
        workspace_id: "workspace-1".to_owned(),
        workspace_name: "Builder Core".to_owned(),
        workspace_root: "/repo".to_owned(),
        workspace_availability: "available".to_owned(),
        worktree_id: "worktree-main".to_owned(),
        worktree_name: "main".to_owned(),
        worktree_root: "/repo".to_owned(),
        worktree_availability: "available".to_owned(),
        cwd_relpath: String::new(),
        effective_workdir: "/repo".to_owned(),
    }
}

fn fork_rollback_transition_request() -> SessionResolveTransitionRequest {
    SessionResolveTransitionRequest {
        client_request_id: "request-1".to_owned(),
        session_id: "session-1".to_owned(),
        transition: SessionTransition {
            action: SessionTransitionAction::ForkRollback,
            initial_prompt: "edited rollback prompt".to_owned(),
            initial_prompt_history_recorded: false,
            initial_input: String::new(),
            target_session_id: String::new(),
            fork_rollback_target_id: "rollback-target-2".to_owned(),
            previous_session_id: None,
        },
    }
}

fn success_response(id: &str, result: impl serde::Serialize) -> Frame {
    Frame::from_response(Response {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: id.to_owned(),
        result: Some(serde_json::to_value(result).unwrap()),
        error: None,
    })
}

fn project_attach_response() -> serde_json::Value {
    json!({
        "kind": "project",
        "project_id": "project-1",
        "workspace_id": "workspace-1",
        "workspace_root": "/workspace",
        "workspace_selection": null
    })
}

fn project_attach_response_for_id(workspace_id: &str, workspace_root: &str) -> serde_json::Value {
    json!({
        "kind": "project",
        "project_id": "project-1",
        "workspace_id": workspace_id,
        "workspace_root": workspace_root,
        "workspace_selection": {
            "kind": "workspace_id",
            "workspace_id": workspace_id
        }
    })
}

fn project_attach_response_for_root(
    requested_root: &str,
    workspace_id: &str,
    canonical_root: &str,
) -> serde_json::Value {
    json!({
        "kind": "project",
        "project_id": "project-1",
        "workspace_id": workspace_id,
        "workspace_root": canonical_root,
        "workspace_selection": {
            "kind": "workspace_root",
            "requested_root": requested_root,
            "canonical_root": canonical_root
        }
    })
}

fn session_attach_response(session_id: &str) -> serde_json::Value {
    json!({
        "kind": "session",
        "project_id": "project-1",
        "workspace_id": "workspace-1",
        "workspace_root": "/workspace",
        "session_id": session_id
    })
}

fn error_response(id: &str) -> Frame {
    Frame::from_response(Response {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: id.to_owned(),
        result: None,
        error: Some(rpc_client::wire::ResponseError {
            code: ErrorCode::InvalidParams.code(),
            message: "invalid".to_owned(),
        }),
    })
}

fn subscription_route() -> rpc_client::stream::SubscriptionRoute {
    rpc_client::stream::SubscriptionRoute {
        request_id: "subscribe-sample",
        method: "sample.subscribe",
        event_method: "sample.event",
        complete_method: "sample.complete",
    }
}

fn assert_sent_methods(sent: &[Frame], expected: &[(&str, &str)]) {
    let actual = sent
        .iter()
        .map(|frame| {
            let request = frame.request();
            (request.id, request.method)
        })
        .collect::<Vec<_>>();
    let expected = expected
        .iter()
        .map(|(id, method)| ((*id).to_owned(), (*method).to_owned()))
        .collect::<Vec<_>>();
    assert_eq!(actual, expected);
}

fn contract_model_capabilities_json() -> serde_json::Value {
    json!({
        "SupportsReasoningEffort": false,
        "SupportsVisionInputs": false
    })
}

fn contract_provider_capabilities_json() -> serde_json::Value {
    json!({
        "ProviderID": "",
        "SupportsResponsesAPI": false,
        "SupportsResponsesCompact": false,
        "SupportsRequestInputTokenCount": false,
        "SupportsPromptCacheKey": false,
        "SupportsNativeWebSearch": false,
        "SupportsReasoningEncrypted": false,
        "SupportsServerSideContextEdit": false,
        "IsOpenAIFirstParty": false
    })
}

fn contract_settings_json() -> serde_json::Value {
    json!({
        "Model": "gpt-5.6-sol",
        "ThinkingLevel": "medium",
        "ModelVerbosity": "",
        "SystemPromptFile": "",
        "SystemPromptFiles": [],
        "ModelCapabilities": contract_model_capabilities_json(),
        "Theme": "",
        "NotificationMethod": "",
        "ToolPreambles": false,
        "PriorityRequestMode": false,
        "Debug": false,
        "ServerHost": "",
        "ServerPort": 0,
        "WebSearch": "",
        "ProviderOverride": "",
        "OpenAIBaseURL": "",
        "ProviderCapabilities": contract_provider_capabilities_json(),
        "Store": false,
        "AllowNonCwdEdits": false,
        "ModelContextWindow": 0,
        "ContextCompactionThresholdTokens": 0,
        "PreSubmitCompactionLeadTokens": 0,
        "MinimumExecToBgSeconds": 0,
        "CompactionMode": "",
        "EnabledTools": {"patch": true, "shell": true},
        "SkillToggles": {},
        "Timeouts": {"ModelRequestSeconds": 0},
        "ShellOutputMaxChars": 0,
        "BGShellsOutput": "",
        "Shell": {"PostprocessingMode": "", "PostprocessHook": null},
        "CacheWarningMode": "",
        "Worktrees": {"BaseDir": "", "SetupScript": ""},
        "Workflow": {
            "CompletionMode": "",
            "Concurrency": 0,
            "MaxInvalidCompletionAttempts": 0
        },
        "Reviewer": {
            "Frequency": "",
            "Model": "",
            "ThinkingLevel": "",
            "ModelVerbosity": "",
            "ProviderOverride": "",
            "OpenAIBaseURL": "",
            "ModelCapabilities": contract_model_capabilities_json(),
            "ProviderCapabilities": contract_provider_capabilities_json(),
            "ModelContextWindow": 0,
            "Auth": "",
            "SystemPromptFile": "",
            "TimeoutSeconds": 0,
            "VerboseOutput": false
        },
        "Subagents": {},
        "PreventSleep": "",
        "MaxSubagentDepth": 2
    })
}

fn contract_source_report_json() -> serde_json::Value {
    json!({
        "SettingsPath": "/home/user/.builder/config.toml",
        "SettingsFileExists": true,
        "CreatedDefaultConfig": false,
        "HomeSettingsPath": "/home/user/.builder/config.toml",
        "HomeSettingsFileExists": true,
        "WorkspaceSettingsPath": "/work/builder/.builder/config.toml",
        "WorkspaceSettingsFileExists": true,
        "WorkspaceSettingsLayerEnabled": true,
        "Sources": {"model": "default", "theme": "cli"}
    })
}

fn contract_transcript_rows_json() -> serde_json::Value {
    json!([
        {
            "Visibility": "O",
            "Integrity": 0,
            "Kind": "user",
            "User": {
                "StepID": "11111111-1111-4111-8111-111111111111",
                "Text": "ship the fix",
                "CondensedText": null,
                "RollbackTargetID": null
            },
            "Assistant": null,
            "Tool": null,
            "Notice": null
        }
    ])
}

fn zero_main_view_json() -> serde_json::Value {
    json!({
        "MainView": {
            "Status": {
                "ReviewerFrequency": "",
                "ReviewerEnabled": false,
                "AutoCompactionEnabled": false,
                "QuestionsEnabled": false,
                "FastModeAvailable": false,
                "FastModeEnabled": false,
                "ConversationFreshness": 0,
                "PreviousSessionID": null,
                "ParentAgentSessionID": null,
                "NavigationTargetSessionID": null,
                "LastCommittedAssistantFinalAnswer": "",
                "ThinkingLevel": "",
                "CompactionMode": "",
                "ContextUsage": {
                    "UsedTokens": 0,
                    "WindowTokens": 0,
                    "CacheHitPercent": 0,
                    "HasCacheHitPercentage": false
                },
                "CompactionCount": 0,
                "Goal": null
            },
            "Session": {
                "SessionID": "",
                "SessionName": "",
                "ConversationFreshness": 0,
                "ExecutionTarget": {
                    "WorkspaceID": "",
                    "WorkspaceName": "",
                    "WorkspaceRoot": "",
                    "WorkspaceAvailability": "",
                    "WorktreeID": "",
                    "WorktreeName": "",
                    "WorktreeRoot": "",
                    "WorktreeAvailability": "",
                    "CwdRelpath": "",
                    "EffectiveWorkdir": ""
                },
            },
            "ActiveRun": null
        }
    })
}
