use std::net::{IpAddr, Ipv4Addr, TcpListener, UdpSocket};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use rpc_client::endpoint::parse_websocket_endpoint;
use rpc_client::transport::{FrameConnection, TransportError};
use rpc_client::websocket::{
    WebSocketDnsPolicy, WebSocketTlsPolicy, WebSocketTransport, WebSocketTransportConfig,
};
use rpc_client::wire::{Frame, JSONRPC_VERSION, Request, Response};
use serde_json::json;
use test_support::transport::{LocalDnsServer, local_tls_certificate};
use tungstenite::Message;
use tungstenite::handshake::server::{
    Callback, ErrorResponse, Request as ServerRequest, Response as ServerResponse,
};

#[test]
fn websocket_transport_resolves_dns_hostname_and_preserves_host_headers() {
    let dns = LocalDnsServer::resolve_hostname_to(
        "builder-tui.test.",
        IpAddr::V4(Ipv4Addr::LOCALHOST),
    )
    .unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let expected_origin = format!("http://builder-tui.test:{port}");
    let expected_host = format!("builder-tui.test:{port}");
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let mut socket = tungstenite::accept_hdr(
            stream,
            DnsHandshakeCallback {
                expected_origin,
                expected_host,
            },
        )
        .unwrap();

        let request_frame: Frame =
            serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
        assert_eq!(request_frame.jsonrpc, JSONRPC_VERSION);
        assert_eq!(request_frame.id, "req-dns-1");
        assert_eq!(request_frame.method, "test.dns");

        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: request_frame.id,
            result: Some(json!({"status": "dns"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint =
        parse_websocket_endpoint(&format!("ws://builder-tui.test:{port}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        dns: WebSocketDnsPolicy::NameServers(vec![dns.address()]),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();
    let mut connection = transport.connect(&endpoint).unwrap();
    connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "req-dns-1".to_owned(),
            method: "test.dns".to_owned(),
            params: Some(json!({"request": true})),
        }))
        .unwrap();

    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "req-dns-1");
    assert_eq!(response.result, Some(json!({"status": "dns"})));

    server.join().unwrap();
}

#[test]
fn websocket_transport_reports_dns_resolution_timeout_without_tcp_attempt() {
    let blackhole = UdpSocket::bind("127.0.0.1:0").unwrap();
    let endpoint =
        parse_websocket_endpoint("ws://timeout-builder-tui.test:65535/rpc").unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        connect_timeout: Duration::from_millis(200),
        resolve_timeout: Duration::from_millis(20),
        dns: WebSocketDnsPolicy::NameServers(vec![blackhole.local_addr().unwrap()]),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();

    let started_at = Instant::now();
    let error = match transport.connect(&endpoint) {
        Ok(_) => panic!("expected DNS timeout"),
        Err(error) => error,
    };

    assert_eq!(error, TransportError::ResolveTimeout);
    assert!(
        started_at.elapsed() < Duration::from_millis(500),
        "DNS timeout should be bounded by resolver timeout"
    );
}

#[test]
fn websocket_transport_reports_dns_resolution_failure_without_tcp_attempt() {
    let dns = LocalDnsServer::resolve_hostname_to(
        "other-builder-tui.test.",
        IpAddr::V4(Ipv4Addr::LOCALHOST),
    )
    .unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let port = listener.local_addr().unwrap().port();
    let endpoint =
        parse_websocket_endpoint(&format!("ws://missing-builder-tui.test:{port}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        connect_timeout: Duration::from_millis(200),
        resolve_timeout: Duration::from_millis(100),
        dns: WebSocketDnsPolicy::NameServers(vec![dns.address()]),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();

    let error = match transport.connect(&endpoint) {
        Ok(_) => panic!("expected DNS failure"),
        Err(error) => error,
    };

    assert!(matches!(error, TransportError::ResolveFailed(_)));
    assert!(
        listener.accept().is_err(),
        "TCP listener must not receive a connection when DNS returns no address"
    );
}

#[test]
fn websocket_transport_resolves_dns_hostname_for_verified_tls_connection() {
    let dns = LocalDnsServer::resolve_hostname_to(
        "builder-tui.test.",
        IpAddr::V4(Ipv4Addr::LOCALHOST),
    )
    .unwrap();
    let certificate = local_tls_certificate(["builder-tui.test"]).unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let expected_origin = format!("https://builder-tui.test:{port}");
    let expected_host = format!("builder-tui.test:{port}");
    let server_config = Arc::clone(&certificate.server_config);
    let server = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        let tls_connection = rustls::ServerConnection::new(server_config).unwrap();
        let tls_stream = rustls::StreamOwned::new(tls_connection, stream);
        let mut socket = tungstenite::accept_hdr(
            tls_stream,
            DnsHandshakeCallback {
                expected_origin,
                expected_host,
            },
        )
        .unwrap();

        let request_frame: Frame =
            serde_json::from_slice(&socket.read().unwrap().into_data()).unwrap();
        assert_eq!(request_frame.jsonrpc, JSONRPC_VERSION);
        assert_eq!(request_frame.id, "req-dns-tls-1");
        assert_eq!(request_frame.method, "test.dnsTls");

        let response = Frame::from_response(Response {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: request_frame.id,
            result: Some(json!({"status": "dns-tls"})),
            error: None,
        });
        socket
            .send(Message::text(serde_json::to_string(&response).unwrap()))
            .unwrap();
    });

    let endpoint =
        parse_websocket_endpoint(&format!("wss://builder-tui.test:{port}/rpc")).unwrap();
    let transport = WebSocketTransport::new(WebSocketTransportConfig {
        dns: WebSocketDnsPolicy::NameServers(vec![dns.address()]),
        tls: WebSocketTlsPolicy::CustomRootCertificates(certificate.roots),
        ..WebSocketTransportConfig::default()
    })
    .unwrap();
    let mut connection = transport.connect(&endpoint).unwrap();
    connection
        .send(Frame::from_request(Request {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: "req-dns-tls-1".to_owned(),
            method: "test.dnsTls".to_owned(),
            params: Some(json!({"request": true})),
        }))
        .unwrap();

    let response = connection.receive().unwrap().response();
    assert_eq!(response.id, "req-dns-tls-1");
    assert_eq!(response.result, Some(json!({"status": "dns-tls"})));

    server.join().unwrap();
}

struct DnsHandshakeCallback {
    expected_origin: String,
    expected_host: String,
}

impl Callback for DnsHandshakeCallback {
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
