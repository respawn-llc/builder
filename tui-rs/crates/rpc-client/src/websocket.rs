use std::io::{self, Read, Write};
use std::net::{IpAddr, Shutdown, TcpStream};
use std::sync::{Arc, OnceLock};
use std::time::Duration;

use crate::endpoint::Endpoint;
use crate::transport::{ConnectionFactory, ConnectionKind, FrameConnection, TransportError};
use crate::wire::Frame;
use rustls::RootCertStore;
use rustls_pki_types::{CertificateDer, ServerName};
use tungstenite::client::IntoClientRequest;
use tungstenite::stream::MaybeTlsStream;

mod network;
pub use network::WebSocketDnsPolicy;
use network::{DEFAULT_MAX_RESOLVED_ADDRESSES, WebSocketNetworkRuntime, open_stream};

const DEFAULT_CONNECT_TIMEOUT: Duration = Duration::from_secs(5);
const DEFAULT_HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(5);
const DEFAULT_WRITE_TIMEOUT: Duration = Duration::from_secs(5);
const DEFAULT_READ_BUFFER_SIZE: usize = 16 * 1024;
const DEFAULT_WRITE_BUFFER_SIZE: usize = 16 * 1024;
const DEFAULT_MAX_WRITE_BUFFER_SIZE: usize = 256 * 1024;
const DEFAULT_MAX_MESSAGE_SIZE: usize = 16 * 1024 * 1024;
const DEFAULT_MAX_FRAME_SIZE: usize = 16 * 1024 * 1024;
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum WebSocketTlsPolicy {
    NativeRoots,
    CustomRootCertificates(Vec<CertificateDer<'static>>),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WebSocketTransportConfig {
    pub connect_timeout: Duration,
    pub handshake_timeout: Duration,
    pub write_timeout: Duration,
    pub read_buffer_size: usize,
    pub write_buffer_size: usize,
    pub max_write_buffer_size: usize,
    pub max_message_size: usize,
    pub max_frame_size: usize,
    pub tls: WebSocketTlsPolicy,
    pub dns: WebSocketDnsPolicy,
    pub resolve_timeout: Duration,
    pub max_resolved_addresses: usize,
}

impl Default for WebSocketTransportConfig {
    fn default() -> Self {
        Self {
            connect_timeout: DEFAULT_CONNECT_TIMEOUT,
            handshake_timeout: DEFAULT_HANDSHAKE_TIMEOUT,
            write_timeout: DEFAULT_WRITE_TIMEOUT,
            read_buffer_size: DEFAULT_READ_BUFFER_SIZE,
            write_buffer_size: DEFAULT_WRITE_BUFFER_SIZE,
            max_write_buffer_size: DEFAULT_MAX_WRITE_BUFFER_SIZE,
            max_message_size: DEFAULT_MAX_MESSAGE_SIZE,
            max_frame_size: DEFAULT_MAX_FRAME_SIZE,
            tls: WebSocketTlsPolicy::NativeRoots,
            dns: WebSocketDnsPolicy::System,
            resolve_timeout: DEFAULT_CONNECT_TIMEOUT,
            max_resolved_addresses: DEFAULT_MAX_RESOLVED_ADDRESSES,
        }
    }
}

#[derive(Clone)]
pub struct WebSocketTransport {
    config: WebSocketTransportConfig,
    network: Arc<OnceLock<Result<Arc<WebSocketNetworkRuntime>, TransportError>>>,
}

impl std::fmt::Debug for WebSocketTransport {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("WebSocketTransport")
            .field("config", &self.config)
            .finish_non_exhaustive()
    }
}

impl Default for WebSocketTransport {
    fn default() -> Self {
        Self {
            config: WebSocketTransportConfig::default(),
            network: Arc::new(OnceLock::new()),
        }
    }
}

impl WebSocketTransport {
    pub fn new(config: WebSocketTransportConfig) -> Result<Self, TransportError> {
        validate_config(&config)?;
        Ok(Self {
            config,
            network: Arc::new(OnceLock::new()),
        })
    }

