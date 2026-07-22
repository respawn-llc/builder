package lifecyclecontract

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"core/shared/runtimeids"
)

const (
	SchemaVersion = 1
	CESPVersion   = "1.0"
)

type Scope string

const ScopeClient Scope = "client"

type Category string

const (
	CategorySessionStart  Category = "session.start"
	CategoryTaskComplete  Category = "task.complete"
	CategoryTaskError     Category = "task.error"
	CategoryInputRequired Category = "input.required"
	CategoryResourceLimit Category = "resource.limit"
)

type CompatibilityAlias string

const (
	CompatibilityAliasSessionStart       CompatibilityAlias = "SessionStart"
	CompatibilityAliasStop               CompatibilityAlias = "Stop"
	CompatibilityAliasPostToolUseFailure CompatibilityAlias = "PostToolUseFailure"
	CompatibilityAliasPermissionRequest  CompatibilityAlias = "PermissionRequest"
	CompatibilityAliasPreCompact         CompatibilityAlias = "PreCompact"
)

type OpeningKind string

const (
	OpeningKindNew     OpeningKind = "new"
	OpeningKindResumed OpeningKind = "resumed"
)

type InputKind string

const (
	InputKindQuestion InputKind = "question"
	InputKindApproval InputKind = "approval"
)

type WorkflowTaskID string

type Context struct {
	SessionID      *runtimeids.SessionID `json:"session_id,omitempty"`
	SessionTitle   *string               `json:"session_title,omitempty"`
	WorkflowTaskID *WorkflowTaskID       `json:"workflow_task_id,omitempty"`
}

type Event struct {
	SchemaVersion int                `json:"schema_version"`
	CESPVersion   string             `json:"cesp_version"`
	Scope         Scope              `json:"scope"`
	Category      Category           `json:"category"`
	HookEventName CompatibilityAlias `json:"hook_event_name"`
	OccurredAt    time.Time          `json:"occurred_at"`
	Focused       bool               `json:"focused"`
	Context       Context            `json:"context"`
	Details       any                `json:"details"`
}

type SessionStartDetails struct {
	Kind OpeningKind `json:"kind"`
}

type TaskCompleteDetails struct {
	FinalAnswer   string `json:"final_answer"`
	WorkPerformed bool   `json:"work_performed"`
}

type TaskErrorDetails struct {
	Diagnostic string `json:"diagnostic"`
}

type InputRequiredDetails struct {
	Kind    InputKind `json:"kind"`
	Summary string    `json:"summary"`
}

type CompactionStartedDetails struct {
	CompactionMode string `json:"compaction_mode"`
}

func NewSessionStart(occurredAt time.Time, focused bool, context Context, kind OpeningKind) Event {
	return newEvent(
		CategorySessionStart,
		CompatibilityAliasSessionStart,
		occurredAt,
		focused,
		context,
		SessionStartDetails{Kind: kind},
	)
}

func NewTaskComplete(occurredAt time.Time, focused bool, context Context, finalAnswer string, workPerformed bool) Event {
	return newEvent(
		CategoryTaskComplete,
		CompatibilityAliasStop,
		occurredAt,
		focused,
		context,
		TaskCompleteDetails{FinalAnswer: finalAnswer, WorkPerformed: workPerformed},
	)
}

func NewTaskError(occurredAt time.Time, focused bool, context Context, diagnostic string) Event {
	return newEvent(
		CategoryTaskError,
		CompatibilityAliasPostToolUseFailure,
		occurredAt,
		focused,
		context,
		TaskErrorDetails{Diagnostic: diagnostic},
	)
}

func NewInputRequired(occurredAt time.Time, focused bool, context Context, kind InputKind, summary string) Event {
	return newEvent(
		CategoryInputRequired,
		CompatibilityAliasPermissionRequest,
		occurredAt,
		focused,
		context,
		InputRequiredDetails{Kind: kind, Summary: summary},
	)
}

func NewCompactionStarted(occurredAt time.Time, focused bool, context Context, mode string) Event {
	return newEvent(
		CategoryResourceLimit,
		CompatibilityAliasPreCompact,
		occurredAt,
		focused,
		context,
		CompactionStartedDetails{CompactionMode: mode},
	)
}

func Encode(event Event) ([]byte, error) {
	if err := event.Context.Validate(); err != nil {
		return nil, fmt.Errorf("validate lifecycle event context: %w", err)
	}
	return json.Marshal(event)
}

func (c Context) Validate() error {
	if c.SessionID != nil && c.SessionID.IsZero() {
		return fmt.Errorf("present session id is invalid")
	}
	if c.SessionTitle != nil && strings.TrimSpace(*c.SessionTitle) == "" {
		return fmt.Errorf("present session title is blank")
	}
	if c.WorkflowTaskID != nil && strings.TrimSpace(string(*c.WorkflowTaskID)) == "" {
		return fmt.Errorf("present workflow task id is blank")
	}
	return nil
}

func newEvent(
	category Category,
	alias CompatibilityAlias,
	occurredAt time.Time,
	focused bool,
	context Context,
	details any,
) Event {
	return Event{
		SchemaVersion: SchemaVersion,
		CESPVersion:   CESPVersion,
		Scope:         ScopeClient,
		Category:      category,
		HookEventName: alias,
		OccurredAt:    occurredAt.UTC(),
		Focused:       focused,
		Context:       context,
		Details:       details,
	}
}
