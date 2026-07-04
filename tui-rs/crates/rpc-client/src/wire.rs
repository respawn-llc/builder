use serde::{Deserialize, Serialize};
use serde_json::Value;

pub const JSONRPC_VERSION: &str = "2.0";

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct Request {
    pub jsonrpc: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub id: String,
    pub method: String,
    #[serde(skip_serializing_if = "Option::is_none", default)]
    pub params: Option<Value>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct Response {
    pub jsonrpc: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub id: String,
    #[serde(skip_serializing_if = "Option::is_none", default)]
    pub result: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none", default)]
    pub error: Option<ResponseError>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ResponseError {
    pub code: i32,
    pub message: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct Frame {
    pub jsonrpc: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub id: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub method: String,
    #[serde(skip_serializing_if = "Option::is_none", default)]
    pub params: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none", default)]
    pub result: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none", default)]
    pub error: Option<ResponseError>,
}

impl Frame {
    pub fn from_request(request: Request) -> Self {
        Self {
            jsonrpc: request.jsonrpc,
            id: request.id,
            method: request.method,
            params: request.params,
            result: None,
            error: None,
        }
    }

    pub fn from_response(response: Response) -> Self {
        Self {
            jsonrpc: response.jsonrpc,
            id: response.id,
            method: String::new(),
            params: None,
            result: response.result,
            error: response.error,
        }
    }

    pub fn request(&self) -> Request {
        Request {
            jsonrpc: self.jsonrpc.clone(),
            id: self.id.clone(),
            method: self.method.clone(),
            params: self.params.clone(),
        }
    }

    pub fn response(&self) -> Response {
        Response {
            jsonrpc: self.jsonrpc.clone(),
            id: self.id.clone(),
            result: self.result.clone(),
            error: self.error.clone(),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ErrorCode {
    ParseError,
    InvalidRequest,
    MethodNotFound,
    InvalidParams,
    InternalError,
    StreamGap,
    StreamUnavailable,
    StreamFailed,
    WorkspaceNotRegistered,
    ProjectNotFound,
    ProjectUnavailable,
    SessionAlreadyControlled,
    InvalidControllerLease,
    AuthRequired,
    RuntimeUnavailable,
    PromptNotFound,
    PromptResolved,
    PromptUnsupported,
    RequestCanceled,
    WorkflowTaskNotFound,
    Unknown(i32),
}

impl ErrorCode {
    pub fn code(self) -> i32 {
        match self {
            Self::ParseError => -32700,
            Self::InvalidRequest => -32600,
            Self::MethodNotFound => -32601,
            Self::InvalidParams => -32602,
            Self::InternalError => -32603,
            Self::StreamGap => -32010,
            Self::StreamUnavailable => -32011,
            Self::StreamFailed => -32012,
            Self::WorkspaceNotRegistered => -32013,
            Self::ProjectNotFound => -32014,
            Self::ProjectUnavailable => -32015,
            Self::SessionAlreadyControlled => -32016,
            Self::InvalidControllerLease => -32017,
            Self::AuthRequired => -32018,
            Self::RuntimeUnavailable => -32019,
            Self::PromptNotFound => -32020,
            Self::PromptResolved => -32021,
            Self::PromptUnsupported => -32022,
            Self::RequestCanceled => -32023,
            Self::WorkflowTaskNotFound => -32024,
            Self::Unknown(code) => code,
        }
    }

    pub fn from_code(code: i32) -> Self {
        match code {
            -32700 => Self::ParseError,
            -32600 => Self::InvalidRequest,
            -32601 => Self::MethodNotFound,
            -32602 => Self::InvalidParams,
            -32603 => Self::InternalError,
            -32010 => Self::StreamGap,
            -32011 => Self::StreamUnavailable,
            -32012 => Self::StreamFailed,
            -32013 => Self::WorkspaceNotRegistered,
            -32014 => Self::ProjectNotFound,
            -32015 => Self::ProjectUnavailable,
            -32016 => Self::SessionAlreadyControlled,
            -32017 => Self::InvalidControllerLease,
            -32018 => Self::AuthRequired,
            -32019 => Self::RuntimeUnavailable,
            -32020 => Self::PromptNotFound,
            -32021 => Self::PromptResolved,
            -32022 => Self::PromptUnsupported,
            -32023 => Self::RequestCanceled,
            -32024 => Self::WorkflowTaskNotFound,
            _ => Self::Unknown(code),
        }
    }
}
