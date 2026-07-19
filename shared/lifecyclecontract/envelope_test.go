package lifecyclecontract

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"core/shared/runtimeids"
)

func TestSessionStartEnvelopeUsesCESPAndOpenPeonWireContract(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	taskID, err := ParseWorkflowTaskID("task-1")
	if err != nil {
		t.Fatalf("parse workflow task ID: %v", err)
	}
	title := "Lifecycle contract"
	occurredAt := time.Date(2026, time.July, 19, 17, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategorySessionStart,
		OccurredAt: occurredAt,
		Focused:    true,
		Context: Context{
			SessionID:      &sessionID,
			SessionTitle:   &title,
			WorkflowTaskID: &taskID,
		},
		Details: NewSessionStartDetails(OpeningKindNew),
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if got["schema_version"] != float64(1) || got["cesp_version"] != "1.0" {
		t.Fatalf("version fields = schema:%v cesp:%v", got["schema_version"], got["cesp_version"])
	}
	if got["scope"] != "client" || got["category"] != "session.start" || got["hook_event_name"] != "SessionStart" {
		t.Fatalf("event identity = scope:%v category:%v alias:%v", got["scope"], got["category"], got["hook_event_name"])
	}
	if got["occurred_at"] != "2026-07-19T15:30:00Z" || got["focused"] != true {
		t.Fatalf("occurrence = time:%v focused:%v", got["occurred_at"], got["focused"])
	}
	context, ok := got["context"].(map[string]any)
	if !ok {
		t.Fatalf("context = %T, want object", got["context"])
	}
	if context["session_id"] != "session-1" || context["session_title"] != title || context["workflow_task_id"] != "task-1" {
		t.Fatalf("context = %#v", context)
	}
	details, ok := got["details"].(map[string]any)
	if !ok || details["kind"] != "new" {
		t.Fatalf("details = %#v, want new session", got["details"])
	}
	if _, present := got["truncation"]; present {
		t.Fatalf("unexpected truncation metadata: %#v", got["truncation"])
	}
}

func TestLifecycleEnvelopeVariantsDeriveCategoryAliases(t *testing.T) {
	occurredAt := time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		category    Category
		context     Context
		details     Details
		wantAlias   string
		wantDetails map[string]any
	}{
		{
			name:        "session start",
			category:    CategorySessionStart,
			context:     Context{SessionID: sessionIDPtr(t, "session-1")},
			details:     NewSessionStartDetails(OpeningKindResumed),
			wantAlias:   "SessionStart",
			wantDetails: map[string]any{"kind": "resumed"},
		},
		{
			name:        "task complete",
			category:    CategoryTaskComplete,
			details:     NewTaskCompleteDetails("Done.", true),
			wantAlias:   "Stop",
			wantDetails: map[string]any{"final_answer": "Done.", "work_performed": true},
		},
		{
			name:        "task error",
			category:    CategoryTaskError,
			details:     NewTaskErrorDetails("Agent stopped before a final answer."),
			wantAlias:   "PostToolUseFailure",
			wantDetails: map[string]any{"diagnostic": "Agent stopped before a final answer."},
		},
		{
			name:        "input required",
			category:    CategoryInputRequired,
			details:     NewInputRequiredDetails(InputKindApproval, "Approve this operation?"),
			wantAlias:   "PermissionRequest",
			wantDetails: map[string]any{"kind": "approval", "summary": "Approve this operation?"},
		},
		{
			name:        "resource limit",
			category:    CategoryResourceLimit,
			details:     NewResourceLimitDetails("native"),
			wantAlias:   "PreCompact",
			wantDetails: map[string]any{"compaction_mode": "native"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope, err := NewEnvelope(EnvelopeInput{
				Scope:      ScopeClient,
				Category:   test.category,
				OccurredAt: occurredAt,
				Context:    test.context,
				Details:    test.details,
			})
			if err != nil {
				t.Fatalf("new envelope: %v", err)
			}
			raw, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if got["category"] != string(test.category) || got["hook_event_name"] != test.wantAlias {
				t.Fatalf("identity = category:%v alias:%v", got["category"], got["hook_event_name"])
			}
			if !reflect.DeepEqual(got["details"], test.wantDetails) {
				t.Fatalf("details = %#v, want %#v", got["details"], test.wantDetails)
			}
			context, ok := got["context"].(map[string]any)
			if !ok {
				t.Fatalf("context = %#v, want object", got["context"])
			}
			if test.category == CategorySessionStart {
				if !reflect.DeepEqual(context, map[string]any{"session_id": "session-1"}) {
					t.Fatalf("session context = %#v", context)
				}
			} else if len(context) != 0 {
				t.Fatalf("empty optional context = %#v, want empty object", context)
			}
			assertJSONKeysAbsent(t, got, []string{
				"server_execution_root",
				"workspace_path",
				"run_id",
				"step_id",
				"prompt_id",
				"approval_id",
				"subscription_id",
			})
		})
	}
}

