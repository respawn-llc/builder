package lifecyclecontract

import (
	"encoding/json"
	"testing"
	"time"

	"core/shared/runtimeids"
)

func TestEncodeClientLifecycleEvents(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	title := "Lifecycle"
	taskID := WorkflowTaskID("BUI-51")
	context := Context{
		SessionID:      &sessionID,
		SessionTitle:   &title,
		WorkflowTaskID: &taskID,
	}
	occurredAt := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		event    Event
		category Category
		alias    CompatibilityAlias
		detail   string
	}{
		{"session start", NewSessionStart(occurredAt, true, context, OpeningKindResumed), CategorySessionStart, CompatibilityAliasSessionStart, `"kind":"resumed"`},
		{"task complete", NewTaskComplete(occurredAt, false, context, "done", true), CategoryTaskComplete, CompatibilityAliasStop, `"work_performed":true`},
		{"task error", NewTaskError(occurredAt, false, context, "boom"), CategoryTaskError, CompatibilityAliasPostToolUseFailure, `"diagnostic":"boom"`},
		{"input required", NewInputRequired(occurredAt, true, context, InputKindApproval, "approve?"), CategoryInputRequired, CompatibilityAliasPermissionRequest, `"kind":"approval"`},
		{"compaction started", NewCompactionStarted(occurredAt, false, context, "manual"), CategoryResourceLimit, CompatibilityAliasPreCompact, `"compaction_mode":"manual"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := Encode(test.event)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if decoded["schema_version"] != float64(SchemaVersion) ||
				decoded["cesp_version"] != CESPVersion ||
				decoded["scope"] != string(ScopeClient) ||
				decoded["category"] != string(test.category) ||
				decoded["hook_event_name"] != string(test.alias) {
				t.Fatalf("identity = %#v", decoded)
			}
			if string(raw) == "" || !containsJSONFragment(raw, test.detail) {
				t.Fatalf("payload %s does not contain %s", raw, test.detail)
			}
			for _, forbidden := range []string{"run_id", "step_id", "prompt_id", "approval_id", "subscription_id", "workspace_path", "server_execution_root"} {
				if _, present := decoded[forbidden]; present {
					t.Fatalf("forbidden field %q present in %#v", forbidden, decoded)
				}
			}
		})
	}
}

func TestEncodeOmitsAbsentLifecycleContext(t *testing.T) {
	raw, err := Encode(NewTaskComplete(time.Unix(1, 0).UTC(), false, Context{}, "done", false))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var decoded struct {
		Context map[string]any `json:"context"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Context) != 0 {
		t.Fatalf("context = %#v, want empty object", decoded.Context)
	}
}

func containsJSONFragment(raw []byte, fragment string) bool {
	for index := 0; index+len(fragment) <= len(raw); index++ {
		if string(raw[index:index+len(fragment)]) == fragment {
			return true
		}
	}
	return false
}
