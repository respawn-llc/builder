use std::marker::PhantomData;
use std::time::Duration;

use serde::de::DeserializeOwned;
use serde_json::Value;

use crate::error::{RpcError, StreamCompleteParams, StreamCompletion, stream_completion};
use crate::transport::FrameConnection;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct SubscriptionRoute {
    pub request_id: &'static str,
    pub method: &'static str,
    pub event_method: &'static str,
    pub complete_method: &'static str,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum StreamItem {
    Event(Value),
    Complete,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TypedStreamItem<T> {
    Event(T),
    Complete,
}

pub struct RawSubscription<C> {
    connection: C,
    route: SubscriptionRoute,
    stream_id: String,
    complete: bool,
    closed: bool,
    item_read_timeout: Option<Duration>,
}

pub struct TypedSubscription<C, T> {
    raw: RawSubscription<C>,
    _event: PhantomData<T>,
}

impl<C, T> TypedSubscription<C, T> {
    pub fn new(raw: RawSubscription<C>) -> Self {
        Self {
            raw,
            _event: PhantomData,
        }
    }

    pub fn into_connection(self) -> C {
        self.raw.into_connection()
    }

    pub fn stream_id(&self) -> &str {
        self.raw.stream_id()
    }
}

impl<C: FrameConnection, T: DeserializeOwned> TypedSubscription<C, T> {
    pub fn next_item(&mut self) -> Result<TypedStreamItem<T>, RpcError> {
        match self.raw.next_item()? {
            StreamItem::Event(value) => serde_json::from_value(value)
                .map(TypedStreamItem::Event)
                .map_err(|error| RpcError::Decode(error.to_string())),
            StreamItem::Complete => Ok(TypedStreamItem::Complete),
        }
    }

    pub fn cancel_next(
        &mut self,
        cancellation: NextCancellation,
    ) -> Result<TypedStreamItem<T>, RpcError> {
        match self.raw.cancel_next(cancellation)? {
            StreamItem::Event(value) => serde_json::from_value(value)
                .map(TypedStreamItem::Event)
                .map_err(|error| RpcError::Decode(error.to_string())),
            StreamItem::Complete => Ok(TypedStreamItem::Complete),
        }
    }

    pub fn close(&mut self) -> Result<(), RpcError> {
        self.raw.close()
    }
}

impl<C> RawSubscription<C> {
    pub fn new(connection: C, route: SubscriptionRoute, stream_id: String) -> Self {
        Self {
            connection,
            route,
            stream_id,
            complete: false,
            closed: false,
            item_read_timeout: None,
        }
    }

    pub fn with_item_read_timeout(mut self, timeout: Option<Duration>) -> Self {
        self.item_read_timeout = timeout;
        self
    }

    pub fn into_connection(self) -> C {
        self.connection
    }

    pub fn stream_id(&self) -> &str {
        &self.stream_id
    }
}

impl<C: FrameConnection> RawSubscription<C> {
    pub fn next_item(&mut self) -> Result<StreamItem, RpcError> {
        if self.closed {
            return Err(RpcError::Closed);
        }
        if self.complete {
            return Ok(StreamItem::Complete);
        }
        loop {
            let frame = match self.item_read_timeout {
                Some(timeout) => self.connection.receive_with_timeout(timeout),
                None => self.connection.receive(),
            }
            .map_err(RpcError::Transport)?;
            if frame.method == self.route.event_method {
                return Ok(StreamItem::Event(frame.params.unwrap_or(Value::Null)));
            }
            if frame.method == self.route.complete_method {
                let params = serde_json::from_value::<StreamCompleteParams>(
                    frame.params.ok_or(RpcError::StreamFailed)?,
                )
                .map_err(|_| RpcError::StreamFailed)?;
                match stream_completion(params) {
                    StreamCompletion::Complete => {
                        self.complete = true;
                        return Ok(StreamItem::Complete);
                    }
                    StreamCompletion::Error(error) => return Err(error),
                }
            }
            if frame.method.trim().is_empty() {
                continue;
            }
            return Err(RpcError::StreamFailed);
        }
    }

    pub fn cancel_next(&mut self, cancellation: NextCancellation) -> Result<StreamItem, RpcError> {
        match cancellation {
            NextCancellation::Local => Err(RpcError::RequestCanceledLocally),
        }
    }

    pub fn close(&mut self) -> Result<(), RpcError> {
        if self.closed {
            return Ok(());
        }
        self.connection.close().map_err(RpcError::Transport)?;
        self.closed = true;
        Ok(())
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NextCancellation {
    Local,
}
