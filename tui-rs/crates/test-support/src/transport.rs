use std::io;
use std::net::{IpAddr, SocketAddr, UdpSocket};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use std::thread;
use std::time::Duration;

use hickory_proto::op::{Message, MessageType, OpCode};
use hickory_proto::rr::{
    RData, Record, RecordType,
    rdata::{A, AAAA},
};
use rcgen::{CertifiedKey, generate_simple_self_signed};
use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};

pub struct LocalTlsCertificate {
    pub roots: Vec<CertificateDer<'static>>,
    pub server_config: Arc<rustls::ServerConfig>,
}

pub fn local_tls_certificate(
    subject_alt_names: impl IntoIterator<Item = impl Into<String>>,
) -> Result<LocalTlsCertificate, String> {
    let CertifiedKey { cert, signing_key } = generate_simple_self_signed(
        subject_alt_names
            .into_iter()
            .map(Into::into)
            .collect::<Vec<_>>(),
    )
    .map_err(|error| error.to_string())?;
    let certificate = cert.der().clone();
    let private_key = PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(signing_key.serialize_der()));
    let server_config = rustls::ServerConfig::builder_with_provider(
        rustls::crypto::ring::default_provider().into(),
    )
    .with_safe_default_protocol_versions()
    .map_err(|error| error.to_string())?
    .with_no_client_auth()
    .with_single_cert(vec![certificate.clone()], private_key)
    .map_err(|error| error.to_string())?;

    Ok(LocalTlsCertificate {
        roots: vec![certificate],
        server_config: Arc::new(server_config),
    })
}

pub struct LocalDnsServer {
    address: SocketAddr,
    stop: Arc<AtomicBool>,
    handle: Option<thread::JoinHandle<()>>,
}

impl LocalDnsServer {
    pub fn resolve_hostname_to(hostname: &str, address: IpAddr) -> io::Result<Self> {
        let socket = UdpSocket::bind("127.0.0.1:0")?;
        socket.set_read_timeout(Some(Duration::from_millis(100)))?;
        let server_address = socket.local_addr()?;
        let stop = Arc::new(AtomicBool::new(false));
        let stop_for_thread = Arc::clone(&stop);
        let hostname = hostname.trim_end_matches('.').to_ascii_lowercase();
        let handle = thread::spawn(move || {
            let mut buffer = [0_u8; 2048];
            while !stop_for_thread.load(Ordering::SeqCst) {
                let (size, peer) = match socket.recv_from(&mut buffer) {
                    Ok(received) => received,
                    Err(error)
                        if matches!(
                            error.kind(),
                            std::io::ErrorKind::TimedOut | std::io::ErrorKind::WouldBlock
                        ) =>
                    {
                        continue;
                    }
                    Err(_) => break,
                };
                let Ok(request) = Message::from_vec(&buffer[..size]) else {
                    continue;
                };
                let response = dns_response(&request, &hostname, address);
                let Ok(encoded) = response.to_vec() else {
                    continue;
                };
                let _ = socket.send_to(&encoded, peer);
            }
        });
        Ok(Self {
            address: server_address,
            stop,
            handle: Some(handle),
        })
    }

    pub fn address(&self) -> SocketAddr {
        self.address
    }
}

impl Drop for LocalDnsServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
        if let Ok(socket) = UdpSocket::bind("127.0.0.1:0") {
            let _ = socket.send_to(&[0], self.address);
        }
        if let Some(handle) = self.handle.take() {
            let _ = handle.join();
        }
    }
}

fn dns_response(request: &Message, hostname: &str, address: IpAddr) -> Message {
    let mut response = Message::new(request.metadata.id, MessageType::Response, OpCode::Query);
    response.metadata = hickory_proto::op::Metadata::response_from_request(&request.metadata);
    response.metadata.authoritative = true;
    response.metadata.recursion_available = true;
    response.add_queries(request.queries.clone());

    for query in &request.queries {
        let query_name = query
            .name()
            .to_ascii()
            .trim_end_matches('.')
            .to_ascii_lowercase();
        if query_name != hostname {
            continue;
        }
        match (query.query_type(), address) {
            (RecordType::A, IpAddr::V4(address)) => {
                response.add_answer(Record::from_rdata(
                    query.name().clone(),
                    60,
                    RData::A(A(address)),
                ));
            }
            (RecordType::AAAA, IpAddr::V6(address)) => {
                response.add_answer(Record::from_rdata(
                    query.name().clone(),
                    60,
                    RData::AAAA(AAAA(address)),
                ));
            }
            _ => {}
        }
    }

    response
}
