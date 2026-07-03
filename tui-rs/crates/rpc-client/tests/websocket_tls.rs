use std::io::Read;
use std::net::TcpListener;
use std::sync::Arc;
use std::thread;
use std::time::Duration;

use rpc_client::endpoint::parse_websocket_endpoint;
use rpc_client::transport::{FrameConnection, TransportError};
use rpc_client::websocket::{WebSocketTlsPolicy, WebSocketTransport, WebSocketTransportConfig};
use rpc_client::wire::{Frame, JSONRPC_VERSION, Request, Response};
use serde_json::json;
use test_support::transport::local_tls_certificate;
use tungstenite::Message;
use tungstenite::handshake::server::{
    Callback, ErrorResponse, Request as ServerRequest, Response as ServerResponse,
};

#[test]
fn websocket_transport_round_trips_tls_frames_with_verified_certificate() {
    let certificate = local_tls_certificate(["127.0.0.1"]).unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let expected_origin = format!("https://{address}");
    let server_config = Arc::clone(&certificate.server_config);
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let tls_connection = rustls::ServerConnection::new(server_config).unwrap();
        let tls_stream = rustls::StreamOwned::new(tls_connection, stream);
        let mut socket =
            tungstenite::accept_hdr(tls_stream, TlsOriginCallback { expected_origin }).unwrap();

        let request_frame: Frame =
            serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
        assert_eq!(request_frame.jsonrpc, JSONRPC_VERSION);
        assert_eq!(request_frame.id, "req-tls-1");
        assert_eq!(request_frame.method, "test.tls");

        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: request_frame.id,
            result: Some(json!({"status": "secure"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint = parse_websocket_endpoint(&format!("wss://{address}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        tls: WebSocketTlsPolicy::CustomRootCertificates(certificate.roots),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();
    let mut connection = transport.connect(&endpoint).unwrap();
    connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "req-tls-1".to_owned(),
            method: "test.tls".to_owned(),
            params: Some(json!({"request": true})),
        }))
        .unwrap();

    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "req-tls-1");
    assert_eq!(response.result, Some(json!({"status": "secure"})));

    server.join().unwrap();
}

#[test]
fn websocket_transport_rejects_untrusted_tls_certificate_before_websocket_request() {
    let certificate = local_tls_certificate(["127.0.0.1"]).unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server_config = Arc::clone(&certificate.server_config);
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(60)))
            .unwrap();
        stream
            .set_write_timeout(Some(Duration::from_secs(60)))
            .unwrap();
        let tls_connection = rustls::ServerConnection::new(server_config).unwrap();
        let tls_stream = rustls::StreamOwned::new(tls_connection, stream);
        tungstenite::accept(tls_stream).is_ok()
    });

    let endpoint = parse_websocket_endpoint(&format!("wss://{address}/rpc")).unwrap();
    let unrelated_certificate = local_tls_certificate(["127.0.0.1"]).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        handshake_timeout: Duration::from_secs(30),
        tls: WebSocketTlsPolicy::CustomRootCertificates(unrelated_certificate.roots),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();
    let error = match transport.connect(&endpoint) {
        Ok(_) => panic!("expected untrusted TLS certificate to fail"),
        Err(error) => error,
    };
    assert!(
        matches!(error, TransportError::HandshakeFailed(_)),
        "unexpected error: {error:?}"
    );
    assert!(
        !server.join().unwrap(),
        "untrusted TLS connection must not complete a WebSocket handshake"
    );
}

#[test]
fn websocket_transport_reports_typed_tls_handshake_timeout() {
    let certificate = local_tls_certificate(["127.0.0.1"]).unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = thread::spawn(move || {
        let (mut stream, _) = listener.accept().unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(1)))
            .unwrap();
        let mut request = [0_u8; 4096];
        assert!(stream.read(&mut request).unwrap() > 0);
        thread::sleep(Duration::from_millis(100));
    });

    let endpoint = parse_websocket_endpoint(&format!("wss://{address}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        handshake_timeout: Duration::from_millis(20),
        tls: WebSocketTlsPolicy::CustomRootCertificates(certificate.roots),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();

    let error = match transport.connect(&endpoint) {
        Ok(_) => panic!("expected stalled TLS handshake to time out"),
        Err(error) => error,
    };
    assert_eq!(error, TransportError::HandshakeTimeout);

    server.join().unwrap();
}

#[test]
fn websocket_transport_round_trips_ipv6_tls_frames_with_verified_certificate() {
    let certificate = local_tls_certificate(["::1"]).unwrap();
    let listener = TcpListener::bind("[::1]:0").unwrap();
    let address = listener.local_addr().unwrap();
    let expected_origin = format!("https://{address}");
    let server_config = Arc::clone(&certificate.server_config);
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let tls_connection = rustls::ServerConnection::new(server_config).unwrap();
        let tls_stream = rustls::StreamOwned::new(tls_connection, stream);
        let mut socket =
            tungstenite::accept_hdr(tls_stream, TlsOriginCallback { expected_origin }).unwrap();

        let request_frame: Frame =
            serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
        assert_eq!(request_frame.id, "req-ipv6-tls-1");
        assert_eq!(request_frame.method, "test.ipv6Tls");

        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: request_frame.id,
            result: Some(json!({"status": "ipv6-secure"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint = parse_websocket_endpoint(&format!("wss://{address}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        tls: WebSocketTlsPolicy::CustomRootCertificates(certificate.roots),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();
    let mut connection = transport.connect(&endpoint).unwrap();
    connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "req-ipv6-tls-1".to_owned(),
            method: "test.ipv6Tls".to_owned(),
            params: Some(json!({"request": true})),
        }))
        .unwrap();

    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "req-ipv6-tls-1");
    assert_eq!(response.result, Some(json!({"status": "ipv6-secure"})));

    server.join().unwrap();
}

struct TlsOriginCallback {
    expected_origin: String,
}

impl Callback for TlsOriginCallback {
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