    pub fn connect(&self, endpoint: &Endpoint) -> Result<WebSocketConnection, TransportError> {
        let stream = open_stream(endpoint, &self.config, &self.network)?;
        stream
            .set_read_timeout(Some(self.config.handshake_timeout))
            .map_err(map_handshake_timeout)?;
        stream
            .set_write_timeout(Some(self.config.handshake_timeout))
            .map_err(map_handshake_timeout)?;

        let request = websocket_request(endpoint)?;
        let stream = websocket_stream(endpoint, stream, &self.config.tls)?;
        let socket = match tungstenite::client::client_with_config(
            request,
            stream,
            Some(websocket_config(&self.config)),
        ) {
            Ok((mut socket, _response)) => {
                set_tls_stream_read_timeout(socket.get_mut(), None)
                    .map_err(|error| TransportError::HandshakeFailed(error.to_string()))?;
                set_tls_stream_write_timeout(socket.get_mut(), None)
                    .map_err(|error| TransportError::HandshakeFailed(error.to_string()))?;
                socket
            }
            Err(tungstenite::HandshakeError::Interrupted(mut handshake)) => {
                let _ = shutdown_tls_stream(handshake.get_mut().get_mut());
                return Err(TransportError::HandshakeTimeout);
            }
            Err(tungstenite::HandshakeError::Failure(error)) => {
                return Err(map_handshake_failure(error));
            }
        };

        Ok(WebSocketConnection {
            socket: Some(socket),
            write_timeout: self.config.write_timeout,
        })
    }
}

#[derive(Debug, Clone)]
pub struct EndpointConnectionFactory {
    endpoint: Endpoint,
    transport: WebSocketTransport,
}

impl EndpointConnectionFactory {
    pub fn new(endpoint: Endpoint, transport: WebSocketTransport) -> Self {
        Self {
            endpoint,
            transport,
        }
    }
}

impl ConnectionFactory for EndpointConnectionFactory {
    type Connection = WebSocketConnection;

    fn open(&mut self, _kind: ConnectionKind) -> Result<Self::Connection, TransportError> {
        self.transport.connect(&self.endpoint)
    }
}

pub struct WebSocketConnection {
    socket: Option<tungstenite::WebSocket<WebSocketStream>>,
    write_timeout: Duration,
}

impl FrameConnection for WebSocketConnection {
    fn send(&mut self, frame: Frame) -> Result<(), TransportError> {
        let data = serde_json::to_string(&frame)
            .map_err(|error| TransportError::EncodeFailed(error.to_string()))?;
        let result = match self.socket.as_mut() {
            Some(socket) => {
                let timeout_result =
                    set_tls_stream_write_timeout(socket.get_mut(), Some(self.write_timeout))
                        .map_err(map_write_error);
                if let Err(error) = timeout_result {
                    Err(error)
                } else {
                    socket
                        .send(tungstenite::Message::text(data))
                        .map_err(map_send_error)
                }
            }
            None => return Err(TransportError::Closed),
        };
        if let Err(error) = result {
            self.terminal_shutdown();
            return Err(error);
        }
        Ok(())
    }

    fn receive(&mut self) -> Result<Frame, TransportError> {
        self.receive_frame()
    }

    fn receive_with_timeout(&mut self, timeout: Duration) -> Result<Frame, TransportError> {
        if timeout.is_zero() {
            return Err(TransportError::ConfigurationInvalid(
                "read timeout must be greater than zero".to_owned(),
            ));
        }
        if let Err(error) = self.set_read_timeout(Some(timeout)) {
            self.terminal_shutdown();
            return Err(error);
        }
        let result = self.receive_frame();
        if self.socket.is_some()
            && let Err(error) = self.set_read_timeout(None)
        {
            self.terminal_shutdown();
            return Err(error);
        }
        result
    }

