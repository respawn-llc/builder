package clientui

import "time"

type AttentionNotificationEventType string

const (
	AttentionNotificationEventPending          AttentionNotificationEventType = "pending"
	AttentionNotificationEventResolved         AttentionNotificationEventType = "resolved"
	AttentionNotificationEventSnapshotComplete AttentionNotificationEventType = "snapshot_complete"
)

type AttentionNotificationSource string

const (
	AttentionNotificationSourceLive     AttentionNotificationSource = "live"
	AttentionNotificationSourceSnapshot AttentionNotificationSource = "snapshot"
)

type AttentionNotificationKind string

const (
	AttentionNotificationKindQuestion AttentionNotificationKind = "question"
	AttentionNotificationKindApproval AttentionNotificationKind = "approval"
)

type AttentionNotificationTargetKind string

const (
	AttentionNotificationTargetTaskDetail    AttentionNotificationTargetKind = "task_detail"
	AttentionNotificationTargetSessionPrompt AttentionNotificationTargetKind = "session_prompt"
)

type AttentionNotificationFocusKind string

const (
	AttentionNotificationFocusQuestion AttentionNotificationFocusKind = "question"
	AttentionNotificationFocusApproval AttentionNotificationFocusKind = "approval"
)

type AttentionNotificationEvent struct {
	Sequence   uint64                         `json:"sequence"`
	Source     AttentionNotificationSource    `json:"source"`
	Type       AttentionNotificationEventType `json:"type"`
	Pending    *AttentionNotification         `json:"pending,omitempty"`
	ID         string                         `json:"id,omitempty"`
	Kind       AttentionNotificationKind      `json:"kind,omitempty"`
	OccurredAt time.Time                      `json:"occurred_at,omitempty"`
	SessionID  string                         `json:"session_id,omitempty"`
}

type AttentionNotification struct {
	ID           string                              `json:"id"`
	Kind         AttentionNotificationKind           `json:"kind"`
	OccurredAt   time.Time                           `json:"occurred_at"`
	Revision     uint64                              `json:"revision"`
	Question     *AttentionNotificationQuestionState `json:"question,omitempty"`
	Target       AttentionNotificationTarget         `json:"target"`
	Presentation AttentionNotificationPresentation   `json:"presentation"`
}

type AttentionNotificationQuestionState struct {
	PreparedAskIDs          []string `json:"prepared_ask_ids"`
	MaterializedAskIDs      []string `json:"materialized_ask_ids"`
	CurrentUnresolvedAskIDs []string `json:"current_unresolved_ask_ids"`
	SkippedAskIDs           []string `json:"skipped_ask_ids"`
	DisplayCount            int      `json:"display_count"`
	MaterializedCount       int      `json:"materialized_count"`
}

type AttentionNotificationTarget struct {
	Kind        AttentionNotificationTargetKind       `json:"kind"`
	ProjectID   string                                `json:"project_id,omitempty"`
	WorkflowID  string                                `json:"workflow_id,omitempty"`
	TaskID      string                                `json:"task_id,omitempty"`
	TaskShortID string                                `json:"task_short_id,omitempty"`
	TaskTitle   string                                `json:"task_title,omitempty"`
	SessionID   string                                `json:"session_id,omitempty"`
	RunID       string                                `json:"run_id,omitempty"`
	Focus       *AttentionNotificationTaskDetailFocus `json:"focus,omitempty"`
}

type AttentionNotificationTaskDetailFocus struct {
	Kind             AttentionNotificationFocusKind `json:"kind"`
	AskIDs           []string                       `json:"ask_ids,omitempty"`
	TaskTransitionID string                         `json:"task_transition_id,omitempty"`
}

type AttentionNotificationPresentation struct {
	Title        string `json:"title"`
	Body         string `json:"body"`
	Preview      string `json:"preview,omitempty"`
	FallbackBody string `json:"fallback_body,omitempty"`
	Count        int    `json:"count"`
	Summary      string `json:"summary,omitempty"`
}
