use std::cell::RefCell;
use std::collections::VecDeque;
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use client_contracts::protocol::HandshakeRequest;
use rpc_client::api::{Client, PROTOCOL_VERSION, RemoteClient, RemoteContext, RpcIoGuard};
use rpc_client::error::RpcError;
use rpc_client::json_rpc::{CallCancellation, JsonRpcConnection};
use rpc_client::transport::{ConnectionFactory, ConnectionKind, FrameConnection, TransportError};
use rpc_client::wire::{Frame, JSONRPC_VERSION, Response};
use serde_json::json;

#[test]
fn receive_pending_with_timeout_preserves_pending_ready_and_cancel_semantics() {
    let incoming = Rc::new(RefCell::new(VecDeque::from([success_response(
        "rpc-2",
        json!({"ok":true}),
    )])));
    let mut rpc = JsonRpcConnection::new(ScriptedConnection::new(incoming.clone()));
    let first = rpc.start_call("first.method", json!({})).unwrap();
    let second = rpc.start_call("second.method", json!({})).unwrap();

    let error = rpc
        .receive_pending_with_timeout(&first, Duration::from_millis(25))
        .unwrap_err();

    assert_eq!(
        error,
        RpcError::Transport(TransportError::ReadTimeout)
    );
    assert_eq!(rpc.pending_count(), 2);
    assert_eq!(rpc.ready_count(), 1);
    assert_eq!(rpc.receive_pending(&second).unwrap(), json!({"ok":true}));
    assert_eq!(rpc.pending_count(), 1);

    rpc.cancel_pending(&first, CallCancellation::Local).unwrap();
    assert_eq!(
        rpc.receive_pending_with_timeout(&first, Duration::from_millis(25)),
        Err(RpcError::RequestCanceledLocally)
    );

    let connection = rpc.into_connection();
    assert_eq!(
        connection.timeout_log.borrow().as_slice(),
        &[Duration::from_millis(25), Duration::from_millis(25)]
    );
    assert_eq!(connection.unbounded_receive_count(), 0);
    assert_eq!(incoming.borrow().len(), 0);
}

#[test]
fn call_and_call_fixed_with_timeout_decode_responses_from_timed_receive_path() {
    let incoming = Rc::new(RefCell::new(VecDeque::from([
        success_response("fixed-id", json!({"fixed":true})),
        success_response("rpc-1", json!({"generated":true})),
    ])));
    let mut rpc = JsonRpcConnection::new(ScriptedConnection::new(incoming));

    let fixed: serde_json::Value = rpc
        .call_fixed_with_timeout(
            "fixed-id",
            "fixed.method",
            &json!({}),
            Duration::from_millis(30),
        )
        .unwrap();
    let generated: serde_json::Value = rpc
        .call_with_timeout("generated.method", &json!({}), Duration::from_millis(30))
        .unwrap();

    let connection = rpc.into_connection();
    assert_eq!(fixed, json!({"fixed":true}));
    assert_eq!(generated, json!({"generated":true}));
    assert_eq!(
        connection.timeout_log.borrow().as_slice(),
        &[Duration::from_millis(30), Duration::from_millis(30)]
    );
    assert_eq!(connection.unbounded_receive_count(), 0);
}

#[test]
fn call_with_timeout_does_not_leave_unreachable_pending_request_after_timeout() {
    let incoming = Rc::new(RefCell::new(VecDeque::new()));
    let mut rpc = JsonRpcConnection::new(ScriptedConnection::new(incoming));

    let error = rpc
        .call_with_timeout::<_, serde_json::Value>(
            "timeout.method",
            &json!({}),
            Duration::from_millis(30),
        )
        .unwrap_err();

    assert_eq!(error, RpcError::Transport(TransportError::ReadTimeout));
    assert_eq!(rpc.pending_count(), 0);
    assert_eq!(rpc.ready_count(), 0);
}

#[test]
fn call_fixed_with_timeout_does_not_leave_unreachable_pending_request_after_timeout() {
    let incoming = Rc::new(RefCell::new(VecDeque::new()));
    let mut rpc = JsonRpcConnection::new(ScriptedConnection::new(incoming));

    let error = rpc
        .call_fixed_with_timeout::<_, serde_json::Value>(
            "fixed-timeout",
            "timeout.fixed",
            &json!({}),
            Duration::from_millis(30),
        )
        .unwrap_err();

    assert_eq!(error, RpcError::Transport(TransportError::ReadTimeout));
    assert_eq!(rpc.pending_count(), 0);
    assert_eq!(rpc.ready_count(), 0);
}

