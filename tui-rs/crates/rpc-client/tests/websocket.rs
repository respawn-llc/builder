use std::io::Read;
use std::net::{SocketAddr, TcpListener, UdpSocket};
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use rpc_client::endpoint::{new_unix_endpoint, parse_websocket_endpoint};
use rpc_client::transport::{FrameConnection, TransportError};
use rpc_client::websocket::{WebSocketDnsPolicy, WebSocketTransport, WebSocketTransportConfig};
use rpc_client::wire::{Frame, JSONRPC_VERSION, Request, Response};
use serde_json::json;
use tungstenite::Message;
use tungstenite::handshake::server::{
    Callback, ErrorResponse, Request as ServerRequest, Response as ServerResponse,
};

#[test]
fn websocket_transport_round_trips_plain_tcp_frames_with_origin_header() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let expected_origin = format!("http://{address}");
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket =
            tungstenite::accept_hdr(stream, OriginCallback { expected_origin }).unwrap();

        let request_frame: Frame =
            serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
        assert_eq!(request_frame.jsonrpc, JSONRPC_VERSION);
        assert_eq!(request_frame.id, "req-1");
        assert_eq!(request_frame.method, "test.ping");

        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: request_frame.id,
            result: Some(json!({"status": "ok"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let mut connection = WebSocketTransport::default().connect(&endpoint).unwrap();
    connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "req-1".to_owned(),
            method: "test.ping".to_owned(),
            params: Some(json!({"request": true})),
        }))
        .unwrap();

    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "req-1");
    assert_eq!(response.result, Some(json!({"status": "ok"})));

    server.join().unwrap();
}

#[cfg(unix)]
#[test]
fn websocket_transport_round_trips_unix_socket_without_leaking_socket_path() {
    use std::os::unix::net::UnixListener;
    use std::time::{SystemTime, UNIX_EPOCH};

    let unique = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let socket_path = std::env::temp_dir().join(format!("builder-rpc-{unique}.sock"));
    let _ = std::fs::remove_file(&socket_path);
    let listener = UnixListener::bind(&socket_path).unwrap();
    let socket_path_for_server = socket_path.clone();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept_hdr(
            stream,
            UnixHandshakeCallback {
                socket_path: socket_path_for_server,
            },
        )
        .unwrap();

        let request_frame: Frame =
            serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
        assert_eq!(request_frame.id, "req-uds-1");
        assert_eq!(request_frame.method, "test.unix");

        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: request_frame.id,
            result: Some(json!({"transport": "unix"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint = new_unix_endpoint(socket_path.to_str().unwrap(), "/rpc").unwrap();
    let mut connection = WebSocketTransport::default().connect(&endpoint).unwrap();
    connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "req-uds-1".to_owned(),
            method: "test.unix".to_owned(),
            params: Some(json!({"request": true})),
        }))
        .unwrap();

    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "req-uds-1");
    assert_eq!(response.result, Some(json!({"transport": "unix"})));

    server.join().unwrap();
    let _ = std::fs::remove_file(&socket_path);
}

#[test]
fn websocket_transport_reports_typed_handshake_timeout() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (mut stream, _) = listener.accept().unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(1)))
            .unwrap();
        let mut request = [0_u8; 4096];
        assert!(stream.read(&mut request).unwrap() > 0);
        let mut eof = [0_u8; 1];
        match stream.read(&mut eof) {
            Ok(0) | Err(_) => {}
            Ok(size) => panic!("unexpected extra handshake bytes after timeout: {size}"),
        }
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        handshake_timeout: Duration::from_millis(20),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();

    let error = match transport.connect(&endpoint) {
        Ok(_) => panic!("expected handshake timeout"),
        Err(error) => error,
    };
    assert_eq!(error, TransportError::HandshakeTimeout);

    server.join().unwrap();
}

