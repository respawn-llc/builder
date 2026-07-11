use rpc_client::wire::{ErrorCode, Frame, JSONRPC_VERSION, Request, Response, ResponseError};
use serde_json::json;

#[test]
fn json_rpc_request_serializes_go_wire_shape() {
    let request = Request {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: "rpc-1".to_owned(),
        method: "project.list".to_owned(),
        params: Some(json!({"project_id":"project-1"})),
    };

    let value = serde_json::to_value(request).unwrap();

    assert_eq!(
        value,
        json!({
            "jsonrpc": "2.0",
            "id": "rpc-1",
            "method": "project.list",
            "params": {"project_id":"project-1"}
        })
    );
}

#[test]
fn json_rpc_request_omits_empty_id_and_missing_params() {
    let request = Request {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: String::new(),
        method: "session.activity".to_owned(),
        params: None,
    };

    let value = serde_json::to_value(request).unwrap();

    assert_eq!(
        value,
        json!({
            "jsonrpc": "2.0",
            "method": "session.activity"
        })
    );
}

#[test]
fn json_rpc_response_serializes_success_and_error_shapes() {
    let success = Response {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: "rpc-2".to_owned(),
        result: Some(json!({"ok":true})),
        error: None,
    };
    let error = Response {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: "rpc-3".to_owned(),
        result: None,
        error: Some(ResponseError {
            code: ErrorCode::RuntimeUnavailable.code(),
            message: "runtime unavailable".to_owned(),
        }),
    };

    assert_eq!(
        serde_json::to_value(success).unwrap(),
        json!({
            "jsonrpc": "2.0",
            "id": "rpc-2",
            "result": {"ok":true}
        })
    );
    let error_value = serde_json::to_value(error).unwrap();
    assert_eq!(
        error_value,
        json!({
            "jsonrpc": "2.0",
            "id": "rpc-3",
            "error": {
                "code": -32019,
                "message": "runtime unavailable"
            }
        })
    );
    assert!(error_value["error"].get("data").is_none());
}

#[test]
fn frame_round_trips_request_and_response_fields() {
    let request = Request {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: "rpc-4".to_owned(),
        method: "auth.getBootstrapStatus".to_owned(),
        params: Some(json!({})),
    };
    let response = Response {
        jsonrpc: JSONRPC_VERSION.to_owned(),
        id: "rpc-4".to_owned(),
        result: Some(json!({"auth_ready":true})),
        error: None,
    };

    assert_eq!(Frame::from_request(request.clone()).request(), request);
    assert_eq!(Frame::from_response(response.clone()).response(), response);
}

#[test]
fn error_code_classification_covers_builder_protocol_constants() {
    let known = [
        (ErrorCode::ParseError, -32700),
        (ErrorCode::InvalidRequest, -32600),
        (ErrorCode::MethodNotFound, -32601),
        (ErrorCode::InvalidParams, -32602),
        (ErrorCode::InternalError, -32603),
        (ErrorCode::StreamGap, -32010),
        (ErrorCode::StreamUnavailable, -32011),
        (ErrorCode::StreamFailed, -32012),
        (ErrorCode::WorkspaceNotRegistered, -32013),
        (ErrorCode::ProjectNotFound, -32014),
        (ErrorCode::ProjectUnavailable, -32015),
        (ErrorCode::SessionAlreadyControlled, -32016),
        (ErrorCode::InvalidControllerLease, -32017),
        (ErrorCode::AuthRequired, -32018),
        (ErrorCode::RuntimeUnavailable, -32019),
        (ErrorCode::PromptNotFound, -32020),
        (ErrorCode::PromptResolved, -32021),
        (ErrorCode::PromptUnsupported, -32022),
        (ErrorCode::RequestCanceled, -32023),
        (ErrorCode::WorkflowTaskNotFound, -32024),
    ];

    for (error_code, wire_code) in known {
        assert_eq!(error_code.code(), wire_code);
        assert_eq!(ErrorCode::from_code(wire_code), error_code);
    }
    assert_eq!(ErrorCode::from_code(-32099), ErrorCode::Unknown(-32099));
}
