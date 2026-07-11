use std::io::{Read, Write};
use std::net::TcpListener;
use std::thread;

use rpc_client::api::{PROTOCOL_VERSION, RemoteClient, RemoteContext};
use rpc_client::endpoint::parse_websocket_endpoint;
use rpc_client::transport::FrameConnection;
use rpc_client::websocket::{EndpointConnectionFactory, WebSocketTransport};
use rpc_client::wire::{Frame, JSONRPC_VERSION, Response};
use serde_json::json;
use tungstenite::Message;

#[test]
fn websocket_endpoint_factory_runs_remote_client_setup_over_real_frames() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept(stream).unwrap();

        respond_to_project_setup(&mut socket);
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let factory = EndpointConnectionFactory::new(endpoint, WebSocketTransport::default());
    let mut remote = RemoteClient::new(
        factory,
        RemoteContext::project("project-1", "workspace-1", ""),
    );
    let mut connection = remote.open_project_connection().unwrap();
    connection.close().unwrap();

    server.join().unwrap();
}

#[test]
fn websocket_endpoint_factory_runs_dedicated_call_over_real_frames() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept(stream).unwrap();

        respond_to_project_setup(&mut socket);

        let dedicated: Frame = serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
        assert_eq!(dedicated.id, "dedicated-test");
        assert_eq!(dedicated.method, "test.dedicated");
        assert_eq!(dedicated.params, Some(json!({"request": "dedicated"})));
        socket
            .send(Message::text(
                serde_json::to_string(&Frame::from_response(Response {
                    jsonrpc: JSONRPC_VERSION.to_owned(),
                    id: dedicated.id,
                    result: Some(json!({"response": "dedicated"})),
                    error: None,
                }))
                .unwrap(),
            ))
            .unwrap();
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let factory = EndpointConnectionFactory::new(endpoint, WebSocketTransport::default());
    let mut remote = RemoteClient::new(
        factory,
        RemoteContext::project("project-1", "workspace-1", ""),
    );
    let (response, mut connection) = remote
        .call_dedicated(
            "dedicated-test",
            "test.dedicated",
            json!({"request": "dedicated"}),
        )
        .unwrap();
    assert_eq!(response, json!({"response": "dedicated"}));
    connection.close().unwrap();

    server.join().unwrap();
}

fn respond_to_project_setup<S>(socket: &mut tungstenite::WebSocket<S>)
where
    S: Read + Write,
{
    let handshake: Frame = serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
    assert_eq!(handshake.id, "handshake");
    assert_eq!(handshake.method, "protocol.handshake");
    assert_eq!(
        handshake.params,
        Some(json!({"protocol_version": PROTOCOL_VERSION}))
    );
    socket
        .send(Message::text(
            serde_json::to_string(&Frame::from_response(Response {
                jsonrpc: JSONRPC_VERSION.to_owned(),
                id: handshake.id,
                result: Some(json!({
                    "identity": {
                        "protocol_version": PROTOCOL_VERSION,
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
                })),
                error: None,
            }))
            .unwrap(),
        ))
        .unwrap();

    let attach: Frame = serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
    assert_eq!(attach.id, "attach-project");
    assert_eq!(attach.method, "project.attach");
    assert_eq!(
        attach.params,
        Some(json!({
            "project_id": "project-1",
            "workspace_id": "workspace-1"
        }))
    );
    socket
        .send(Message::text(
            serde_json::to_string(&Frame::from_response(Response {
                jsonrpc: JSONRPC_VERSION.to_owned(),
                id: attach.id,
                result: Some(json!({
                    "kind": "project",
                    "project_id": "project-1",
                    "workspace_id": "workspace-1"
                })),
                error: None,
            }))
            .unwrap(),
        ))
        .unwrap();
}