#[test]
fn websocket_transport_timed_receive_is_non_terminal_after_handshake() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept(stream).unwrap();
        thread::sleep(Duration::from_millis(80));
        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "after-read-timeout".to_owned(),
            result: Some(json!({"status": "late"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let mut connection = WebSocketTransport::default().connect(&endpoint).unwrap();

    assert_eq!(
        connection
            .receive_with_timeout(Duration::from_millis(20))
            .unwrap_err(),
        TransportError::ReadTimeout
    );
    thread::sleep(Duration::from_millis(100));
    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "after-read-timeout");
    assert_eq!(response.result, Some(json!({"status": "late"})));

    server.join().unwrap();
}

#[test]
fn websocket_transport_reports_typed_dns_failure_for_unresolvable_hostname() {
    let blackhole = UdpSocket::bind("127.0.0.1:0").unwrap();
    let endpoint = parse_websocket_endpoint("ws://timeout-builder-tui.test:65535/rpc").unwrap();
    let error = match WebSocketTransport::new(WebSocketTransportConfig {
        connect_timeout: Duration::from_millis(200),
        resolve_timeout: Duration::from_millis(20),
        dns: WebSocketDnsPolicy::NameServers(vec![blackhole.local_addr().unwrap()]),
        ..WebSocketTransportConfig::default()
    })
    .unwrap()
    .connect(&endpoint)
    {
        Ok(_) => panic!("expected DNS name to fail resolution"),
        Err(error) => error,
    };

    assert_eq!(error, TransportError::ResolveTimeout);
}

#[test]
fn websocket_transport_connects_localhost_without_dns_resolution() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let endpoint_url = format!("ws://localhost:{}/rpc", address.port());
    let expected_origin = format!("http://localhost:{}", address.port());
    let expected_host = format!("localhost:{}", address.port());
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept_hdr(
            stream,
            LocalhostHandshakeCallback {
                expected_origin,
                expected_host,
            },
        )
        .unwrap();

        let request_frame: Frame =
            serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: request_frame.id,
            result: Some(json!({"status": "localhost"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint = parse_websocket_endpoint(&endpoint_url).unwrap();
    let mut connection = WebSocketTransport::default().connect(&endpoint).unwrap();
    connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "req-localhost-1".to_owned(),
            method: "test.localhost".to_owned(),
            params: Some(json!({})),
        }))
        .unwrap();

    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "req-localhost-1");
    assert_eq!(response.result, Some(json!({"status": "localhost"})));

    server.join().unwrap();
}

struct OriginCallback {
    expected_origin: String,
}

impl Callback for OriginCallback {
    fn on_request(
        self,
        request: &ServerRequest,
        response: ServerResponse,
    ) -> Result<ServerResponse, ErrorResponse> {
        let origin = request
            .headers()
            .get("origin")
            .and_then(|value| value.to_str().ok())
            .unwrap_or("");
        assert_eq!(origin, self.expected_origin);
        Ok(response)
    }
}

struct UnixHandshakeCallback {
    socket_path: std::path::PathBuf,
}

impl Callback for UnixHandshakeCallback {
    fn on_request(
        self,
        request: &ServerRequest,
        response: ServerResponse,
    ) -> Result<ServerResponse, ErrorResponse> {
        assert_eq!(request.uri().path(), "/rpc");
        assert!(
            !request
                .uri()
                .to_string()
                .contains(self.socket_path.to_str().unwrap())
        );
        let host = request
            .headers()
            .get("host")
            .and_then(|value| value.to_str().ok())
            .unwrap_or("");
        assert_eq!(host, "builder.local");
        let origin = request
            .headers()
            .get("origin")
            .and_then(|value| value.to_str().ok())
            .unwrap_or("");
        assert_eq!(origin, "http://builder.local");
        Ok(response)
    }
}

struct LocalhostHandshakeCallback {
    expected_origin: String,
    expected_host: String,
}

impl Callback for LocalhostHandshakeCallback {
    fn on_request(
        self,
        request: &ServerRequest,
        response: ServerResponse,
    ) -> Result<ServerResponse, ErrorResponse> {
        let origin = request
            .headers()
            .get("origin")
            .and_then(|value| value.to_str().ok())
            .unwrap_or("");
        assert_eq!(origin, self.expected_origin);
        let host = request
            .headers()
            .get("host")
            .and_then(|value| value.to_str().ok())
            .unwrap_or("");
        assert_eq!(host, self.expected_host);
        Ok(response)
    }
}

#[test]
fn websocket_transport_rejects_untrusted_wss_without_plain_websocket_handshake() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let endpoint = parse_websocket_endpoint(&format!("wss://{address}/rpc")).unwrap();

    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        handshake_timeout: Duration::from_millis(20),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();
    let error = match transport.connect(&endpoint) {
        Ok(_) => panic!("expected TLS endpoint to fail without a TLS server"),
        Err(error) => error,
    };

    assert!(matches!(
        error,
        TransportError::HandshakeTimeout
            | TransportError::HandshakeFailed(_)
            | TransportError::ConfigurationInvalid(_)
    ));
    assert!(listener.set_nonblocking(true).is_ok());
    if let Ok((mut stream, _)) = listener.accept() {
        let mut handshake_prefix = [0_u8; 4];
        stream
            .set_read_timeout(Some(Duration::from_millis(20)))
            .unwrap();
        match stream.read(&mut handshake_prefix) {
            Ok(0) | Err(_) => {}
            Ok(size) if handshake_prefix[..size].starts_with(b"GET") => {
                panic!("unexpected plaintext WebSocket handshake on TLS listener")
            }
            Ok(_) => {}
        }
    }
}