#[test]
fn default_call_paths_remain_unbounded() {
    let incoming = Rc::new(RefCell::new(VecDeque::from([success_response(
        "rpc-1",
        json!({"ok":true}),
    )])));
    let mut rpc = JsonRpcConnection::new(ScriptedConnection::new(incoming));

    let response: serde_json::Value = rpc.call("default.method", &json!({})).unwrap();

    let connection = rpc.into_connection();
    assert_eq!(response, json!({"ok":true}));
    assert!(connection.timeout_log.borrow().is_empty());
    assert_eq!(connection.unbounded_receive_count(), 1);
}

#[test]
fn remote_client_unary_timeout_is_used_for_setup_and_unary_call_receives() {
    let timeout_log = Rc::new(RefCell::new(Vec::new()));
    let connection = ScriptedConnection::with_timeout_log(
        Rc::new(RefCell::new(VecDeque::from([
            success_response(
                "handshake",
                json!({
                    "identity": {
                        "protocol_version": "16",
                        "server_id": "server-1",
                        "pid": 123,
                        "capabilities": {
                            "jsonrpc_websocket": true,
                            "auth_bootstrap": true,
                            "project_attach": true,
                            "session_attach": true,
                            "health_endpoint": true,
                            "readiness_endpoint": true,
                            "run_prompt": true,
                            "session_plan": true,
                            "session_lifecycle": true,
                            "session_transcript_paging": true,
                            "session_runtime": true,
                            "runtime_control": true,
                            "prompt_control": true,
                            "prompt_activity": true,
                            "session_activity": true,
                            "process_output": true
                        }
                    }
                }),
            ),
            success_response("rpc-1", json!({"ok":true})),
        ]))),
        timeout_log.clone(),
    );
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![connection]),
        RemoteContext::unscoped(),
    )
    .with_unary_receive_timeout(Duration::from_millis(40));

    let (response, connection) = remote.call_control("test.method", json!({})).unwrap();

    assert_eq!(response, json!({"ok":true}));
    assert_eq!(
        timeout_log.borrow().as_slice(),
        &[Duration::from_millis(40), Duration::from_millis(40)]
    );
    assert_eq!(connection.unbounded_receive_count(), 0);
}

#[test]
fn direct_client_unary_timeout_is_used_for_startup_control_calls() {
    let timeout_log = Rc::new(RefCell::new(Vec::new()));
    let connection = ScriptedConnection::with_timeout_log(
        Rc::new(RefCell::new(VecDeque::from([success_response(
            "handshake",
            json!({
                "identity": {
                    "protocol_version": "16",
                    "server_id": "server-1",
                    "pid": 123,
                    "capabilities": {
                        "jsonrpc_websocket": true,
                        "auth_bootstrap": true,
                        "project_attach": true,
                        "session_attach": true,
                        "health_endpoint": true,
                        "readiness_endpoint": true,
                        "run_prompt": true,
                        "session_plan": true,
                        "session_lifecycle": true,
                        "session_transcript_paging": true,
                        "session_runtime": true,
                        "runtime_control": true,
                        "prompt_control": true,
                        "prompt_activity": true,
                        "session_activity": true,
                        "process_output": true
                    }
                }
            }),
        )]))),
        timeout_log.clone(),
    );
    let mut client = Client::new(connection).with_unary_receive_timeout(Duration::from_millis(45));

    let response = client
        .handshake(HandshakeRequest {
            protocol_version: PROTOCOL_VERSION.to_owned(),
        })
        .unwrap();

    assert_eq!(response.identity.server_id, "server-1");
    let connection = client.into_connection();
    assert_eq!(timeout_log.borrow().as_slice(), &[Duration::from_millis(45)]);
    assert_eq!(connection.unbounded_receive_count(), 0);
}

