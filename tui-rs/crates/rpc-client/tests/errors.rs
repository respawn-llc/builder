use rpc_client::error::{
    ProtocolError, RpcError, StreamCompleteParams, StreamCompletion, response_error,
    stream_completion,
};
use rpc_client::wire::{ErrorCode, ResponseError};

#[test]
fn response_error_mapping_covers_all_go_protocol_codes() {
    let known = [
        ErrorCode::ParseError,
        ErrorCode::InvalidRequest,
        ErrorCode::MethodNotFound,
        ErrorCode::InvalidParams,
        ErrorCode::InternalError,
        ErrorCode::StreamGap,
        ErrorCode::StreamUnavailable,
        ErrorCode::StreamFailed,
        ErrorCode::WorkspaceNotRegistered,
        ErrorCode::ProjectNotFound,
        ErrorCode::ProjectUnavailable,
        ErrorCode::SessionAlreadyControlled,
        ErrorCode::InvalidControllerLease,
        ErrorCode::AuthRequired,
        ErrorCode::RuntimeUnavailable,
        ErrorCode::PromptNotFound,
        ErrorCode::PromptResolved,
        ErrorCode::PromptUnsupported,
        ErrorCode::WorkflowTaskNotFound,
    ];

    for code in known {
        assert_eq!(
            response_error(ResponseError {
                code: code.code(),
                message: "message".to_owned(),
            }),
            RpcError::Protocol(ProtocolError {
                code,
                raw_code: code.code(),
                message: "message".to_owned(),
            })
        );
    }
}

#[test]
fn request_canceled_code_is_typed_separately_from_protocol_failure() {
    assert_eq!(
        response_error(ResponseError {
            code: ErrorCode::RequestCanceled.code(),
            message: "context canceled".to_owned(),
        }),
        RpcError::RequestCanceled(ProtocolError {
            code: ErrorCode::RequestCanceled,
            raw_code: ErrorCode::RequestCanceled.code(),
            message: "context canceled".to_owned(),
        })
    );
}

#[test]
fn unknown_protocol_error_preserves_raw_code_and_message() {
    assert_eq!(
        response_error(ResponseError {
            code: -32099,
            message: "custom".to_owned(),
        }),
        RpcError::Protocol(ProtocolError {
            code: ErrorCode::Unknown(-32099),
            raw_code: -32099,
            message: "custom".to_owned(),
        })
    );
}

#[test]
fn stream_completion_maps_empty_complete_and_error_codes() {
    assert_eq!(
        stream_completion(StreamCompleteParams {
            code: 0,
            message: String::new(),
        }),
        StreamCompletion::Complete
    );
    assert_eq!(
        stream_completion(StreamCompleteParams {
            code: ErrorCode::StreamGap.code(),
            message: "gap".to_owned(),
        }),
        StreamCompletion::Error(RpcError::Protocol(ProtocolError {
            code: ErrorCode::StreamGap,
            raw_code: ErrorCode::StreamGap.code(),
            message: "gap".to_owned(),
        }))
    );
    assert_eq!(
        stream_completion(StreamCompleteParams {
            code: 0,
            message: "complete message".to_owned(),
        }),
        StreamCompletion::Error(RpcError::Protocol(ProtocolError {
            code: ErrorCode::Unknown(0),
            raw_code: 0,
            message: "complete message".to_owned(),
        }))
    );
}
