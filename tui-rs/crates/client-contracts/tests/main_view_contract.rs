use client_contracts::session::SessionMainViewResponse;
use serde_json::json;

#[test]
fn session_main_view_decodes_without_legacy_update_status() {
    let response: SessionMainViewResponse = serde_json::from_value(json!({
        "MainView": {
            "Status": {
                "ReviewerFrequency": "",
                "ReviewerEnabled": false,
                "AutoCompactionEnabled": false,
                "QuestionsEnabled": false,
                "FastModeAvailable": false,
                "FastModeEnabled": false,
                "ConversationFreshness": 0,
                "PreviousSessionID": null,
                "ParentAgentSessionID": null,
                "NavigationTargetSessionID": null,
                "LastCommittedAssistantFinalAnswer": "",
                "ThinkingLevel": "",
                "CompactionMode": "",
                "ContextUsage": {
                    "UsedTokens": 0,
                    "WindowTokens": 0,
                    "CacheHitPercent": 0,
                    "HasCacheHitPercentage": false
                },
                "CompactionCount": 0,
                "Goal": null
            },
            "Session": {
                "SessionID": "",
                "SessionName": "",
                "ConversationFreshness": 0,
                "ExecutionTarget": {
                    "WorkspaceID": "",
                    "WorkspaceName": "",
                    "WorkspaceRoot": "",
                    "WorkspaceAvailability": "",
                    "WorktreeID": "",
                    "WorktreeName": "",
                    "WorktreeRoot": "",
                    "WorktreeAvailability": "",
                    "CwdRelpath": "",
                    "EffectiveWorkdir": ""
                }
            },
            "ActiveRun": null
        }
    }))
    .expect("current session main-view payload must decode");

    assert!(!response.main_view.status.questions_enabled);
}