    fn close(&mut self) -> Result<(), TransportError> {
        self.terminal_shutdown();
        Ok(())
    }
}

impl WebSocketConnection {
    fn receive_frame(&mut self) -> Result<Frame, TransportError> {
        loop {
            let message = match self.socket.as_mut() {
                Some(socket) => socket.read().map_err(map_receive_error),
                None => return Err(TransportError::Closed),
            };
            match message {
                Ok(tungstenite::Message::Text(text)) => {
                    return serde_json::from_str(text.as_ref()).map_err(|error| {
                        self.terminal_shutdown();
                        TransportError::DecodeFailed(error.to_string())
                    });
                }
                Ok(tungstenite::Message::Binary(data)) => {
                    return serde_json::from_slice(&data).map_err(|error| {
                        self.terminal_shutdown();
                        TransportError::DecodeFailed(error.to_string())
                    });
                }
                Ok(tungstenite::Message::Ping(_))
                | Ok(tungstenite::Message::Pong(_))
                | Ok(tungstenite::Message::Frame(_)) => {}
                Ok(tungstenite::Message::Close(_)) => {
                    self.terminal_shutdown();
                    return Err(TransportError::Closed);
                }
                Err(error) => {
                    if error != TransportError::ReadTimeout {
                        self.terminal_shutdown();
                    }
                    return Err(error);
                }
            }
        }
    }

    fn set_read_timeout(&mut self, timeout: Option<Duration>) -> Result<(), TransportError> {
        let Some(socket) = self.socket.as_mut() else {
            return Err(TransportError::Closed);
        };
        set_tls_stream_read_timeout(socket.get_mut(), timeout)
            .map_err(|error| TransportError::ReceiveFailed(error.to_string()))
    }

    fn terminal_shutdown(&mut self) {
        if let Some(socket) = self.socket.as_mut() {
            let _ = shutdown_tls_stream(socket.get_mut());
        }
        self.socket = None;
    }
}

type WebSocketStream = MaybeTlsStream<TransportStream>;

fn set_tls_stream_read_timeout(
    stream: &WebSocketStream,
    timeout: Option<Duration>,
) -> io::Result<()> {
    match stream {
        MaybeTlsStream::Plain(stream) => stream.set_read_timeout(timeout),
        MaybeTlsStream::Rustls(stream) => stream.get_ref().set_read_timeout(timeout),
        _ => Err(unsupported_websocket_stream()),
    }
}

fn set_tls_stream_write_timeout(
    stream: &WebSocketStream,
    timeout: Option<Duration>,
) -> io::Result<()> {
    match stream {
        MaybeTlsStream::Plain(stream) => stream.set_write_timeout(timeout),
        MaybeTlsStream::Rustls(stream) => stream.get_ref().set_write_timeout(timeout),
        _ => Err(unsupported_websocket_stream()),
    }
}

fn shutdown_tls_stream(stream: &WebSocketStream) -> io::Result<()> {
    match stream {
        MaybeTlsStream::Plain(stream) => stream.shutdown(),
        MaybeTlsStream::Rustls(stream) => stream.get_ref().shutdown(),
        _ => Err(unsupported_websocket_stream()),
    }
}

fn unsupported_websocket_stream() -> io::Error {
    io::Error::new(
        io::ErrorKind::Unsupported,
        "unsupported websocket transport stream",
    )
}

enum TransportStream {
    Tcp(TcpStream),
    #[cfg(unix)]
    Unix(std::os::unix::net::UnixStream),
}

impl TransportStream {
    fn set_read_timeout(&self, timeout: Option<Duration>) -> io::Result<()> {
        match self {
            Self::Tcp(stream) => stream.set_read_timeout(timeout),
            #[cfg(unix)]
            Self::Unix(stream) => stream.set_read_timeout(timeout),
        }
    }

    fn set_write_timeout(&self, timeout: Option<Duration>) -> io::Result<()> {
        match self {
            Self::Tcp(stream) => stream.set_write_timeout(timeout),
            #[cfg(unix)]
            Self::Unix(stream) => stream.set_write_timeout(timeout),
        }
    }

