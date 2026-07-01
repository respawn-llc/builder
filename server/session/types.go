package session

import (
	"encoding/json"
	"time"
)

type LockedContract struct {
	Model             string                     `json:"model"`
	Temperature       float64                    `json:"temperature"`
	MaxOutputToken    int                        `json:"max_output_token"`
	SystemPrompt      string                     `json:"system_prompt"`
	HasSystemPrompt   bool                       `json:"has_system_prompt,omitempty"`
	ReviewerPrompt    string                     `json:"reviewer_prompt,omitempty"`
	HasReviewerPrompt bool                       `json:"has_reviewer_prompt,omitempty"`
	ContextWindow     int                        `json:"context_window,omitempty"`
	ContextPercent    int                        `json:"context_percent,omitempty"`
	EnabledTools      []string                   `json:"enabled_tools,omitempty"`
	HasEnabledTools   bool                       `json:"has_enabled_tools,omitempty"`
	WebSearchMode     string                     `json:"web_search_mode,omitempty"`
	ToolPreambles     *bool                      `json:"tool_preambles,omitempty"`
	ModelCapabilities LockedModelCapabilities    `json:"model_capabilities,omitempty"`
	ProviderContract  LockedProviderCapabilities `json:"provider_contract,omitempty"`
	LockedAt          time.Time                  `json:"locked_at"`
}

type LockedMainPromptSnapshot struct {
	SystemPrompt    string
	HasSystemPrompt bool
	ToolPreambles   *bool
	ContextWindow   int
	ContextPercent  int
}

type LockedReviewerPromptSnapshot struct {
	ReviewerPrompt    string
	HasReviewerPrompt bool
}

type LockedRequestShapeBackfill struct {
	EnabledTools    []string
	HasEnabledTools bool
	WebSearchMode   string
}

type LockedContractMutationResult struct {
	Committed bool
	Locked    *LockedContract
}

type LockedModelCapabilities struct {
	SupportsReasoningEffort bool `json:"supports_reasoning_effort,omitempty"`
	SupportsVisionInputs    bool `json:"supports_vision_inputs,omitempty"`
}

type LockedProviderCapabilities struct {
	ProviderID                        string `json:"provider_id,omitempty"`
	SupportsResponsesAPI              bool   `json:"supports_responses_api,omitempty"`
	SupportsResponsesCompact          bool   `json:"supports_responses_compact,omitempty"`
	SupportsRequestInputTokenCount    bool   `json:"supports_request_input_token_count,omitempty"`
	HasSupportsRequestInputTokenCount bool   `json:"has_supports_request_input_token_count,omitempty"`
	SupportsPromptCacheKey            bool   `json:"supports_prompt_cache_key,omitempty"`
	HasSupportsPromptCacheKey         bool   `json:"has_supports_prompt_cache_key,omitempty"`
	SupportsNativeWebSearch           bool   `json:"supports_native_web_search,omitempty"`
	SupportsReasoningEncrypted        bool   `json:"supports_reasoning_encrypted,omitempty"`
	SupportsServerSideContextEdit     bool   `json:"supports_server_side_context_edit,omitempty"`
	IsOpenAIFirstParty                bool   `json:"is_openai_first_party,omitempty"`
}

type ContinuationContext struct {
	OpenAIBaseURL string `json:"openai_base_url,omitempty"`
	AgentRole     string `json:"agent_role,omitempty"`
}

type UsageState struct {
	InputTokens             int  `json:"input_tokens,omitempty"`
	OutputTokens            int  `json:"output_tokens,omitempty"`
	WindowTokens            int  `json:"window_tokens,omitempty"`
	CachedInputTokens       int  `json:"cached_input_tokens,omitempty"`
	HasCachedInputTokens    bool `json:"has_cached_input_tokens,omitempty"`
	EstimatedProviderTokens int  `json:"estimated_provider_tokens,omitempty"`
	TotalInputTokens        int  `json:"total_input_tokens,omitempty"`
	TotalCachedInputTokens  int  `json:"total_cached_input_tokens,omitempty"`
}

