package workflowruntime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/workflow"
	"core/shared/config"
)

type completionSchemaProperty struct {
	Type  any                        `json:"type"`
	Enum  []string                   `json:"enum,omitempty"`
	AnyOf []completionSchemaProperty `json:"anyOf,omitempty"`
	OneOf []completionSchemaProperty `json:"oneOf,omitempty"`
}

func mustCompletionContract(
	t *testing.T,
	transitions []CompletionTransition,
) CompletionContract {
	t.Helper()
	return CompletionContract{Transitions: transitions}
}

func TestSelectCompletionMode(t *testing.T) {
	supported := llm.ProviderCapabilities{SupportsResponsesAPI: true}
	tests := []struct {
		name      string
		selection CompletionModeSelection
		want      CompletionMode
		wantErr   error
	}{
		{name: "auto structured", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeAuto, ProviderCapabilities: supported, ShellAvailable: true}, want: CompletionModeStructuredOutput},
		{name: "auto tool", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeAuto, ShellAvailable: true}, want: CompletionModeTool},
		{name: "auto continuation shell", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeAuto, ProviderCapabilities: supported, HasContinueSessionEdge: true, ShellAvailable: true}, want: CompletionModeShellCommand},
		{name: "auto unavailable shell", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeAuto, ProviderCapabilities: supported}, want: CompletionModeUnstructuredOutput},
		{name: "forced tool", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeTool}, want: CompletionModeTool},
		{name: "forced shell", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeShellCommand, ShellAvailable: true}, want: CompletionModeShellCommand},
		{name: "forced shell unavailable", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeShellCommand}, wantErr: ErrShellCompletionUnavailable},
		{name: "forced unstructured", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeUnstructured}, want: CompletionModeUnstructuredOutput},
		{name: "forced structured unsupported", selection: CompletionModeSelection{ConfiguredMode: config.WorkflowCompletionModeStructuredOutput}, wantErr: ErrStructuredOutputUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SelectCompletionMode(test.selection)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("SelectCompletionMode(%+v) error = %v, want %v", test.selection, err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("SelectCompletionMode(%+v) = %q, %v; want %q, nil", test.selection, got, err, test.want)
			}
		})
	}
}

