use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSubmitUserTurnRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub text: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub prompt_history_recorded: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSubmitUserTurnResponse {
    pub message: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub compacted: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSubmitUserShellCommandRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub command: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeCompactContextRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub args: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeInterruptRequest {
    pub client_request_id: String,
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeQueueUserMessageRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub text: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeQueueUserMessageResponse {
    pub queue_item_id: String,
    pub text: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeHasQueuedUserWorkRequest {
    pub session_id: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeHasQueuedUserWorkResponse {
    pub has_queued_user_work: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSubmitQueuedUserMessagesRequest {
    pub client_request_id: String,
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSubmitQueuedUserMessagesResponse {
    pub message: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeDiscardQueuedUserMessageRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub queue_item_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeDiscardQueuedUserMessageResponse {
    pub discarded: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetSessionNameRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub name: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetThinkingLevelRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub level: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetFastModeEnabledRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub enabled: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetFastModeEnabledResponse {
    pub changed: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetReviewerEnabledRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub enabled: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetReviewerEnabledResponse {
    pub changed: bool,
    pub mode: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetAutoCompactionEnabledRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub enabled: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetAutoCompactionEnabledResponse {
    pub changed: bool,
    pub enabled: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetQuestionsEnabledRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub enabled: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSetQuestionsEnabledResponse {
    pub changed: bool,
    pub enabled: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeRecordPromptHistoryRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub text: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeGoal {
    pub id: String,
    pub objective: String,
    pub status: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub suspended: bool,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeGoalShowRequest {
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeGoalShowResponse {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub goal: Option<RuntimeGoal>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeGoalSetRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub objective: String,
    pub actor: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeGoalStatusRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub actor: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeGoalClearRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub actor: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeAppendCommittedEntryRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub role: String,
    pub text: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub visibility: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub notice_id: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeEmptyResponse {}

fn is_false(value: &bool) -> bool {
    !*value
}