#[test]
fn direct_and_remote_clients_invoke_io_guard_before_rpc_calls() {
    let direct_guard = CountingGuard::default();
    let direct_count = direct_guard.count();
    let direct_connection = ScriptedConnection::new(Rc::new(RefCell::new(VecDeque::from([
        success_response(
            "handshake",
            json!({
                "identity": {
                    "protocol_version": "16",
                    "server_id": "server-1",
                    "pid": 123,
                    "capabilities": {
                        "jsonrpc_websocket": true,
                        "auth_bootstrap": true,
                        "project_attach": true,
                        "session_attach": true,
                        "health_endpoint": true,
                        "readiness_endpoint": true,
                        "run_prompt": true,
                        "session_plan": true,
                        "session_lifecycle": true,
                        "session_transcript_paging": true,
                        "session_runtime": true,
                        "runtime_control": true,
                        "prompt_control": true,
                        "prompt_activity": true,
                        "session_activity": true,
                        "process_output": true
                    }
                }
            }),
        ),
    ]))));
    let mut direct = Client::new(direct_connection).with_io_guard(Arc::new(direct_guard));

    direct
        .handshake(HandshakeRequest {
            protocol_version: PROTOCOL_VERSION.to_owned(),
        })
        .unwrap();

    assert_eq!(direct_count.load(Ordering::SeqCst), 1);

    let remote_guard = CountingGuard::default();
    let remote_count = remote_guard.count();
    let remote_connection = ScriptedConnection::new(Rc::new(RefCell::new(VecDeque::from([
        success_response(
            "handshake",
            json!({
                "identity": {
                    "protocol_version": "16",
                    "server_id": "server-1",
                    "pid": 123,
                    "capabilities": {
                        "jsonrpc_websocket": true,
                        "auth_bootstrap": true,
                        "project_attach": true,
                        "session_attach": true,
                        "health_endpoint": true,
                        "readiness_endpoint": true,
                        "run_prompt": true,
                        "session_plan": true,
                        "session_lifecycle": true,
                        "session_transcript_paging": true,
                        "session_runtime": true,
                        "runtime_control": true,
                        "prompt_control": true,
                        "prompt_activity": true,
                        "session_activity": true,
                        "process_output": true
                    }
                }
            }),
        ),
        success_response("rpc-1", json!({"ok":true})),
    ]))));
    let mut remote = RemoteClient::new(
        ScriptedFactory::new(vec![remote_connection]),
        RemoteContext::unscoped(),
    )
    .with_io_guard(Arc::new(remote_guard));

    let (response, _connection) = remote.call_control("test.method", json!({})).unwrap();

    assert_eq!(response, json!({"ok":true}));
    assert!(remote_count.load(Ordering::SeqCst) >= 2);
}

#[derive(Default)]
struct CountingGuard {
    count: Arc<AtomicUsize>,
}

impl CountingGuard {
    fn count(&self) -> Arc<AtomicUsize> {
        Arc::clone(&self.count)
    }
}

impl RpcIoGuard for CountingGuard {
    fn assert_rpc_io_allowed(&self, _operation: &'static str) {
        self.count.fetch_add(1, Ordering::SeqCst);
    }
}

struct ScriptedConnection {
    sent: Vec<Frame>,
    incoming: Rc<RefCell<VecDeque<Frame>>>,
    timeout_log: Rc<RefCell<Vec<Duration>>>,
    unbounded_receive_count: Rc<RefCell<usize>>,
}

impl ScriptedConnection {
    fn new(incoming: Rc<RefCell<VecDeque<Frame>>>) -> Self {
        Self::with_timeout_log(incoming, Rc::new(RefCell::new(Vec::new())))
    }

    fn with_timeout_log(
        incoming: Rc<RefCell<VecDeque<Frame>>>,
        timeout_log: Rc<RefCell<Vec<Duration>>>,
    ) -> Self {
        Self {
            sent: Vec::new(),
            incoming,
            timeout_log,
            unbounded_receive_count: Rc::new(RefCell::new(0)),
        }
    }

    fn unbounded_receive_count(&self) -> usize {
        *self.unbounded_receive_count.borrow()
    }
}

impl FrameConnection for ScriptedConnection {
    fn send(&mut self, frame: Frame) -> Result<(), TransportError> {
        self.sent.push(frame);
        Ok(())
    }

    fn receive(&mut self) -> Result<Frame, TransportError> {
        *self.unbounded_receive_count.borrow_mut() += 1;
        self.incoming
            .borrow_mut()
            .pop_front()
            .ok_or(TransportError::Closed)
    }

    fn receive_with_timeout(&mut self, timeout: Duration) -> Result<Frame, TransportError> {
        self.timeout_log.borrow_mut().push(timeout);
        self.incoming
            .borrow_mut()
            .pop_front()
            .ok_or(TransportError::ReadTimeout)
    }

    fn close(&mut self) -> Result<(), TransportError> {
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
