use url::Url;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TransportKind {
    Tcp,
    Unix,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Endpoint {
    pub transport: TransportKind,
    pub address: String,
    pub server_url: String,
    pub origin_url: String,
    pub use_tls: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum EndpointParseError {
    Required,
    InvalidUrl(String),
    UnsupportedWebSocketScheme(String),
    WebSocketHostRequired,
    UnixSocketPathRequired,
}

pub fn parse_websocket_endpoint(raw: &str) -> Result<Endpoint, EndpointParseError> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return Err(EndpointParseError::Required);
    }
    let parsed =
        Url::parse(trimmed).map_err(|error| EndpointParseError::InvalidUrl(error.to_string()))?;
    let scheme = parsed.scheme();
    if scheme != "ws" && scheme != "wss" {
        return Err(EndpointParseError::UnsupportedWebSocketScheme(
            scheme.to_owned(),
        ));
    }
    if raw_websocket_authority_missing(trimmed) {
        return Err(EndpointParseError::WebSocketHostRequired);
    }
    let host = parsed
        .host_str()
        .map(str::trim)
        .filter(|host| !host.is_empty())
        .ok_or(EndpointParseError::WebSocketHostRequired)?;
    let port = parsed
        .port()
        .unwrap_or(if scheme == "wss" { 443 } else { 80 });
    let origin_scheme = if scheme == "wss" { "https" } else { "http" };

    Ok(Endpoint {
        transport: TransportKind::Tcp,
        address: join_host_port(host, port),
        server_url: trimmed.to_owned(),
        origin_url: format!(
            "{origin_scheme}://{}",
            host_with_optional_port(&parsed, host)
        ),
        use_tls: scheme == "wss",
    })
}

pub fn new_unix_endpoint(
    socket_path: &str,
    rpc_path: &str,
) -> Result<Endpoint, EndpointParseError> {
    let trimmed_socket_path = socket_path.trim();
    if trimmed_socket_path.is_empty() {
        return Err(EndpointParseError::UnixSocketPathRequired);
    }
    let trimmed_path = rpc_path.trim();
    let normalized_path = if trimmed_path.is_empty() {
        "/".to_owned()
    } else if trimmed_path.starts_with('/') {
        trimmed_path.to_owned()
    } else {
        format!("/{trimmed_path}")
    };

    Ok(Endpoint {
        transport: TransportKind::Unix,
        address: trimmed_socket_path.to_owned(),
        server_url: format!("ws://builder.local{normalized_path}"),
        origin_url: "http://builder.local".to_owned(),
        use_tls: false,
    })
}

fn join_host_port(host: &str, port: u16) -> String {
    if host.contains(':') {
        format!("{}:{port}", bracket_host(host))
    } else {
        format!("{host}:{port}")
    }
}

fn host_with_optional_port(parsed: &Url, host: &str) -> String {
    let formatted_host = if host.contains(':') {
        bracket_host(host)
    } else {
        host.to_owned()
    };
    match parsed.port() {
        Some(port) => format!("{formatted_host}:{port}"),
        None => formatted_host,
    }
}

fn bracket_host(host: &str) -> String {
    if host.starts_with('[') && host.ends_with(']') {
        host.to_owned()
    } else {
        format!("[{host}]")
    }
}

fn raw_websocket_authority_missing(trimmed: &str) -> bool {
    let rest = trimmed
        .strip_prefix("ws://")
        .or_else(|| trimmed.strip_prefix("wss://"));
    match rest.and_then(|value| value.chars().next()) {
        None => true,
        Some('/') | Some('?') | Some('#') => true,
        Some(_) => false,
    }
}