    fn shutdown(&self) -> io::Result<()> {
        match self {
            Self::Tcp(stream) => stream.shutdown(Shutdown::Both),
            #[cfg(unix)]
            Self::Unix(stream) => stream.shutdown(Shutdown::Both),
        }
    }
}

impl std::fmt::Debug for TransportStream {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Tcp(_) => formatter.write_str("TransportStream::Tcp"),
            #[cfg(unix)]
            Self::Unix(_) => formatter.write_str("TransportStream::Unix"),
        }
    }
}

impl Read for TransportStream {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        match self {
            Self::Tcp(stream) => stream.read(buffer),
            #[cfg(unix)]
            Self::Unix(stream) => stream.read(buffer),
        }
    }
}

impl Write for TransportStream {
    fn write(&mut self, buffer: &[u8]) -> io::Result<usize> {
        match self {
            Self::Tcp(stream) => stream.write(buffer),
            #[cfg(unix)]
            Self::Unix(stream) => stream.write(buffer),
        }
    }

    fn flush(&mut self) -> io::Result<()> {
        match self {
            Self::Tcp(stream) => stream.flush(),
            #[cfg(unix)]
            Self::Unix(stream) => stream.flush(),
        }
    }
}

fn validate_config(config: &WebSocketTransportConfig) -> Result<(), TransportError> {
    if config.max_write_buffer_size <= config.write_buffer_size {
        return Err(TransportError::ConfigurationInvalid(
            "max_write_buffer_size must be greater than write_buffer_size".to_owned(),
        ));
    }
    if config.resolve_timeout.is_zero() {
        return Err(TransportError::ConfigurationInvalid(
            "resolve_timeout must be greater than zero".to_owned(),
        ));
    }
    if config.max_resolved_addresses == 0 {
        return Err(TransportError::ConfigurationInvalid(
            "max_resolved_addresses must be greater than zero".to_owned(),
        ));
    }
    Ok(())
}

fn websocket_config(config: &WebSocketTransportConfig) -> tungstenite::protocol::WebSocketConfig {
    tungstenite::protocol::WebSocketConfig::default()
        .read_buffer_size(config.read_buffer_size)
        .write_buffer_size(config.write_buffer_size)
        .max_write_buffer_size(config.max_write_buffer_size)
        .max_message_size(Some(config.max_message_size))
        .max_frame_size(Some(config.max_frame_size))
}

fn websocket_stream(
    endpoint: &Endpoint,
    stream: TransportStream,
    policy: &WebSocketTlsPolicy,
) -> Result<WebSocketStream, TransportError> {
    if !endpoint.use_tls {
        return Ok(MaybeTlsStream::Plain(stream));
    }
    let connection = rustls::ClientConnection::new(
        Arc::new(tls_client_config(policy)?),
        tls_server_name(endpoint)?,
    )
    .map_err(|error| TransportError::HandshakeFailed(error.to_string()))?;
    Ok(MaybeTlsStream::Rustls(rustls::StreamOwned::new(
        connection, stream,
    )))
}

fn tls_server_name(endpoint: &Endpoint) -> Result<ServerName<'static>, TransportError> {
    let parsed = url::Url::parse(&endpoint.server_url)
        .map_err(|error| TransportError::HandshakeFailed(error.to_string()))?;
    let host = parsed
        .host()
        .ok_or_else(|| TransportError::HandshakeFailed("TLS server name is required".to_owned()))?;
    match host {
        url::Host::Ipv4(address) => Ok(ServerName::from(IpAddr::V4(address))),
        url::Host::Ipv6(address) => Ok(ServerName::from(IpAddr::V6(address))),
        url::Host::Domain(domain) => ServerName::try_from(domain.to_owned())
            .map_err(|_| TransportError::HandshakeFailed("invalid TLS server name".to_owned())),
    }
}

