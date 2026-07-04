use rpc_client::endpoint::{
    EndpointParseError, TransportKind, new_unix_endpoint, parse_websocket_endpoint,
};

#[test]
fn websocket_endpoint_applies_go_defaults_and_preserves_urls() {
    let endpoint = parse_websocket_endpoint("  ws://example.com/rpc?token=one  ").unwrap();

    assert_eq!(endpoint.transport, TransportKind::Tcp);
    assert_eq!(endpoint.address, "example.com:80");
    assert_eq!(endpoint.server_url, "ws://example.com/rpc?token=one");
    assert_eq!(endpoint.origin_url, "http://example.com");
    assert!(!endpoint.use_tls);
}

#[test]
fn websocket_endpoint_preserves_explicit_tls_port_and_origin() {
    let endpoint = parse_websocket_endpoint("wss://example.com:8443/rpc").unwrap();

    assert_eq!(endpoint.transport, TransportKind::Tcp);
    assert_eq!(endpoint.address, "example.com:8443");
    assert_eq!(endpoint.server_url, "wss://example.com:8443/rpc");
    assert_eq!(endpoint.origin_url, "https://example.com:8443");
    assert!(endpoint.use_tls);
}

#[test]
fn websocket_endpoint_handles_ipv6_default_port() {
    let endpoint = parse_websocket_endpoint("ws://[::1]/rpc").unwrap();

    assert_eq!(endpoint.address, "[::1]:80");
    assert_eq!(endpoint.origin_url, "http://[::1]");
}

#[test]
fn websocket_endpoint_rejects_go_error_cases() {
    assert_eq!(
        parse_websocket_endpoint(" ").unwrap_err(),
        EndpointParseError::Required
    );
    assert_eq!(
        parse_websocket_endpoint("http://example.com/rpc").unwrap_err(),
        EndpointParseError::UnsupportedWebSocketScheme("http".to_owned())
    );
    assert_eq!(
        parse_websocket_endpoint("ws:///rpc").unwrap_err(),
        EndpointParseError::WebSocketHostRequired
    );
}

#[test]
fn unix_endpoint_matches_go_url_defaults() {
    let endpoint = new_unix_endpoint(" /tmp/builder.sock ", "rpc").unwrap();

    assert_eq!(endpoint.transport, TransportKind::Unix);
    assert_eq!(endpoint.address, "/tmp/builder.sock");
    assert_eq!(endpoint.server_url, "ws://builder.local/rpc");
    assert_eq!(endpoint.origin_url, "http://builder.local");
    assert!(!endpoint.use_tls);
}

#[test]
fn unix_endpoint_defaults_blank_rpc_path_to_root_and_rejects_blank_socket() {
    let endpoint = new_unix_endpoint("/tmp/builder.sock", " ").unwrap();

    assert_eq!(endpoint.server_url, "ws://builder.local/");
    assert_eq!(
        new_unix_endpoint(" \t ", "/rpc").unwrap_err(),
        EndpointParseError::UnixSocketPathRequired
    );
}