func TestLifecycleEnvelopeRejectsInvalidVariantContracts(t *testing.T) {
	occurredAt := time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		input EnvelopeInput
	}{
		{
			name: "invalid scope",
			input: EnvelopeInput{
				Scope:      Scope("invalid"),
				Category:   CategoryTaskComplete,
				OccurredAt: occurredAt,
				Details:    NewTaskCompleteDetails("Done", false),
			},
		},
		{
			name: "invalid category",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   Category("invalid"),
				OccurredAt: occurredAt,
				Details:    NewTaskCompleteDetails("Done", false),
			},
		},
		{
			name: "zero occurrence time",
			input: EnvelopeInput{
				Scope:    ScopeClient,
				Category: CategorySessionStart,
				Details:  NewSessionStartDetails(OpeningKindNew),
			},
		},
		{
			name: "session start without materialized identity",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategorySessionStart,
				OccurredAt: occurredAt,
				Details:    NewSessionStartDetails(OpeningKindNew),
			},
		},
		{
			name: "category detail mismatch",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategoryTaskComplete,
				OccurredAt: occurredAt,
				Details:    NewSessionStartDetails(OpeningKindNew),
			},
		},
		{
			name: "missing details",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategorySessionStart,
				OccurredAt: occurredAt,
			},
		},
		{
			name: "invalid opening kind",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategorySessionStart,
				OccurredAt: occurredAt,
				Details:    NewSessionStartDetails(OpeningKind("invalid")),
			},
		},
		{
			name: "blank final answer",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategoryTaskComplete,
				OccurredAt: occurredAt,
				Details:    NewTaskCompleteDetails(" \t ", false),
			},
		},
		{
			name: "blank error diagnostic",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategoryTaskError,
				OccurredAt: occurredAt,
				Details:    NewTaskErrorDetails(""),
			},
		},
		{
			name: "invalid input kind",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategoryInputRequired,
				OccurredAt: occurredAt,
				Details:    NewInputRequiredDetails(InputKind("invalid"), "Question"),
			},
		},
		{
			name: "blank input summary",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategoryInputRequired,
				OccurredAt: occurredAt,
				Details:    NewInputRequiredDetails(InputKindQuestion, ""),
			},
		},
		{
			name: "blank compaction mode",
			input: EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategoryResourceLimit,
				OccurredAt: occurredAt,
				Details:    NewResourceLimitDetails(" "),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewEnvelope(test.input); err == nil {
				t.Fatal("invalid lifecycle envelope succeeded")
			}
		})
	}
}

func TestLifecycleEnvelopeValidatesOptionalContextAndTruncation(t *testing.T) {
	occurredAt := time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC)
	blankTitle := " "
	zeroSession := runtimeids.SessionID{}
	tests := []struct {
		name       string
		context    Context
		truncation *Truncation
	}{
		{name: "blank session title", context: Context{SessionTitle: &blankTitle}},
		{name: "zero session ID", context: Context{SessionID: &zeroSession}},
		{name: "empty truncation", truncation: &Truncation{}},
		{name: "duplicate truncation", truncation: &Truncation{Fields: []TruncationField{TruncationFieldFinalAnswer, TruncationFieldFinalAnswer}}},
		{name: "unknown truncation", truncation: &Truncation{Fields: []TruncationField{"details.unknown"}}},
		{name: "wrong-category truncation", truncation: &Truncation{Fields: []TruncationField{TruncationFieldDiagnostic}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewEnvelope(EnvelopeInput{
				Scope:      ScopeClient,
				Category:   CategoryTaskComplete,
				OccurredAt: occurredAt,
				Context:    test.context,
				Details:    NewTaskCompleteDetails("Done", false),
				Truncation: test.truncation,
			}); err == nil {
				t.Fatal("invalid context or truncation succeeded")
			}
		})
	}

	envelope, err := NewEnvelope(EnvelopeInput{
		Scope:      ScopeClient,
		Category:   CategoryTaskComplete,
		OccurredAt: occurredAt,
		Context:    Context{SessionTitle: stringPtr("Title")},
		Details:    NewTaskCompleteDetails("Done", false),
		Truncation: &Truncation{Fields: []TruncationField{
			TruncationFieldSessionTitle,
			TruncationFieldFinalAnswer,
		}},
	})
	if err != nil {
		t.Fatalf("valid truncation: %v", err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var got struct {
		Truncation Truncation `json:"truncation"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode truncation: %v", err)
	}
	if want := []TruncationField{TruncationFieldSessionTitle, TruncationFieldFinalAnswer}; !reflect.DeepEqual(got.Truncation.Fields, want) {
		t.Fatalf("truncation fields = %#v, want %#v", got.Truncation.Fields, want)
	}
}

func sessionIDPtr(t *testing.T, raw string) *runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	return &id
}

func stringPtr(value string) *string {
	return &value
}

func TestWorkflowTaskIDRejectsBlankOrNormalizedAbsence(t *testing.T) {
	for _, raw := range []string{"", " ", " task-1"} {
		if _, err := ParseWorkflowTaskID(raw); err == nil {
			t.Fatalf("ParseWorkflowTaskID(%q) succeeded", raw)
		}
	}
}

func assertJSONKeysAbsent(t *testing.T, value any, forbidden []string) {
	t.Helper()
	forbiddenSet := make(map[string]struct{}, len(forbidden))
	for _, key := range forbidden {
		forbiddenSet[key] = struct{}{}
	}
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				if _, exists := forbiddenSet[key]; exists {
					t.Fatalf("payload exposes forbidden field %q", key)
				}
				walk(nested)
			}
		case []any:
			for _, nested := range typed {
				walk(nested)
			}
		}
	}
	walk(value)
}
