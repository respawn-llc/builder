package session

import (
	"time"

	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

// Metadata is the metadata-only session projection. Event-log revision and
// conversation freshness belong to a materialized event log capability.
type Metadata struct {
	SessionID                       string                           `json:"session_id"`
	Category                        *sessioncontract.SessionCategory `json:"category,omitempty"`
	Name                            string                           `json:"name,omitempty"`
	FirstPromptPreview              string                           `json:"first_prompt_preview,omitempty"`
	InputDraft                      string                           `json:"input_draft,omitempty"`
	InputDraftRecoveryBuffers       []InputDraftRecoveryBuffer       `json:"input_draft_recovery_buffers,omitempty"`
	PreviousSessionID               *runtimeids.SessionID            `json:"previous_session_id,omitempty"`
	ParentAgentSessionID            *runtimeids.SessionID            `json:"parent_agent_session_id,omitempty"`
	WorkspaceRoot                   string                           `json:"workspace_root"`
	WorkspaceContainer              string                           `json:"workspace_container"`
	Continuation                    *ContinuationContext             `json:"continuation,omitempty"`
	CreatedAt                       time.Time                        `json:"created_at"`
	UpdatedAt                       time.Time                        `json:"updated_at"`
	ModelRequestCount               int64                            `json:"model_request_count"`
	PromptCacheLineageGeneration    int                              `json:"prompt_cache_lineage_generation,omitempty"`
	HeadlessActive                  bool                             `json:"headless_active,omitempty"`
	CompactionSoonReminderIssued    bool                             `json:"compaction_soon_reminder_issued,omitempty"`
	GeneratedRecoveredWarningIssued bool                             `json:"generated_recovered_warning_issued,omitempty"`
	PendingModelRecovery            *PendingModelRecovery            `json:"pending_model_recovery,omitempty"`
	WorktreeReminder                *WorktreeReminderState           `json:"worktree_reminder,omitempty"`
	UsageState                      *UsageState                      `json:"usage_state,omitempty"`
	Goal                            *GoalState                       `json:"goal,omitempty"`
	WorkflowSession                 *WorkflowSessionState            `json:"workflow_session,omitempty"`
	Locked                          *LockedContract                  `json:"locked,omitempty"`
}

func (s *Store) Metadata() Metadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	return metadataFromMeta(s.meta)
}

func metadataFromMeta(in Meta) Metadata {
	cloned := cloneMeta(in)
	return Metadata{
		SessionID:                       cloned.SessionID,
		Category:                        cloned.Category,
		Name:                            cloned.Name,
		FirstPromptPreview:              cloned.FirstPromptPreview,
		InputDraft:                      cloned.InputDraft,
		InputDraftRecoveryBuffers:       cloned.InputDraftRecoveryBuffers,
		PreviousSessionID:               cloned.PreviousSessionID,
		ParentAgentSessionID:            cloned.ParentAgentSessionID,
		WorkspaceRoot:                   cloned.WorkspaceRoot,
		WorkspaceContainer:              cloned.WorkspaceContainer,
		Continuation:                    cloned.Continuation,
		CreatedAt:                       cloned.CreatedAt,
		UpdatedAt:                       cloned.UpdatedAt,
		ModelRequestCount:               cloned.ModelRequestCount,
		PromptCacheLineageGeneration:    cloned.PromptCacheLineageGeneration,
		HeadlessActive:                  cloned.HeadlessActive,
		CompactionSoonReminderIssued:    cloned.CompactionSoonReminderIssued,
		GeneratedRecoveredWarningIssued: cloned.GeneratedRecoveredWarningIssued,
		PendingModelRecovery:            cloned.PendingModelRecovery,
		WorktreeReminder:                cloned.WorktreeReminder,
		UsageState:                      cloned.UsageState,
		Goal:                            cloned.Goal,
		WorkflowSession:                 cloned.WorkflowSession,
		Locked:                          cloned.Locked,
	}
}
