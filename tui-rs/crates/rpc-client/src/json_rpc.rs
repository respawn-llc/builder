use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::Value;
use std::time::Duration;

pub use crate::error::RpcError;
use crate::error::response_error;
use crate::transport::FrameConnection;
use crate::wire::{Frame, JSONRPC_VERSION, Request, Response};

pub struct JsonRpcConnection<C> {
    connection: C,
    next_request_id: u64,
    pending: std::collections::BTreeSet<String>,
    canceled: std::collections::BTreeSet<String>,
    failed: std::collections::BTreeMap<String, RpcError>,
    ready: std::collections::BTreeMap<String, Response>,
    terminal_error: Option<RpcError>,
    closed: bool,
}

impl<C> JsonRpcConnection<C> {
    pub fn new(connection: C) -> Self {
        Self {
            connection,
            next_request_id: 0,
            pending: std::collections::BTreeSet::new(),
            canceled: std::collections::BTreeSet::new(),
            failed: std::collections::BTreeMap::new(),
            ready: std::collections::BTreeMap::new(),
            terminal_error: None,
            closed: false,
        }
    }

    pub fn into_connection(self) -> C {
        self.connection
    }

    pub fn pending_count(&self) -> usize {
        self.pending.len()
    }

    pub fn ready_count(&self) -> usize {
        self.ready.len()
    }
}