func TestCompletionJSONSchemaUsesAdvertisedCompletionContract(t *testing.T) {
	raw, err := CompletionJSONSchema(mustCompletionContract(t, []CompletionTransition{
		{ID: "done", Parameters: []workflow.Parameter{{Key: "summary"}}},
		{ID: "blocked", Parameters: []workflow.Parameter{{Key: "risk"}}},
	}))
	if err != nil {
		t.Fatalf("CompletionJSONSchema: %v", err)
	}
	var schema struct {
		AdditionalProperties bool                                `json:"additionalProperties"`
		Required             []string                            `json:"required"`
		Properties           map[string]completionSchemaProperty `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if schema.AdditionalProperties {
		t.Fatal("schema allows additional properties")
	}
	if got := strings.Join(schema.Properties["transition"].Enum, ","); got != "blocked,done" {
		t.Fatalf("transition enum = %q, want blocked,done", got)
	}
	if got := schema.Properties["transition"].Type; got != "string" {
		t.Fatalf("transition property type = %#v, want string", got)
	}
	for _, field := range []string{"transition", "commentary", "risk", "summary"} {
		if !containsString(schema.Required, field) {
			t.Fatalf("schema required fields = %v, missing %q", schema.Required, field)
		}
	}
	for _, field := range []string{"commentary", "risk", "summary"} {
		assertNullableStringProperty(t, schema.Properties[field])
	}
	for _, field := range []string{"commentary", "risk", "summary"} {
		if len(schema.Properties[field].OneOf) != 0 {
			t.Fatalf("structured output property %q uses unsupported oneOf", field)
		}
	}
}

func TestCompletionJSONSchemaRequiresSingleTransitionParameters(t *testing.T) {
	raw, err := CompletionJSONSchema(mustCompletionContract(t, []CompletionTransition{{
		ID:         "done",
		Parameters: []workflow.Parameter{{Key: "summary"}, {Key: "risk"}},
	}}))
	if err != nil {
		t.Fatalf("CompletionJSONSchema: %v", err)
	}
	var schema struct {
		Required   []string                            `json:"required"`
		Properties map[string]completionSchemaProperty `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if _, found := schema.Properties["transition"]; found {
		t.Fatalf("single-transition schema exposed transition: %+v", schema.Properties)
	}
	for _, field := range []string{"commentary", "risk", "summary"} {
		if !containsString(schema.Required, field) {
			t.Fatalf("single-transition schema required fields = %v, missing %q", schema.Required, field)
		}
	}
	if got := schema.Properties["summary"].Type; got != "string" {
		t.Fatalf("summary property type = %#v, want string", got)
	}
}

func TestDecodeCompletionEnforcesTransitionAndParameterSelection(t *testing.T) {
	contract := mustCompletionContract(t, []CompletionTransition{
		{ID: "done", Parameters: []workflow.Parameter{{Key: "summary"}}},
		{ID: "blocked", Parameters: []workflow.Parameter{{Key: "reason"}}},
	})
	for _, test := range []struct {
		name  string
		raw   string
		code  string
		field string
	}{
		{name: "legacy transition ID", raw: `{"transition_id":"done","summary":"ok"}`, code: "required_field_missing", field: "transition"},
		{name: "missing transition", raw: `{"summary":"ok"}`, code: "required_field_missing", field: "transition"},
		{name: "undeclared transition", raw: `{"transition":"unknown","summary":"ok"}`, code: "invalid_transition", field: "transition"},
		{name: "parameter from unselected transition", raw: `{"transition":"done","summary":"ok","reason":"no"}`, code: "unexpected_parameter", field: "reason"},
	} {
		t.Run(test.name, func(t *testing.T) {
			validation := requireValidationError(t, decodeCompletionError(test.raw, contract))
			if !hasIssue(validation, test.code, test.field) {
				t.Fatalf("validation issues = %+v, missing %s/%s", validation.Issues, test.code, test.field)
			}
		})
	}
}

func TestDecodeCompletionInfersSingleTransitionAndRequiresParameters(t *testing.T) {
	contract := mustCompletionContract(t, []CompletionTransition{{
		ID:         "done",
		Parameters: []workflow.Parameter{{Key: "summary"}, {Key: "risk"}},
	}})
	validation := requireValidationError(t, decodeCompletionError(`{"commentary":"done"}`, contract))
	for _, field := range []string{"risk", "summary"} {
		if !hasIssue(validation, "required_parameter_missing", field) {
			t.Fatalf("validation issues = %+v, missing required %q", validation.Issues, field)
		}
	}
	parsed, err := DecodeCompletion(json.RawMessage(`{"summary":"done","risk":"low"}`), contract)
	if err != nil {
		t.Fatalf("DecodeCompletion: %v", err)
	}
	if parsed.TransitionID != "done" || parsed.OutputValues["summary"] != "done" || parsed.OutputValues["risk"] != "low" {
		t.Fatalf("parsed completion = %+v", parsed)
	}
}

func TestDecodeCompletionUsesUnavailableRoleIssueForMissingProtectedRole(t *testing.T) {
	contract := mustCompletionContract(t, []CompletionTransition{{
		ID: "done",
		Parameters: []workflow.Parameter{{
			Key:     "agent_role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}},
	}})
	validation := requireValidationError(t, decodeCompletionError(`{"commentary":"done"}`, contract))
	if !hasIssue(validation, "workflow.target_agent.unavailable_role", "agent_role") {
		t.Fatalf("validation issues = %+v, want unavailable-role issue", validation.Issues)
	}
}

func TestDecodeCompletionAcceptsOptionalCommentary(t *testing.T) {
	contract := mustCompletionContract(t, []CompletionTransition{{ID: "done", Parameters: []workflow.Parameter{{Key: "summary"}}}})
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: `{"summary":"done"}`, want: ""},
		{raw: `{"commentary":null,"summary":"done"}`, want: ""},
		{raw: `{"commentary":"evidence","summary":"done"}`, want: "evidence"},
	} {
		parsed, err := DecodeCompletion(json.RawMessage(test.raw), contract)
		if err != nil {
			t.Fatalf("DecodeCompletion(%s): %v", test.raw, err)
		}
		if parsed.Commentary != test.want {
			t.Fatalf("commentary = %q, want %q", parsed.Commentary, test.want)
		}
	}
}

func TestDecodeCompletionDiscardsUndeclaredFields(t *testing.T) {
	contract := mustCompletionContract(t, []CompletionTransition{{
		ID:         "done",
		Parameters: []workflow.Parameter{{Key: "summary"}},
	}})
	parsed, err := DecodeCompletion(
		json.RawMessage(`{"summary":"done","legacy":"discard","nested":{"ignored":true}}`),
		contract,
	)
	if err != nil {
		t.Fatalf("DecodeCompletion: %v", err)
	}
	if len(parsed.OutputValues) != 1 || parsed.OutputValues["summary"] != "done" {
		t.Fatalf("output values = %+v, want only declared summary", parsed.OutputValues)
	}
}

func TestDecodeCompletionStringifiesParameterValues(t *testing.T) {
	parsed, err := DecodeCompletion(json.RawMessage(`{"summary":123,"risk":false,"details":{"ok":true},"items":["a",2],"empty":null}`), mustCompletionContract(t, []CompletionTransition{{
		ID: "done",
		Parameters: []workflow.Parameter{
			{Key: "summary"}, {Key: "risk"}, {Key: "details"}, {Key: "items"}, {Key: "empty"},
		},
	}}))
	if err != nil {
		t.Fatalf("DecodeCompletion: %v", err)
	}
	for key, want := range map[string]string{
		"summary": "123", "risk": "false", "details": `{"ok":true}`, "items": `["a",2]`, "empty": "null",
	} {
		if parsed.OutputValues[key] != want {
			t.Fatalf("output %s = %q, want %q; all outputs = %+v", key, parsed.OutputValues[key], want, parsed.OutputValues)
		}
	}
}

func TestDecodeCompletionAcceptsNullForUnselectedTransitionParameter(t *testing.T) {
	parsed, err := DecodeCompletion(json.RawMessage(`{"transition":"done","summary":"done","reason":null}`), mustCompletionContract(t, []CompletionTransition{
		{ID: "done", Parameters: []workflow.Parameter{{Key: "summary"}}},
		{ID: "blocked", Parameters: []workflow.Parameter{{Key: "reason"}}},
	}))
	if err != nil {
		t.Fatalf("DecodeCompletion: %v", err)
	}
	if parsed.OutputValues["summary"] != "done" {
		t.Fatalf("summary = %q", parsed.OutputValues["summary"])
	}
	if _, found := parsed.OutputValues["reason"]; found {
		t.Fatalf("unselected null parameter survived decoding: %+v", parsed.OutputValues)
	}
}

func TestDecodeUnstructuredCompletionAcceptsOnlyRawJSONObject(t *testing.T) {
	contract := mustCompletionContract(t, []CompletionTransition{{ID: "done", Parameters: []workflow.Parameter{{Key: "summary"}}}})
	for _, raw := range []string{
		"```json\n{\"summary\":\"ok\"}\n```",
		"{\"summary\":\"ok\"}\ncomplete",
		"[]",
	} {
		if _, err := DecodeUnstructuredCompletion(raw, contract); err == nil {
			t.Fatalf("DecodeUnstructuredCompletion accepted %q", raw)
		}
	}
	parsed, err := DecodeUnstructuredCompletion(" \n{\"summary\":\"ok\"}\t", contract)
	if err != nil {
		t.Fatalf("DecodeUnstructuredCompletion: %v", err)
	}
	if parsed.TransitionID != "done" || parsed.OutputValues["summary"] != "ok" {
		t.Fatalf("parsed completion = %+v", parsed)
	}
}

func assertNullableStringProperty(t *testing.T, property completionSchemaProperty) {
	t.Helper()
	if len(property.AnyOf) != 2 ||
		property.AnyOf[0].Type != "string" ||
		property.AnyOf[1].Type != "null" {
		t.Fatalf("property = %#v, want string/null union", property)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func decodeCompletionError(raw string, contract CompletionContract) error {
	_, err := DecodeCompletion(json.RawMessage(raw), contract)
	return err
}

func requireValidationError(t *testing.T, err error) ValidationError {
	t.Helper()
	var validation ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want ValidationError", err, err)
	}
	return validation
}

func hasIssue(validation ValidationError, code, field string) bool {
	for _, issue := range validation.Issues {
		if issue.Code == code && issue.Field == field {
			return true
		}
	}
	return false
}
