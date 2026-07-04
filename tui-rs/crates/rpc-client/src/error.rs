use serde::{Deserialize, Serialize};

use crate::transport::TransportError;
use crate::wire::{ErrorCode, ResponseError};

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RpcError {
    Transport(TransportError),
    Closed,
    Protocol(ProtocolError),
    RequestCanceled(ProtocolError),
    RequestCanceledLocally,
    StreamFailed,
    Encode(String),
    Decode(String),
    MissingResult,
    UnknownPendingRequest,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProtocolError {
    pub code: ErrorCode,
    pub raw_code: i32,
    pub message: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct StreamCompleteParams {
    #[serde(default)]
    pub code: i32,
    #[serde(default)]
    pub message: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum StreamCompletion {
    Complete,
    Error(RpcError),
}

pub fn response_error(error: ResponseError) -> RpcError {
    let protocol_error = ProtocolError {
        code: ErrorCode::from_code(error.code),
        raw_code: error.code,
        message: error.message,
    };
    if protocol_error.code == ErrorCode::RequestCanceled {
        RpcError::RequestCanceled(protocol_error)
    } else {
        RpcError::Protocol(protocol_error)
    }
}

pub fn stream_completion(params: StreamCompleteParams) -> StreamCompletion {
    if params.code == 0 && params.message.trim().is_empty() {
        return StreamCompletion::Complete;
    }
    StreamCompletion::Error(response_error(ResponseError {
        code: params.code,
        message: params.message,
    }))
}

impl From<TransportError> for RpcError {
    fn from(error: TransportError) -> Self {
        Self::Transport(error)
    }
}
