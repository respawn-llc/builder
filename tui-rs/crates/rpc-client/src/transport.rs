use std::time::Duration;

use crate::wire::Frame;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ConnectionKind {
    Control,
    Dedicated,
    Subscription,
}

pub trait ConnectionFactory {
    type Connection: FrameConnection;

    fn open(&mut self, kind: ConnectionKind) -> Result<Self::Connection, TransportError>;
}

pub trait FrameConnection {
    fn send(&mut self, frame: Frame) -> Result<(), TransportError>;
    fn receive(&mut self) -> Result<Frame, TransportError>;
    fn receive_with_timeout(&mut self, timeout: Duration) -> Result<Frame, TransportError>;
    fn close(&mut self) -> Result<(), TransportError>;
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TransportError {
    Closed,
    ConnectFailed(String),
    HandshakeFailed(String),
    SendFailed(String),
    ReceiveFailed(String),
    Backpressure,
    EncodeFailed(String),
    DecodeFailed(String),
    ResolveFailed(String),
    ResolveTimeout,
    ConnectTimeout,
    HandshakeTimeout,
    ReadTimeout,
    WriteTimeout,
    ConfigurationInvalid(String),
}