type WorktreeReminderMode string

const (
	WorktreeReminderModeEnter WorktreeReminderMode = "enter"
	WorktreeReminderModeExit  WorktreeReminderMode = "exit"
)

type WorktreeReminderState struct {
	Mode                  WorktreeReminderMode `json:"mode,omitempty"`
	Branch                string               `json:"branch,omitempty"`
	WorktreePath          string               `json:"worktree_path,omitempty"`
	WorkspaceRoot         string               `json:"workspace_root,omitempty"`
	EffectiveCwd          string               `json:"effective_cwd,omitempty"`
	HasIssuedInGeneration bool                 `json:"has_issued_in_generation,omitempty"`
	IssuedCompactionCount int                  `json:"issued_compaction_count,omitempty"`
}

type GoalStatus string

const (
	GoalStatusActive   GoalStatus = "active"
	GoalStatusPaused   GoalStatus = "paused"
	GoalStatusComplete GoalStatus = "complete"
)

type GoalActor string

const (
	GoalActorUser   GoalActor = "user"
	GoalActorAgent  GoalActor = "agent"
	GoalActorSystem GoalActor = "system"
)

type GoalState struct {
	ID        string     `json:"id"`
	Objective string     `json:"objective"`
	Status    GoalStatus `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type GoalSetEvent struct {
	Goal           GoalState `json:"goal"`
	Actor          GoalActor `json:"actor"`
	ReplacedGoalID string    `json:"replaced_goal_id,omitempty"`
}

type GoalStatusUpdatedEvent struct {
	Goal           GoalState  `json:"goal"`
	Actor          GoalActor  `json:"actor"`
	PreviousStatus GoalStatus `json:"previous_status"`
}

type GoalClearedEvent struct {
	Goal  GoalState `json:"goal"`
	Actor GoalActor `json:"actor"`
}

type Meta struct {
	SessionID                       string                 `json:"session_id"`
	Name                            string                 `json:"name,omitempty"`
	FirstPromptPreview              string                 `json:"first_prompt_preview,omitempty"`
	InputDraft                      string                 `json:"input_draft,omitempty"`
	ParentSessionID                 string                 `json:"parent_session_id,omitempty"`
	WorkspaceRoot                   string                 `json:"workspace_root"`
	WorkspaceContainer              string                 `json:"workspace_container"`
	Continuation                    *ContinuationContext   `json:"continuation,omitempty"`
	CreatedAt                       time.Time              `json:"created_at"`
	UpdatedAt                       time.Time              `json:"updated_at"`
	LastSequence                    int64                  `json:"last_sequence"`
	ConversationEstablished         bool                   `json:"conversation_established,omitempty"`
	ModelRequestCount               int64                  `json:"model_request_count"`
	InFlightStep                    bool                   `json:"in_flight_step"`
	HeadlessActive                  bool                   `json:"headless_active,omitempty"`
	CompactionSoonReminderIssued    bool                   `json:"compaction_soon_reminder_issued,omitempty"`
	GeneratedRecoveredWarningIssued bool                   `json:"generated_recovered_warning_issued,omitempty"`
	WorktreeReminder                *WorktreeReminderState `json:"worktree_reminder,omitempty"`
	UsageState                      *UsageState            `json:"usage_state,omitempty"`
	Goal                            *GoalState             `json:"goal,omitempty"`
	WorkflowSession                 *WorkflowSessionState  `json:"workflow_session,omitempty"`
	Locked                          *LockedContract        `json:"locked,omitempty"`
	LatestRun                       *RunRecord             `json:"latest_run,omitempty"`
}

type WorkflowSessionState struct {
	RunID      string `json:"run_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty"`
}

type Event struct {
	Seq       int64           `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Kind      string          `json:"kind"`
	StepID    string          `json:"step_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

type Summary struct {
	SessionID          string    `json:"session_id"`
	Name               string    `json:"name,omitempty"`
	FirstPromptPreview string    `json:"first_prompt_preview,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
	Path               string    `json:"path"`
}
