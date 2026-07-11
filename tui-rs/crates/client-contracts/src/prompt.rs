use serde::{Deserialize, Serialize};

use crate::clientui::ApprovalDecision;

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AskAnswerRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub ask_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error_message: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub answer: String,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub selected_option_number: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub freeform_answer: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ApprovalAnswerRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub approval_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error_message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub decision: Option<ApprovalDecision>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub commentary: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct PromptActivitySubscribeRequest {
    #[serde(rename = "SessionID", default)]
    pub session_id: String,
    #[serde(rename = "AfterSequence")]
    pub after_sequence: u64,
}

fn is_zero_i32(value: &i32) -> bool {
    *value == 0
}
