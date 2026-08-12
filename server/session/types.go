package session

import (
	"time"

	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"github.com/google/uuid"
)

type LockedContract struct {
	Model                  string                                  `json:"model"`
	Temperature            float64                                 `json:"temperature"`
	MaxOutputToken         int                                     `json:"max_output_token"`
	SystemPrompt           string                                  `json:"system_prompt"`
	HasSystemPrompt        bool                                    `json:"has_system_prompt,omitempty"`
	ReviewerPrompt         string                                  `json:"reviewer_prompt,omitempty"`
	HasReviewerPrompt      bool                                    `json:"has_reviewer_prompt,omitempty"`
	ContextWindow          int                                     `json:"context_window,omitempty"`
	ContextPercent         int                                     `json:"context_percent,omitempty"`
	EnabledTools           []string                                `json:"enabled_tools,omitempty"`
	HasEnabledTools        bool                                    `json:"has_enabled_tools,omitempty"`
	WebSearchMode          string                                  `json:"web_search_mode,omitempty"`
	ToolPreambles          *bool                                   `json:"tool_preambles,omitempty"`
	WorkflowCompletionMode *sessioncontract.WorkflowCompletionMode `json:"workflow_completion_mode,omitempty"`
	ModelCapabilities      LockedModelCapabilities                 `json:"model_capabilities,omitempty"`
	ProviderContract       LockedProviderCapabilities              `json:"provider_contract,omitempty"`
	LockedAt               time.Time                               `json:"locked_at"`
}

func (c LockedContract) WithPromptFacingSnapshotsStale() LockedContract {
	c.SystemPrompt = ""
	c.HasSystemPrompt = false
	c.ReviewerPrompt = ""
	c.HasReviewerPrompt = false
	return c
}

func (c LockedContract) WithMainPromptSnapshot(snapshot LockedMainPromptSnapshot) LockedContract {
	c.SystemPrompt = snapshot.SystemPrompt
	c.HasSystemPrompt = snapshot.HasSystemPrompt
	c.ToolPreambles = snapshot.ToolPreambles
	if snapshot.ContextWindow > 0 {
		c.ContextWindow = snapshot.ContextWindow
	}
	if snapshot.ContextPercent > 0 {
		c.ContextPercent = snapshot.ContextPercent
	}
	return c
}

func (c LockedContract) WithReviewerPromptSnapshot(snapshot LockedReviewerPromptSnapshot) LockedContract {
	c.ReviewerPrompt = snapshot.ReviewerPrompt
	c.HasReviewerPrompt = snapshot.HasReviewerPrompt
	return c
}

func (c LockedContract) WithRequestShape(fields LockedRequestShapeBackfill) LockedContract {
	c.EnabledTools = append([]string(nil), fields.EnabledTools...)
	c.HasEnabledTools = fields.HasEnabledTools
	c.WebSearchMode = fields.WebSearchMode
	return c
}

