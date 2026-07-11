use std::collections::VecDeque;

use client_contracts::clientui::{
    EventKind, PendingPromptEventType, RunLifecyclePhase, RunMode, RunStatus,
};
use client_contracts::prompt::PromptActivitySubscribeRequest;
use client_contracts::protocol::{
    AttachResponse, CapabilityFlags, HandshakeResponse, ServerIdentity,
};
use client_contracts::session::SessionActivitySubscribeRequest;
use rpc_client::api::{RemoteClient, RemoteContext};
use rpc_client::error::{ProtocolError, RpcError};
use rpc_client::stream::{NextCancellation, StreamItem, SubscriptionRoute, TypedStreamItem};
use rpc_client::transport::{ConnectionFactory, ConnectionKind, FrameConnection, TransportError};
use rpc_client::wire::{ErrorCode, Frame, JSONRPC_VERSION, Request, Response};
use serde_json::json;

#[test]
fn subscription_ack_event_and_complete_are_ordered_and_terminal() {
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-session",
            AttachResponse {
                kind: "session".to_owned(),
                project_id: String::new(),
                workspace_id: String::new(),
                workspace_root: String::new(),
                session_id: "session-1".to_owned(),
            },
        ),
        success_response("subscribe-session-activity", json!({"stream":"stream-1"})),
        notification(
            "session.activity",
            json!({"event":{"kind":"assistant_delta"}}),
        ),
        notification("session.activity.complete", json!({})),
    ]);
    let route = SubscriptionRoute {
        request_id: "subscribe-session-activity",
        method: "session.subscribeActivity",
        event_method: "session.activity",
        complete_method: "session.activity.complete",
    };
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::unscoped(),
    );

    let mut subscription = remote
        .subscribe_raw("session-1", route, json!({"session_id":"session-1"}))
        .unwrap();

    assert_eq!(
        subscription.next_item().unwrap(),
        StreamItem::Event(json!({"event":{"kind":"assistant_delta"}}))
    );
    assert_eq!(subscription.next_item().unwrap(), StreamItem::Complete);
    assert_eq!(subscription.next_item().unwrap(), StreamItem::Complete);
    let connection = subscription.into_connection();
    assert_sent_methods(
        &connection.sent,
        &[
            ("handshake", "protocol.handshake"),
            ("attach-session", "session.attach"),
            ("subscribe-session-activity", "session.subscribeActivity"),
        ],
    );
}

#[test]
fn session_activity_wrapper_decodes_typed_events_and_uses_route_metadata() {
    let event_params = json!({
        "event": {
            "Sequence": 7,
            "Kind": "run_state_changed",
            "StepID": "step-1",
            "RecoveryCause": "",
            "CommittedTranscriptChanged": false,
            "Error": "",
            "AssistantDelta": "",
            "UserMessage": "",
            "CacheWarningVisibility": "",
            "RunState": {
                "Lifecycle": {"Phase": "running", "Mode": "turn"},
                "RunID": "run-1",
                "Status": "running",
                "StartedAt": "2026-05-31T12:00:00Z",
                "FinishedAt": "0001-01-01T00:00:00Z"
            }
        }
    });
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-session",
            AttachResponse {
                kind: "session".to_owned(),
                project_id: String::new(),
                workspace_id: String::new(),
                workspace_root: String::new(),
                session_id: "session-1".to_owned(),
            },
        ),
        success_response(
            "subscribe-session-activity",
            json!({"stream":"session-activity"}),
        ),
        notification("session.activity", event_params),
        notification("session.activity.complete", json!({})),
    ]);
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::unscoped(),
    );

    let mut subscription = remote
        .subscribe_session_activity(SessionActivitySubscribeRequest {
            session_id: "session-1".to_owned(),
            after_sequence: 0,
        })
        .unwrap();

    let event = match subscription.next_item().unwrap() {
        TypedStreamItem::Event(params) => params.event,
        TypedStreamItem::Complete => panic!("expected event"),
    };
    assert_eq!(event.kind, EventKind::RunStateChanged);
    let run_state = event.run_state.unwrap();
    assert_eq!(run_state.status, RunStatus::Running);
    assert_eq!(run_state.lifecycle.phase, RunLifecyclePhase::Running);
    assert_eq!(run_state.lifecycle.mode, RunMode::Turn);
    assert_eq!(subscription.next_item().unwrap(), TypedStreamItem::Complete);
    let connection = subscription.into_connection();
    assert_sent_methods(
        &connection.sent,
        &[
            ("handshake", "protocol.handshake"),
            ("attach-session", "session.attach"),
            ("subscribe-session-activity", "session.subscribeActivity"),
        ],
    );
    assert_eq!(
        connection.sent[2].request().params.unwrap(),
        json!({"SessionID":"session-1","AfterSequence":0})
    );
}