#[test]
fn websocket_transport_receive_errors_and_control_frames_are_terminal_or_filtered() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept(stream).unwrap();
        socket.send(Message::text("{")).unwrap();
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let mut connection = WebSocketTransport::default().connect(&endpoint).unwrap();
    let error = connection.receive().unwrap_err();
    assert!(matches!(error, TransportError::DecodeFailed(_)));
    let closed = connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "after-decode-error".to_owned(),
            method: "test.afterDecodeError".to_owned(),
            params: Some(json!({})),
        }))
        .unwrap_err();
    assert_eq!(closed, TransportError::Closed);

    server.join().unwrap();

    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept(stream).unwrap();
        socket.send(Message::Ping(vec![1, 2, 3].into())).unwrap();
        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "after-ping".to_owned(),
            result: Some(json!({"status": "after-control"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let mut connection = WebSocketTransport::default().connect(&endpoint).unwrap();
    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "after-ping");
    assert_eq!(response.result, Some(json!({"status": "after-control"})));

    server.join().unwrap();
}

#[test]
fn websocket_transport_backpressure_closes_connection_without_later_flush() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept(stream).unwrap();
        match socket.read() {
            Ok(Message::Close(_)) | Err(_) => {}
            Ok(message) => panic!("unexpected message after client backpressure: {message:?}"),
        }
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        write_buffer_size: 1,
        max_write_buffer_size: 2,
        ..WebSocketTransportConfig::default()
    })
    .unwrap();
    let mut connection = transport.connect(&endpoint).unwrap();
    let error = connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "too-large-for-buffer".to_owned(),
            method: "test.backpressure".to_owned(),
            params: Some(json!({"payload": "this frame is larger than two bytes"})),
        }))
        .unwrap_err();
    assert_eq!(error, TransportError::Backpressure);
    let closed = connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "must-not-flush-later".to_owned(),
            method: "test.afterBackpressure".to_owned(),
            params: Some(json!({})),
        }))
        .unwrap_err();
    assert_eq!(closed, TransportError::Closed);
    assert_eq!(connection.receive().unwrap_err(), TransportError::Closed);

    server.join().unwrap();
}

#[test]
fn websocket_transport_close_is_idempotent_and_raw_shutdown_reaches_server() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept(stream).unwrap();
        socket
            .get_mut()
            .set_read_timeout(Some(Duration::from_secs(1)))
            .unwrap();
        match socket.read() {
            Ok(Message::Close(_)) | Err(_) => {}
            Ok(message) => panic!("unexpected message after client close: {message:?}"),
        }
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let mut connection = WebSocketTransport::default().connect(&endpoint).unwrap();
    connection.close().unwrap();
    connection.close().unwrap();
    assert_eq!(
        connection
            .send(Frame::from_request(Request {
                jsonrpc: JSONRPC_VERSION.to_owned(),
                id: "after-close".to_owned(),
                method: "test.afterClose".to_owned(),
                params: Some(json!({})),
            }))
            .unwrap_err(),
        TransportError::Closed
    );

    server.join().unwrap();
}

#[test]
fn websocket_transport_write_failure_closes_without_later_flush() {
    let listener = small_receive_buffer_listener();
    let address = listener.local_addr().unwrap();
    let (client_done_tx, client_done_rx) = mpsc::channel();
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept(stream).unwrap();
        client_done_rx.recv().unwrap();
        socket
            .get_mut()
            .set_read_timeout(Some(Duration::from_secs(1)))
            .unwrap();
        match socket.read() {
            Ok(Message::Close(_)) | Err(_) => {}
            Ok(message) => panic!("unexpected complete message after write failure: {message:?}"),
        }
    });

    let endpoint = parse_websocket_endpoint(&format!("ws://{address}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        write_timeout: Duration::from_millis(1),
        max_write_buffer_size: 128 * 1024 * 1024,
        max_message_size: 128 * 1024 * 1024,
        max_frame_size: 128 * 1024 * 1024,
        ..WebSocketTransportConfig::default()
    })
    .unwrap();
    let mut connection = transport.connect(&endpoint).unwrap();
    let result = connection.send(Frame::from_request(Request {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: "write-timeout".to_owned(),
        method: "test.writeTimeout".to_owned(),
        params: Some(json!({"payload": "x".repeat(8 * 1024 * 1024)})),
    }));
    client_done_tx.send(()).unwrap();
    let error = result.unwrap_err();
    assert!(
        matches!(
            error,
            TransportError::WriteTimeout | TransportError::SendFailed(_) | TransportError::Closed
        ),
        "unexpected send error: {error:?}"
    );
    assert_eq!(
        connection
            .send(Frame::from_request(Request {
                jsonrpc: JSONRPC_VERSION.to_owned(),
                id: "after-write-failure".to_owned(),
                method: "test.afterWriteFailure".to_owned(),
                params: Some(json!({})),
            }))
            .unwrap_err(),
        TransportError::Closed
    );
    assert_eq!(connection.receive().unwrap_err(), TransportError::Closed);

    server.join().unwrap();
}

fn small_receive_buffer_listener() -> TcpListener {
    let socket = socket2::Socket::new(
        socket2::Domain::IPV4,
        socket2::Type::STREAM,
        Some(socket2::Protocol::TCP),
    )
    .unwrap();
    socket.set_reuse_address(true).unwrap();
    socket.set_recv_buffer_size(1024).unwrap();
    let address: SocketAddr = "127.0.0.1:0".parse().unwrap();
    socket.bind(&socket2::SockAddr::from(address)).unwrap();
    socket.listen(1).unwrap();
    socket.into()
}
