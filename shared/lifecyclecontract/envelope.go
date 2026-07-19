package lifecyclecontract

import (
	"encoding/json"
	"errors"
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

const (
	ScopeClient Scope = "client"
	ScopeServer Scope = "server"
)

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

type WorkflowTaskID struct {
	value string
}

func ParseWorkflowTaskID(raw string) (WorkflowTaskID, error) {
	if strings.TrimSpace(raw) == "" {
		return WorkflowTaskID{}, errors.New("workflow_task_id is required")
	}
	if strings.TrimSpace(raw) != raw {
		return WorkflowTaskID{}, errors.New("workflow_task_id must not have leading or trailing whitespace")
	}
	return WorkflowTaskID{value: raw}, nil
}

func (id WorkflowTaskID) String() string {
	return id.value
}

func (id WorkflowTaskID) Validate() error {
	_, err := ParseWorkflowTaskID(id.value)
	return err
}

func (id WorkflowTaskID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(id.value)
}

type Context struct {
	SessionID      *runtimeids.SessionID `json:"session_id,omitempty"`
	SessionTitle   *string               `json:"session_title,omitempty"`
	WorkflowTaskID *WorkflowTaskID       `json:"workflow_task_id,omitempty"`
}

type detailKind uint8

const (
	detailKindSessionStart detailKind = iota + 1
	detailKindTaskComplete
	detailKindTaskError
	detailKindInputRequired
	detailKindResourceLimit
)

type Details struct {
	kind          detailKind
	sessionStart  *SessionStartDetails
	taskComplete  *TaskCompleteDetails
	taskError     *TaskErrorDetails
	inputRequired *InputRequiredDetails
	resourceLimit *ResourceLimitDetails
}

type SessionStartDetails struct {
	Kind OpeningKind `json:"kind"`
}

func NewSessionStartDetails(kind OpeningKind) Details {
	return Details{
		kind:         detailKindSessionStart,
		sessionStart: &SessionStartDetails{Kind: kind},
	}
}

type TaskCompleteDetails struct {
	FinalAnswer   string `json:"final_answer"`
	WorkPerformed bool   `json:"work_performed"`
}

func NewTaskCompleteDetails(finalAnswer string, workPerformed bool) Details {
	return Details{
		kind: detailKindTaskComplete,
		taskComplete: &TaskCompleteDetails{
			FinalAnswer:   finalAnswer,
			WorkPerformed: workPerformed,
		},
	}
}

type TaskErrorDetails struct {
	Diagnostic string `json:"diagnostic"`
}

func NewTaskErrorDetails(diagnostic string) Details {
	return Details{
		kind:      detailKindTaskError,
		taskError: &TaskErrorDetails{Diagnostic: diagnostic},
	}
}

type InputRequiredDetails struct {
	Kind    InputKind `json:"kind"`
	Summary string    `json:"summary"`
}

func NewInputRequiredDetails(kind InputKind, summary string) Details {
	return Details{
		kind: detailKindInputRequired,
		inputRequired: &InputRequiredDetails{
			Kind:    kind,
			Summary: summary,
		},
	}
}

type ResourceLimitDetails struct {
	CompactionMode string `json:"compaction_mode"`
}

func NewResourceLimitDetails(compactionMode string) Details {
	return Details{
		kind:          detailKindResourceLimit,
		resourceLimit: &ResourceLimitDetails{CompactionMode: compactionMode},
	}
}

type TruncationField string

const (
	TruncationFieldSessionTitle TruncationField = "context.session_title"
	TruncationFieldFinalAnswer  TruncationField = "details.final_answer"
	TruncationFieldDiagnostic   TruncationField = "details.diagnostic"
	TruncationFieldInputSummary TruncationField = "details.summary"
)

type Truncation struct {
	Fields []TruncationField `json:"fields"`
}

type EnvelopeInput struct {
	Scope      Scope
	Category   Category
	OccurredAt time.Time
	Focused    bool
	Context    Context
	Details    Details
	Truncation *Truncation
}

type Envelope struct {
	scope              Scope
	category           Category
	compatibilityAlias CompatibilityAlias
	occurredAt         time.Time
	focused            bool
	context            Context
	details            Details
	truncation         *Truncation
}

func NewEnvelope(input EnvelopeInput) (Envelope, error) {
	alias, err := compatibilityAlias(input.Category)
	if err != nil {
		return Envelope{}, err
	}
	envelope := Envelope{
		scope:              input.Scope,
		category:           input.Category,
		compatibilityAlias: alias,
		occurredAt:         input.OccurredAt.UTC(),
		focused:            input.Focused,
		context:            copyContext(input.Context),
		details:            input.Details,
		truncation:         copyTruncation(input.Truncation),
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (e Envelope) Validate() error {
	switch e.scope {
	case ScopeClient, ScopeServer:
	default:
		return errors.New("lifecycle scope is invalid")
	}
	if _, err := compatibilityAlias(e.category); err != nil {
		return err
	}
	if e.occurredAt.IsZero() {
		return errors.New("lifecycle occurrence time is required")
	}
	if err := validateContext(e.context); err != nil {
		return err
	}
	if e.category == CategorySessionStart && e.context.SessionID == nil {
		return errors.New("session start context requires session_id")
	}
	if err := e.details.validate(e.category); err != nil {
		return err
	}
	return validateTruncation(e.truncation, e.category, e.context)
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	details, err := e.details.value()
	if err != nil {
		return nil, err
	}
	type wire struct {
		SchemaVersion int                `json:"schema_version"`
		CESPVersion   string             `json:"cesp_version"`
		Scope         Scope              `json:"scope"`
		Category      Category           `json:"category"`
		HookEventName CompatibilityAlias `json:"hook_event_name"`
		OccurredAt    time.Time          `json:"occurred_at"`
		Focused       bool               `json:"focused"`
		Context       Context            `json:"context"`
		Details       any                `json:"details"`
		Truncation    *Truncation        `json:"truncation,omitempty"`
	}
	return json.Marshal(wire{
		SchemaVersion: SchemaVersion,
		CESPVersion:   CESPVersion,
		Scope:         e.scope,
		Category:      e.category,
		HookEventName: e.compatibilityAlias,
		OccurredAt:    e.occurredAt,
		Focused:       e.focused,
		Context:       e.context,
		Details:       details,
		Truncation:    e.truncation,
	})
}

func compatibilityAlias(category Category) (CompatibilityAlias, error) {
	switch category {
	case CategorySessionStart:
		return CompatibilityAliasSessionStart, nil
	case CategoryTaskComplete:
		return CompatibilityAliasStop, nil
	case CategoryTaskError:
		return CompatibilityAliasPostToolUseFailure, nil
	case CategoryInputRequired:
		return CompatibilityAliasPermissionRequest, nil
	case CategoryResourceLimit:
		return CompatibilityAliasPreCompact, nil
	default:
		return "", errors.New("lifecycle category is invalid")
	}
}

func copyContext(context Context) Context {
	copied := Context{}
	if context.SessionID != nil {
		value := *context.SessionID
		copied.SessionID = &value
	}
	if context.SessionTitle != nil {
		value := *context.SessionTitle
		copied.SessionTitle = &value
	}
	if context.WorkflowTaskID != nil {
		value := *context.WorkflowTaskID
		copied.WorkflowTaskID = &value
	}
	return copied
}

func copyTruncation(truncation *Truncation) *Truncation {
	if truncation == nil {
		return nil
	}
	return &Truncation{Fields: append([]TruncationField(nil), truncation.Fields...)}
}

func validateContext(context Context) error {
	if context.SessionID != nil {
		if context.SessionID.IsZero() {
			return errors.New("context session_id cannot be empty")
		}
		if _, err := runtimeids.ParseSessionID(context.SessionID.String()); err != nil {
			return err
		}
	}
	if context.SessionTitle != nil && strings.TrimSpace(*context.SessionTitle) == "" {
		return errors.New("context session_title cannot be blank")
	}
	if context.WorkflowTaskID != nil {
		if err := context.WorkflowTaskID.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (d Details) validate(category Category) error {
	expected := d.category()
	if expected == "" {
		return errors.New("lifecycle details are required")
	}
	if expected != category {
		return fmt.Errorf("lifecycle category %q does not match details category %q", category, expected)
	}
	switch d.kind {
	case detailKindSessionStart:
		if d.sessionStart == nil || d.taskComplete != nil || d.taskError != nil || d.inputRequired != nil || d.resourceLimit != nil {
			return errors.New("session start details cardinality is invalid")
		}
		switch d.sessionStart.Kind {
		case OpeningKindNew, OpeningKindResumed:
			return nil
		default:
			return errors.New("session opening kind is invalid")
		}
	case detailKindTaskComplete:
		if d.taskComplete == nil || d.sessionStart != nil || d.taskError != nil || d.inputRequired != nil || d.resourceLimit != nil {
			return errors.New("task complete details cardinality is invalid")
		}
		if strings.TrimSpace(d.taskComplete.FinalAnswer) == "" {
			return errors.New("task complete final_answer cannot be blank")
		}
		return nil
	case detailKindTaskError:
		if d.taskError == nil || d.sessionStart != nil || d.taskComplete != nil || d.inputRequired != nil || d.resourceLimit != nil {
			return errors.New("task error details cardinality is invalid")
		}
		if strings.TrimSpace(d.taskError.Diagnostic) == "" {
			return errors.New("task error diagnostic cannot be blank")
		}
		return nil
	case detailKindInputRequired:
		if d.inputRequired == nil || d.sessionStart != nil || d.taskComplete != nil || d.taskError != nil || d.resourceLimit != nil {
			return errors.New("input required details cardinality is invalid")
		}
		switch d.inputRequired.Kind {
		case InputKindQuestion, InputKindApproval:
		default:
			return errors.New("input required kind is invalid")
		}
		if strings.TrimSpace(d.inputRequired.Summary) == "" {
			return errors.New("input required summary cannot be blank")
		}
		return nil
	case detailKindResourceLimit:
		if d.resourceLimit == nil || d.sessionStart != nil || d.taskComplete != nil || d.taskError != nil || d.inputRequired != nil {
			return errors.New("resource limit details cardinality is invalid")
		}
		if strings.TrimSpace(d.resourceLimit.CompactionMode) == "" {
			return errors.New("resource limit compaction_mode cannot be blank")
		}
		return nil
	default:
		return errors.New("lifecycle details variant is invalid")
	}
}

func (d Details) category() Category {
	switch d.kind {
	case detailKindSessionStart:
		return CategorySessionStart
	case detailKindTaskComplete:
		return CategoryTaskComplete
	case detailKindTaskError:
		return CategoryTaskError
	case detailKindInputRequired:
		return CategoryInputRequired
	case detailKindResourceLimit:
		return CategoryResourceLimit
	default:
		return ""
	}
}

func (d Details) value() (any, error) {
	switch d.kind {
	case detailKindSessionStart:
		return d.sessionStart, nil
	case detailKindTaskComplete:
		return d.taskComplete, nil
	case detailKindTaskError:
		return d.taskError, nil
	case detailKindInputRequired:
		return d.inputRequired, nil
	case detailKindResourceLimit:
		return d.resourceLimit, nil
	default:
		return nil, errors.New("lifecycle details variant is invalid")
	}
}

func validateTruncation(truncation *Truncation, category Category, context Context) error {
	if truncation == nil {
		return nil
	}
	if len(truncation.Fields) == 0 {
		return errors.New("truncation fields cannot be empty")
	}
	seen := make(map[TruncationField]struct{}, len(truncation.Fields))
	for _, field := range truncation.Fields {
		if _, exists := seen[field]; exists {
			return fmt.Errorf("duplicate truncation field %q", field)
		}
		seen[field] = struct{}{}
		switch field {
		case TruncationFieldSessionTitle:
			if context.SessionTitle == nil {
				return fmt.Errorf("truncation field %q requires context session_title", field)
			}
		case TruncationFieldFinalAnswer:
			if category != CategoryTaskComplete {
				return fmt.Errorf("truncation field %q does not apply to category %q", field, category)
			}
		case TruncationFieldDiagnostic:
			if category != CategoryTaskError {
				return fmt.Errorf("truncation field %q does not apply to category %q", field, category)
			}
		case TruncationFieldInputSummary:
			if category != CategoryInputRequired {
				return fmt.Errorf("truncation field %q does not apply to category %q", field, category)
			}
		default:
			return fmt.Errorf("truncation field %q is invalid", field)
		}
	}
	return nil
}