#[test]
fn prompt_activity_wrapper_decodes_typed_events_and_uses_route_metadata() {
    let event_params = json!({
        "event": {
            "Sequence": 5,
            "Type": "pending",
            "PromptID": "prompt-1",
            "SessionID": "session-1",
            "Question": "Continue?",
            "Suggestions": ["Yes", "No"],
            "RecommendedOptionIndex": 1,
            "Approval": false,
            "CreatedAt": "2026-05-31T12:01:00Z"
        }
    });
    let connection = ScriptedConnection::new(vec![
        success_response("handshake", handshake_response()),
        success_response(
            "attach-session",
            AttachResponse {
                kind: "session".to_owned(),
                project_id: String::new(),
                workspace_id: String::new(),
                workspace_root: String::new(),
                session_id: "session-1".to_owned(),
            },
        ),
        success_response(
            "subscribe-prompt-activity",
            json!({"stream":"prompt-activity"}),
        ),
        notification("prompt.activity", event_params),
        notification("prompt.activity.complete", json!({})),
    ]);
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::unscoped(),
    );

    let mut subscription = remote
        .subscribe_prompt_activity(PromptActivitySubscribeRequest {
            session_id: "session-1".to_owned(),
            after_sequence: 0,
        })
        .unwrap();

    let event = match subscription.next_item().unwrap() {
        TypedStreamItem::Event(params) => params.event,
        TypedStreamItem::Complete => panic!("expected event"),
    };
    assert_eq!(event.event_type, PendingPromptEventType::Pending);
    assert_eq!(event.prompt_id, "prompt-1");
    assert_eq!(event.suggestions, ["Yes", "No"]);
    assert_eq!(subscription.next_item().unwrap(), TypedStreamItem::Complete);
    let connection = subscription.into_connection();
    assert_sent_methods(
        &connection.sent,
        &[
            ("handshake", "protocol.handshake"),
            ("attach-session", "session.attach"),
            ("subscribe-prompt-activity", "prompt.subscribeActivity"),
        ],
    );
    assert_eq!(
        connection.sent[2].request().params.unwrap(),
        json!({"SessionID":"session-1","AfterSequence":0})
    );
}

#[test]
fn subscription_maps_error_unexpected_invalid_complete_cancel_and_close() {
    let mut gap = RawSubscriptionFixture::new(vec![notification(
        "session.activity.complete",
        json!({"code": -32010, "message": "gap"}),
    )]);
    assert_eq!(
        gap.subscription.next_item().unwrap_err(),
        RpcError::Protocol(ProtocolError {
            code: ErrorCode::StreamGap,
            raw_code: -32010,
            message: "gap".to_owned(),
        })
    );

    let mut unexpected = RawSubscriptionFixture::new(vec![notification("other.method", json!({}))]);
    assert_eq!(
        unexpected.subscription.next_item().unwrap_err(),
        RpcError::StreamFailed
    );

    let mut invalid_complete = RawSubscriptionFixture::new(vec![notification(
        "session.activity.complete",
        json!("invalid"),
    )]);
    assert_eq!(
        invalid_complete.subscription.next_item().unwrap_err(),
        RpcError::StreamFailed
    );

    let mut cancel = RawSubscriptionFixture::new(vec![notification(
        "session.activity",
        json!({"event":{"kind":"assistant_delta"}}),
    )]);
    assert_eq!(
        cancel
            .subscription
            .cancel_next(NextCancellation::Local)
            .unwrap_err(),
        RpcError::RequestCanceledLocally
    );
    assert_eq!(
        cancel.subscription.next_item().unwrap(),
        StreamItem::Event(json!({"event":{"kind":"assistant_delta"}}))
    );

    let mut close = RawSubscriptionFixture::new(Vec::new());
    close.subscription.close().unwrap();
    assert_eq!(
        close.subscription.next_item().unwrap_err(),
        RpcError::Closed
    );
    let connection = close.subscription.into_connection();
    assert_eq!(connection.close_count, 1);
}

struct RawSubscriptionFixture {
    subscription: rpc_client::stream::RawSubscription<ScriptedConnection>,
}

impl RawSubscriptionFixture {
    fn new(incoming: Vec<Frame>) -> Self {
        Self {
            subscription: rpc_client::stream::RawSubscription::new(
                ScriptedConnection::new(incoming),
                SubscriptionRoute {
                    request_id: "subscribe-session-activity",
                    method: "session.subscribeActivity",
                    event_method: "session.activity",
                    complete_method: "session.activity.complete",
                },
                "stream-1".to_owned(),
            ),
        }
    }
}

struct ScriptedConnection {
    sent: Vec<Frame>,
    incoming: VecDeque<Frame>,
    close_count: usize,
}

impl ScriptedConnection {
    fn new(incoming: Vec<Frame>) -> Self {
        Self {
            sent: Vec::new(),
            incoming: incoming.into(),
            close_count: 0,
        }
    }
}

impl FrameConnection for ScriptedConnection {
    fn send(&mut self, frame: Frame) -> Result<(), TransportError> {
        self.sent.push(frame);
        Ok(())
    }

    fn receive(&mut self) -> Result<Frame, TransportError> {
        self.incoming.pop_front().ok_or(TransportError::Closed)
    }

    fn receive_with_timeout(
        &mut self,
        _timeout: std::time::Duration,
    ) -> Result<Frame, TransportError> {
        self.receive()
    }

    fn close(&mut self) -> Result<(), TransportError> {
        self.close_count += 1;
        Ok(())
    }
}

struct ScriptedFactory {
    connections: VecDeque<ScriptedConnection>,
}

impl ScriptedFactory {
    fn new(connections: Vec<ScriptedConnection>) -> Self {
        Self {
            connections: connections.into(),
        }
    }
}

impl ConnectionFactory for ScriptedFactory {
    type Connection = ScriptedConnection;

    fn open(&mut self, _kind: ConnectionKind) -> Result<Self::Connection, TransportError> {
        self.connections.pop_front().ok_or(TransportError::Closed)
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

fn notification(method: &str, params: impl serde::Serialize) -> Frame {
    Frame::from_request(Request {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: String::new(),
        method: method.to_owned(),
        params: Some(serde_json::to_value(params).unwrap()),
    })
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
        prompt_activity: true,
        session_activity: true,
        process_output: true,
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