fn tls_client_config(policy: &WebSocketTlsPolicy) -> Result<rustls::ClientConfig, TransportError> {
    let mut roots = RootCertStore::empty();
    match policy {
        WebSocketTlsPolicy::NativeRoots => {
            let rustls_native_certs::CertificateResult { certs, errors, .. } =
                rustls_native_certs::load_native_certs();
            let total_errors = errors.len();
            let total_certs = certs.len();
            let (added, _ignored) = roots.add_parsable_certificates(certs);
            if added == 0 {
                return Err(TransportError::ConfigurationInvalid(format!(
                    "no native root CA certificates loaded; certs={total_certs} errors={total_errors}"
                )));
            }
        }
        WebSocketTlsPolicy::CustomRootCertificates(certificates) => {
            let total_certs = certificates.len();
            let (added, _ignored) = roots.add_parsable_certificates(certificates.iter().cloned());
            if added == 0 {
                return Err(TransportError::ConfigurationInvalid(format!(
                    "no custom root CA certificates loaded; certs={total_certs}"
                )));
            }
        }
    }

    rustls::ClientConfig::builder_with_provider(rustls::crypto::ring::default_provider().into())
        .with_safe_default_protocol_versions()
        .map_err(|error| TransportError::ConfigurationInvalid(error.to_string()))
        .map(|builder| builder.with_root_certificates(roots).with_no_client_auth())
}

fn websocket_request(
    endpoint: &Endpoint,
) -> Result<tungstenite::handshake::client::Request, TransportError> {
    let uri = endpoint
        .server_url
        .parse::<tungstenite::http::Uri>()
        .map_err(|error| TransportError::HandshakeFailed(error.to_string()))?;
    tungstenite::ClientRequestBuilder::new(uri)
        .with_header("Origin", endpoint.origin_url.clone())
        .into_client_request()
        .map_err(|error| TransportError::HandshakeFailed(error.to_string()))
}

fn map_handshake_timeout(error: io::Error) -> TransportError {
    if error.kind() == io::ErrorKind::TimedOut {
        TransportError::HandshakeTimeout
    } else {
        TransportError::HandshakeFailed(error.to_string())
    }
}

fn map_handshake_failure(error: tungstenite::Error) -> TransportError {
    match error {
        tungstenite::Error::Io(error)
            if error.kind() == io::ErrorKind::TimedOut
                || error.kind() == io::ErrorKind::WouldBlock =>
        {
            TransportError::HandshakeTimeout
        }
        other => TransportError::HandshakeFailed(other.to_string()),
    }
}

fn map_write_error(error: io::Error) -> TransportError {
    if error.kind() == io::ErrorKind::TimedOut || error.kind() == io::ErrorKind::WouldBlock {
        TransportError::WriteTimeout
    } else {
        TransportError::SendFailed(error.to_string())
    }
}

fn map_send_error(error: tungstenite::Error) -> TransportError {
    match error {
        tungstenite::Error::WriteBufferFull(_) => TransportError::Backpressure,
        tungstenite::Error::Io(error)
            if error.kind() == io::ErrorKind::TimedOut
                || error.kind() == io::ErrorKind::WouldBlock =>
        {
            TransportError::WriteTimeout
        }
        tungstenite::Error::AlreadyClosed | tungstenite::Error::ConnectionClosed => {
            TransportError::Closed
        }
        other => TransportError::SendFailed(other.to_string()),
    }
}

fn map_receive_error(error: tungstenite::Error) -> TransportError {
    match error {
        tungstenite::Error::ConnectionClosed | tungstenite::Error::AlreadyClosed => {
            TransportError::Closed
        }
        tungstenite::Error::Io(error)
            if error.kind() == io::ErrorKind::TimedOut
                || error.kind() == io::ErrorKind::WouldBlock =>
        {
            TransportError::ReadTimeout
        }
        other => TransportError::ReceiveFailed(other.to_string()),
    }
}
