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
	AttentionNotificationKindQuestion       AttentionNotificationKind = "question"
	AttentionNotificationKindApproval       AttentionNotificationKind = "approval"
	AttentionNotificationKindInterruptedRun AttentionNotificationKind = "interrupted_run"
)

type AttentionNotificationTargetKind string

const (
	AttentionNotificationTargetWorkflowTask  AttentionNotificationTargetKind = "workflow_task"
	AttentionNotificationTargetSessionPrompt AttentionNotificationTargetKind = "session_prompt"
)

type AttentionNotificationFocusKind string

const (
	AttentionNotificationFocusQuestion       AttentionNotificationFocusKind = "question"
	AttentionNotificationFocusApproval       AttentionNotificationFocusKind = "approval"
	AttentionNotificationFocusInterruptedRun AttentionNotificationFocusKind = "interrupted_run"
)

type AttentionNotificationEvent struct {
	Sequence   uint64                         `json:"sequence"`
	Source     AttentionNotificationSource    `json:"source"`
	Type       AttentionNotificationEventType `json:"type"`
	Pending    *AttentionNotification         `json:"pending,omitempty"`
	ID         *AttentionNotificationID       `json:"id,omitempty"`
	Kind       AttentionNotificationKind      `json:"kind,omitempty"`
	OccurredAt *time.Time                     `json:"occurred_at,omitempty"`
	SessionID  string                         `json:"session_id,omitempty"`
}

type AttentionNotificationID struct {
	Kind AttentionNotificationKind `json:"kind"`
	UUID string                    `json:"uuid"`
}

type AttentionNotification struct {
	ID             AttentionNotificationID                   `json:"id"`
	Kind           AttentionNotificationKind                 `json:"kind"`
	OccurredAt     time.Time                                 `json:"occurred_at"`
	Revision       uint64                                    `json:"revision"`
	Question       *AttentionNotificationQuestionState       `json:"question,omitempty"`
	Approval       *AttentionNotificationApprovalState       `json:"approval,omitempty"`
	InterruptedRun *AttentionNotificationInterruptedRunState `json:"interrupted_run,omitempty"`
	Target         AttentionNotificationTarget               `json:"target"`
}

type AttentionNotificationQuestionState struct {
	PreparedAskIDs          []string `json:"prepared_ask_ids"`
	MaterializedAskIDs      []string `json:"materialized_ask_ids"`
	CurrentUnresolvedAskIDs []string `json:"current_unresolved_ask_ids"`
	SkippedAskIDs           []string `json:"skipped_ask_ids"`
	Preview                 string   `json:"preview,omitempty"`
	DisplayCount            int      `json:"display_count"`
	MaterializedCount       int      `json:"materialized_count"`
}

type AttentionNotificationApprovalState struct {
	TaskTransitionID string `json:"task_transition_id"`
	Message          string `json:"message,omitempty"`
}

type AttentionNotificationInterruptedRunState struct {
	RunID      string `json:"run_id"`
	Message    string `json:"message,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DetailJSON string `json:"detail_json,omitempty"`
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
	RunID            string                         `json:"run_id,omitempty"`
}