func (c LockedContract) WithWorkflowCompletionMode(mode sessioncontract.WorkflowCompletionMode) LockedContract {
	c.WorkflowCompletionMode = &mode
	return c
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

// CommitReceipt reports whether a Store mutation crossed its durable commit
// fence before a returned operational error.
type CommitReceipt struct {
	Committed bool
}

type LockedContractMutationResult struct {
	CommitReceipt
	Locked *LockedContract
}

type LockedModelCapabilities struct {
	SupportsReasoningEffort bool `json:"supports_reasoning_effort,omitempty"`
	SupportsVisionInputs    bool `json:"supports_vision_inputs,omitempty"`
}

type LockedProviderCapabilities struct {
	ProviderID                    string `json:"provider_id,omitempty"`
	SupportsResponsesAPI          bool   `json:"supports_responses_api,omitempty"`
	SupportsResponsesCompact      bool   `json:"supports_responses_compact,omitempty"`
	SupportsPromptCacheKey        bool   `json:"supports_prompt_cache_key,omitempty"`
	HasSupportsPromptCacheKey     bool   `json:"has_supports_prompt_cache_key,omitempty"`
	SupportsNativeWebSearch       bool   `json:"supports_native_web_search,omitempty"`
	SupportsReasoningEncrypted    bool   `json:"supports_reasoning_encrypted,omitempty"`
	SupportsServerSideContextEdit bool   `json:"supports_server_side_context_edit,omitempty"`
	SupportsProviderVerbosity     *bool  `json:"supports_provider_verbosity,omitempty"`
	IsOpenAIFirstParty            bool   `json:"is_openai_first_party,omitempty"`
}

type ContinuationContext struct {
	OpenAIBaseURL *string `json:"openai_base_url"`
	AgentRole     *string `json:"agent_role,omitempty"`
}

// NavigationTargetSessionID returns the authoritative human-navigation target
// for a session, preferring its immediate previous session over agent ancestry.
func NavigationTargetSessionID(meta Meta) *runtimeids.SessionID {
	source := meta.PreviousSessionID
	if source == nil {
		source = meta.ParentAgentSessionID
	}
	if source == nil {
		return nil
	}
	copied := *source
	return &copied
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

type WorktreeContext struct {
	ContextID     *uuid.UUID `json:"context_id,omitempty"`
	Branch        *string    `json:"branch,omitempty"`
	WorktreePath  string     `json:"worktree_path,omitempty"`
	WorkspaceRoot string     `json:"workspace_root,omitempty"`
	EffectiveCwd  string     `json:"effective_cwd,omitempty"`
}

type WorktreeReminderState struct {
	Mode WorktreeReminderMode `json:"mode,omitempty"`
	WorktreeContext
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

type Meta struct {
	SessionID                       string                           `json:"session_id"`
	Category                        *sessioncontract.SessionCategory `json:"category,omitempty"`
	Name                            string                           `json:"name,omitempty"`
	FirstPromptPreview              string                           `json:"first_prompt_preview,omitempty"`
	InputDraft                      string                           `json:"input_draft,omitempty"`
	PreviousSessionID               *runtimeids.SessionID            `json:"previous_session_id,omitempty"`
	ParentAgentSessionID            *runtimeids.SessionID            `json:"parent_agent_session_id,omitempty"`
	WorkspaceRoot                   string                           `json:"workspace_root"`
	WorkspaceContainer              string                           `json:"workspace_container"`
	Continuation                    *ContinuationContext             `json:"continuation,omitempty"`
	ChatSettings                    *ChatSettingsOverrides           `json:"chat_settings,omitempty"`
	CreatedAt                       time.Time                        `json:"created_at"`
	UpdatedAt                       time.Time                        `json:"updated_at"`
	LastSequence                    int64                            `json:"last_sequence"`
	ConversationEstablished         bool                             `json:"conversation_established,omitempty"`
	ModelRequestCount               int64                            `json:"model_request_count"`
	PromptCacheLineageGeneration    int                              `json:"prompt_cache_lineage_generation,omitempty"`
	HeadlessActive                  bool                             `json:"headless_active,omitempty"`
	CompactionSoonReminderIssued    bool                             `json:"compaction_soon_reminder_issued,omitempty"`
	GeneratedRecoveredWarningIssued bool                             `json:"generated_recovered_warning_issued,omitempty"`
	PendingModelRecovery            *PendingModelRecovery            `json:"pending_model_recovery,omitempty"`
	LegacyInFlightStepRecovery      bool                             `json:"-"`
	WorktreeReminder                *WorktreeReminderState           `json:"worktree_reminder,omitempty"`
	UsageState                      *UsageState                      `json:"usage_state,omitempty"`
	Goal                            *GoalState                       `json:"goal,omitempty"`
	Locked                          *LockedContract                  `json:"locked,omitempty"`
}

type PendingModelRecovery struct {
	RecoveryID             string    `json:"recovery_id"`
	StepID                 string    `json:"step_id,omitempty"`
	Reason                 string    `json:"reason"`
	CreatedAt              time.Time `json:"created_at"`
	OutstandingToolCallIDs []string  `json:"outstanding_tool_call_ids,omitempty"`
}