impl<C: FrameConnection> JsonRpcConnection<C> {
    pub fn call<Req, Resp>(&mut self, method: &str, request: &Req) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        let pending = self.start_call(method, request)?;
        let result = self.receive_pending(&pending)?;
        serde_json::from_value(result).map_err(|error| RpcError::Decode(error.to_string()))
    }

    pub fn call_with_timeout<Req, Resp>(
        &mut self,
        method: &str,
        request: &Req,
        timeout: Duration,
    ) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        let pending = self.start_call(method, request)?;
        let result = match self.receive_pending_with_timeout(&pending, timeout) {
            Ok(result) => result,
            Err(error) => {
                self.clear_pending_after_timed_call(&pending, &error);
                return Err(error);
            }
        };
        serde_json::from_value(result).map_err(|error| RpcError::Decode(error.to_string()))
    }

    pub fn start_call<Req>(&mut self, method: &str, request: Req) -> Result<PendingCall, RpcError>
    where
        Req: Serialize,
    {
        self.next_request_id += 1;
        let id = format!("rpc-{}", self.next_request_id);
        self.start_fixed_call(&id, method, &request)
    }

    pub fn receive_pending(&mut self, pending: &PendingCall) -> Result<Value, RpcError> {
        self.receive_pending_with(pending, ReceiveMode::Unbounded)
    }

    pub fn receive_pending_with_timeout(
        &mut self,
        pending: &PendingCall,
        timeout: Duration,
    ) -> Result<Value, RpcError> {
        self.receive_pending_with(pending, ReceiveMode::Timeout(timeout))
    }

    fn receive_pending_with(
        &mut self,
        pending: &PendingCall,
        mode: ReceiveMode,
    ) -> Result<Value, RpcError> {
        if self.canceled.remove(&pending.id) {
            return Err(RpcError::RequestCanceledLocally);
        }
        if let Some(error) = self.failed.remove(&pending.id) {
            return Err(error);
        }
        if !self.pending.contains(&pending.id) {
            return Err(RpcError::UnknownPendingRequest);
        }
        loop {
            if let Some(response) = self.take_ready_response(&pending.id) {
                return self.complete_pending(pending, response);
            }
            let frame = match mode.receive(&mut self.connection) {
                Ok(frame) => frame,
                Err(TransportReadError::Timeout(error)) => return Err(RpcError::Transport(error)),
                Err(error) => {
                    let rpc_error = RpcError::Transport(error.into_transport_error());
                    self.fail_pending(rpc_error.clone());
                    return Err(rpc_error);
                }
            };
            if frame.id.trim().is_empty() {
                continue;
            }
            if frame.id == pending.id {
                return self.complete_pending(pending, frame.response());
            }
            if self.pending.contains(&frame.id) && !self.ready.contains_key(&frame.id) {
                self.ready.insert(frame.id.clone(), frame.response());
            }
        }
    }

    pub fn call_fixed<Req, Resp>(
        &mut self,
        id: &str,
        method: &str,
        request: &Req,
    ) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        let pending = self.start_fixed_call(id, method, request)?;
        let result = self.receive_pending(&pending)?;
        serde_json::from_value(result).map_err(|error| RpcError::Decode(error.to_string()))
    }

    pub fn call_fixed_with_timeout<Req, Resp>(
        &mut self,
        id: &str,
        method: &str,
        request: &Req,
        timeout: Duration,
    ) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        let pending = self.start_fixed_call(id, method, request)?;
        let result = match self.receive_pending_with_timeout(&pending, timeout) {
            Ok(result) => result,
            Err(error) => {
                self.clear_pending_after_timed_call(&pending, &error);
                return Err(error);
            }
        };
        serde_json::from_value(result).map_err(|error| RpcError::Decode(error.to_string()))
    }

    pub fn close(&mut self) -> Result<(), RpcError> {
        if self.closed {
            return Ok(());
        }
        self.connection.close().map_err(RpcError::Transport)?;
        self.closed = true;
        if self.terminal_error.is_none() {
            self.fail_pending(RpcError::Closed);
        }
        Ok(())
    }

    pub fn cancel_pending(
        &mut self,
        pending: &PendingCall,
        cancellation: CallCancellation,
    ) -> Result<(), RpcError> {
        match cancellation {
            CallCancellation::Local => {
                if self.pending.remove(&pending.id) {
                    self.ready.remove(&pending.id);
                    self.canceled.insert(pending.id.clone());
                    Ok(())
                } else if self.canceled.contains(&pending.id) {
                    Ok(())
                } else {
                    Err(RpcError::UnknownPendingRequest)
                }
            }
        }
    }

    fn start_fixed_call<Req>(
        &mut self,
        id: &str,
        method: &str,
        request: &Req,
    ) -> Result<PendingCall, RpcError>
    where
        Req: Serialize,
    {
        if let Some(error) = &self.terminal_error {
            return Err(error.clone());
        }
        let params =
            serde_json::to_value(request).map_err(|error| RpcError::Encode(error.to_string()))?;
        let id = id.to_owned();
        self.connection
            .send(Frame::from_request(Request {
                jsonrpc: JSONRPC_VERSION.to_owned(),
                id: id.clone(),
                method: method.to_owned(),
                params: Some(params),
            }))
            .map_err(RpcError::Transport)?;
        self.pending.insert(id.clone());
        Ok(PendingCall { id })
    }

    fn take_ready_response(&mut self, id: &str) -> Option<Response> {
        self.ready.remove(id)
    }

    fn complete_pending(
        &mut self,
        pending: &PendingCall,
        response: Response,
    ) -> Result<Value, RpcError> {
        self.pending.remove(&pending.id);
        if let Some(error) = response.error {
            return Err(response_error(error));
        }
        response.result.ok_or(RpcError::MissingResult)
    }

    fn fail_pending(&mut self, error: RpcError) {
        self.terminal_error = Some(error.clone());
        let pending_ids = std::mem::take(&mut self.pending);
        for id in pending_ids {
            self.failed.insert(id, error.clone());
        }
    }

    fn clear_pending_after_timed_call(&mut self, pending: &PendingCall, error: &RpcError) {
        if matches!(
            error,
            RpcError::Transport(crate::transport::TransportError::ReadTimeout)
        ) {
            self.pending.remove(&pending.id);
            self.ready.remove(&pending.id);
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ReceiveMode {
    Unbounded,
    Timeout(Duration),
}

impl ReceiveMode {
    fn receive<C: FrameConnection>(self, connection: &mut C) -> Result<Frame, TransportReadError> {
        match self {
            Self::Unbounded => connection.receive().map_err(TransportReadError::Terminal),
            Self::Timeout(timeout) => connection
                .receive_with_timeout(timeout)
                .map_err(TransportReadError::from_timed_receive_error),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum TransportReadError {
    Timeout(crate::transport::TransportError),
    Terminal(crate::transport::TransportError),
}

impl TransportReadError {
    fn from_timed_receive_error(error: crate::transport::TransportError) -> Self {
        match error {
            crate::transport::TransportError::ReadTimeout => Self::Timeout(error),
            error => Self::Terminal(error),
        }
    }

    fn into_transport_error(self) -> crate::transport::TransportError {
        match self {
            Self::Timeout(error) | Self::Terminal(error) => error,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PendingCall {
    id: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CallCancellation {
    Local,
}

impl PendingCall {
    pub fn id(&self) -> &str {
        &self.id
    }
}
