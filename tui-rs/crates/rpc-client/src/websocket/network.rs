use std::net::{IpAddr, Ipv4Addr, SocketAddr, TcpStream};
use std::sync::{Arc, OnceLock};
use std::time::{Duration, Instant};

use hickory_resolver::TokioResolver;
use hickory_resolver::config::{
    ConnectionConfig, LookupIpStrategy, NameServerConfig, ResolverConfig, ResolverOpts,
};
use hickory_resolver::net::runtime::TokioRuntimeProvider;

use crate::endpoint::{Endpoint, TransportKind};
use crate::transport::TransportError;

use super::{TransportStream, WebSocketTransportConfig};

pub(super) const DEFAULT_MAX_RESOLVED_ADDRESSES: usize = 16;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum WebSocketDnsPolicy {
    System,
    NameServers(Vec<SocketAddr>),
}

pub(super) struct WebSocketNetworkRuntime {
    runtime: tokio::runtime::Runtime,
    resolver: TokioResolver,
}

impl WebSocketNetworkRuntime {
    fn resolve(&self, host: &str, timeout: Duration) -> Result<Vec<IpAddr>, TransportError> {
        let lookup = self.runtime.block_on(async {
            tokio::time::timeout(timeout, self.resolver.lookup_ip(host.to_owned())).await
        });
        match lookup {
            Err(_) => Err(TransportError::ResolveTimeout),
            Ok(Err(error)) => Err(TransportError::ResolveFailed(error.to_string())),
            Ok(Ok(addresses)) => Ok(addresses.iter().collect()),
        }
    }
}

pub(super) fn open_stream(
    endpoint: &Endpoint,
    config: &WebSocketTransportConfig,
    network: &OnceLock<Result<Arc<WebSocketNetworkRuntime>, TransportError>>,
) -> Result<TransportStream, TransportError> {
    match endpoint.transport {
        TransportKind::Tcp => open_tcp_stream(&endpoint.address, config, network),
        TransportKind::Unix => open_unix_stream(&endpoint.address),
    }
}

fn open_tcp_stream(
    address: &str,
    config: &WebSocketTransportConfig,
    network: &OnceLock<Result<Arc<WebSocketNetworkRuntime>, TransportError>>,
) -> Result<TransportStream, TransportError> {
    if let Ok(socket_address) = address.parse::<SocketAddr>() {
        return open_tcp_socket(socket_address, config.connect_timeout);
    }
    let (host, port) = parse_host_port(address)?;
    if host.eq_ignore_ascii_case("localhost") {
        return open_tcp_socket(
            SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), port),
            config.connect_timeout,
        );
    }

    let addresses = resolve_socket_addresses(host, port, config, network)?;
    open_first_reachable_tcp_socket(addresses, config.connect_timeout)
}

fn open_tcp_socket(
    socket_address: SocketAddr,
    connect_timeout: Duration,
) -> Result<TransportStream, TransportError> {
    TcpStream::connect_timeout(&socket_address, connect_timeout)
        .map_err(map_connect_error)
        .and_then(|stream| {
            stream
                .set_nodelay(true)
                .map_err(|error| TransportError::ConnectFailed(error.to_string()))?;
            Ok(TransportStream::Tcp(stream))
        })
}

fn map_connect_error(error: std::io::Error) -> TransportError {
    if error.kind() == std::io::ErrorKind::TimedOut {
        TransportError::ConnectTimeout
    } else {
        TransportError::ConnectFailed(error.to_string())
    }
}

fn open_first_reachable_tcp_socket(
    socket_addresses: Vec<SocketAddr>,
    connect_timeout: Duration,
) -> Result<TransportStream, TransportError> {
    let started_at = Instant::now();
    let mut last_error = None;
    for socket_address in socket_addresses {
        let elapsed = started_at.elapsed();
        if elapsed >= connect_timeout {
            return Err(TransportError::ConnectTimeout);
        }
        let remaining = connect_timeout - elapsed;
        match open_tcp_socket(socket_address, remaining) {
            Ok(stream) => return Ok(stream),
            Err(error) => last_error = Some(error),
        }
    }
    match last_error {
        Some(error) => Err(error),
        None => Err(TransportError::ResolveFailed(
            "DNS resolution returned no socket addresses".to_owned(),
        )),
    }
}

#[cfg(unix)]
fn open_unix_stream(address: &str) -> Result<TransportStream, TransportError> {
    std::os::unix::net::UnixStream::connect(address)
        .map(TransportStream::Unix)
        .map_err(|error| TransportError::ConnectFailed(error.to_string()))
}

#[cfg(not(unix))]
fn open_unix_stream(_address: &str) -> Result<TransportStream, TransportError> {
    Err(TransportError::ConnectFailed(
        "unix sockets are not supported on this platform".to_owned(),
    ))
}

fn parse_host_port(address: &str) -> Result<(&str, u16), TransportError> {
    let (host, port) = if let Some(rest) = address.strip_prefix('[') {
        let Some((host, after_host)) = rest.split_once(']') else {
            return Err(TransportError::ConnectFailed(format!(
                "invalid socket address {address}"
            )));
        };
        let Some(port) = after_host.strip_prefix(':') else {
            return Err(TransportError::ConnectFailed(format!(
                "invalid socket address {address}"
            )));
        };
        (host, port)
    } else {
        let Some((host, port)) = address.rsplit_once(':') else {
            return Err(TransportError::ConnectFailed(format!(
                "invalid socket address {address}"
            )));
        };
        (host, port)
    };
    if host.trim().is_empty() {
        return Err(TransportError::ConnectFailed(format!(
            "invalid socket address {address}"
        )));
    }
    let port = port
        .parse::<u16>()
        .map_err(|error| TransportError::ConnectFailed(error.to_string()))?;
    Ok((host, port))
}

fn resolve_socket_addresses(
    host: &str,
    port: u16,
    config: &WebSocketTransportConfig,
    network: &OnceLock<Result<Arc<WebSocketNetworkRuntime>, TransportError>>,
) -> Result<Vec<SocketAddr>, TransportError> {
    let runtime = network_runtime(config, network)?;
    let timeout = min_duration(config.resolve_timeout, config.connect_timeout);
    let mut addresses = runtime.resolve(host, timeout)?;
    addresses.truncate(config.max_resolved_addresses);
    let socket_addresses = addresses
        .into_iter()
        .map(|address| SocketAddr::new(address, port))
        .collect::<Vec<_>>();
    if socket_addresses.is_empty() {
        return Err(TransportError::ResolveFailed(format!(
            "no addresses resolved for {host}"
        )));
    }
    Ok(socket_addresses)
}

fn network_runtime(
    config: &WebSocketTransportConfig,
    network: &OnceLock<Result<Arc<WebSocketNetworkRuntime>, TransportError>>,
) -> Result<Arc<WebSocketNetworkRuntime>, TransportError> {
    network
        .get_or_init(|| build_network_runtime(config).map(Arc::new))
        .clone()
}

fn build_network_runtime(
    config: &WebSocketTransportConfig,
) -> Result<WebSocketNetworkRuntime, TransportError> {
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_io()
        .enable_time()
        .build()
        .map_err(|error| TransportError::ConfigurationInvalid(error.to_string()))?;
    let resolver = build_resolver(config)?;
    Ok(WebSocketNetworkRuntime { runtime, resolver })
}

fn build_resolver(config: &WebSocketTransportConfig) -> Result<TokioResolver, TransportError> {
    let mut builder = match &config.dns {
        WebSocketDnsPolicy::System => TokioResolver::builder_tokio()
            .map_err(|error| TransportError::ConfigurationInvalid(error.to_string()))?,
        WebSocketDnsPolicy::NameServers(name_servers) => TokioResolver::builder_with_config(
            resolver_config_for_name_servers(name_servers),
            TokioRuntimeProvider::default(),
        ),
    };
    *builder.options_mut() = resolver_options(config);
    builder
        .build()
        .map_err(|error| TransportError::ConfigurationInvalid(error.to_string()))
}

fn resolver_config_for_name_servers(name_servers: &[SocketAddr]) -> ResolverConfig {
    let configs = name_servers
        .iter()
        .map(|address| {
            let mut server = NameServerConfig::udp(address.ip());
            server.connections = vec![udp_connection_config(address.port())];
            server
        })
        .collect::<Vec<_>>();
    ResolverConfig::from_parts(None, Vec::new(), configs)
}

fn udp_connection_config(port: u16) -> ConnectionConfig {
    let mut config = ConnectionConfig::udp();
    config.port = port;
    config
}

fn resolver_options(config: &WebSocketTransportConfig) -> ResolverOpts {
    let mut options = ResolverOpts::default();
    options.timeout = min_duration(config.resolve_timeout, config.connect_timeout);
    options.attempts = 1;
    options.ip_strategy = LookupIpStrategy::Ipv4AndIpv6;
    options
}

fn min_duration(left: Duration, right: Duration) -> Duration {
    if left <= right { left } else { right }
}
