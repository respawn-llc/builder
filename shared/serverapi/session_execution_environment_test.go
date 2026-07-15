package serverapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"core/shared/runtimeids"
)

func TestSessionExecutionEnvironmentRoundTrip(t *testing.T) {
	sessionID := mustExecutionEnvironmentSessionID(t, "environment-session")
	want := SessionExecutionEnvironmentResponse{
		Environment: SessionExecutionEnvironment{
			SessionID: sessionID,
			Workspace: AvailableSessionExecutionWorkspace("/workspace/current"),
			Branch:    UnavailableSessionExecutionBranch(SessionExecutionBranchUnavailableDetachedHead),
			Auth: AvailableSessionExecutionAuth(SessionExecutionAuth{
				Provider: "openai",
				Method:   SessionExecutionAuthMethodNone,
			}),
			Model: FailedSessionExecutionModel(SessionExecutionFieldError{
				Code:    SessionExecutionFieldErrorInvalidConfiguration,
				Message: "configured model is invalid",
			}),
		},
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got SessionExecutionEnvironmentResponse
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if got.Environment.SessionID != sessionID {
		t.Fatalf("session ID = %q, want %q", got.Environment.SessionID.String(), sessionID.String())
	}
}

func TestSessionExecutionEnvironmentFieldStatesRemainIndependent(t *testing.T) {
	environment := SessionExecutionEnvironment{
		SessionID: mustExecutionEnvironmentSessionID(t, "partial-environment"),
		Workspace: FailedSessionExecutionWorkspace(SessionExecutionFieldError{
			Code:    SessionExecutionFieldErrorSourceFailure,
			Message: "workspace target lookup failed",
		}),
		Branch: UnavailableSessionExecutionBranch(SessionExecutionBranchUnavailableNotGitRepository),
		Auth:   UnavailableSessionExecutionAuth(SessionExecutionAuthUnavailableNotApplicable),
		Model: AvailableSessionExecutionModel(SessionExecutionModel{
			Name:     "gpt-5.6-sol",
			Provider: "openai",
			Locked:   true,
		}),
	}
	if err := environment.Validate(); err != nil {
		t.Fatalf("Validate partial environment: %v", err)
	}
	if environment.Workspace.Kind() != SessionExecutionFieldFailed {
		t.Fatalf("workspace kind = %q, want failed", environment.Workspace.Kind())
	}
	if environment.Branch.Kind() != SessionExecutionFieldUnavailable {
		t.Fatalf("branch kind = %q, want unavailable", environment.Branch.Kind())
	}
	if environment.Auth.Kind() != SessionExecutionFieldUnavailable {
		t.Fatalf("auth kind = %q, want unavailable", environment.Auth.Kind())
	}
	if environment.Model.Kind() != SessionExecutionFieldAvailable {
		t.Fatalf("model kind = %q, want available", environment.Model.Kind())
	}
}

func TestSessionExecutionEnvironmentWorkspaceAndModelUnavailableReasonsAreTyped(t *testing.T) {
	workspace := UnavailableSessionExecutionWorkspace(SessionExecutionWorkspaceUnavailableNotConfigured)
	workspaceReason, ok := workspace.UnavailableReason()
	if !ok || workspaceReason != SessionExecutionWorkspaceUnavailableNotConfigured {
		t.Fatalf("workspace unavailable reason = %q/%v", workspaceReason, ok)
	}

	model := UnavailableSessionExecutionModel(SessionExecutionModelUnavailableNotConfigured)
	modelReason, ok := model.UnavailableReason()
	if !ok || modelReason != SessionExecutionModelUnavailableNotConfigured {
		t.Fatalf("model unavailable reason = %q/%v", modelReason, ok)
	}

	for _, field := range []any{workspace, model} {
		encoded, err := json.Marshal(field)
		if err != nil {
			t.Fatalf("Marshal(%T): %v", field, err)
		}
		if len(encoded) == 0 {
			t.Fatalf("Marshal(%T) produced no JSON", field)
		}
	}
}

func TestSessionExecutionEnvironmentRejectsInvalidContractJSON(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"environment":{}}`,
		`{"environment":{"session_id":"","workspace":{"kind":"unavailable","reason":"not_configured"},"branch":{"kind":"unavailable","reason":"not_git_repository"},"auth":{"kind":"unavailable","reason":"not_applicable"},"model":{"kind":"unavailable","reason":"not_configured"}}}`,
		`{"environment":{"session_id":"environment-session","workspace":{"kind":"unknown"},"branch":{"kind":"unavailable","reason":"not_git_repository"},"auth":{"kind":"unavailable","reason":"not_applicable"},"model":{"kind":"unavailable","reason":"not_configured"}}}`,
		`{"environment":{"session_id":"environment-session","workspace":{"kind":"available"},"branch":{"kind":"unavailable","reason":"not_git_repository"},"auth":{"kind":"unavailable","reason":"not_applicable"},"model":{"kind":"unavailable","reason":"not_configured"}}}`,
		`{"environment":{"session_id":"environment-session","workspace":{"kind":"failed","error":{"code":"","message":"failed"}},"branch":{"kind":"unavailable","reason":"not_git_repository"},"auth":{"kind":"unavailable","reason":"not_applicable"},"model":{"kind":"unavailable","reason":"not_configured"}}}`,
		`{"environment":{"session_id":"environment-session","workspace":{"kind":"unavailable","reason":"unknown"},"branch":{"kind":"unavailable","reason":"not_git_repository"},"auth":{"kind":"unavailable","reason":"not_applicable"},"model":{"kind":"unavailable","reason":"not_configured"}}}`,
		`{"environment":{"session_id":"environment-session","workspace":{"kind":"unavailable","reason":"not_configured"},"branch":{"kind":"unavailable","reason":"not_git_repository"},"auth":{"kind":"unavailable","reason":"not_applicable"},"model":{"kind":"unavailable","reason":"unknown"}}}`,
		`{"environment":{"session_id":"environment-session","workspace":{"kind":"unavailable","reason":"not_configured"},"branch":{"kind":"unavailable","reason":"not_git_repository"},"auth":{"kind":"unavailable","reason":"not_applicable"},"model":{"kind":"unavailable","reason":"not_configured"},"placeholder":"loading"}}`,
	} {
		var got SessionExecutionEnvironmentResponse
		if err := json.Unmarshal([]byte(raw), &got); err == nil {
			t.Fatalf("Unmarshal(%s) unexpectedly succeeded: %+v", raw, got)
		}
	}
}

func TestSessionExecutionEnvironmentFieldDecodersRemainStrictAndTyped(t *testing.T) {
	tests := []struct {
		name    string
		target  func() any
		valid   string
		invalid string
	}{
		{
			name:    "workspace",
			target:  func() any { return &SessionExecutionWorkspaceField{} },
			valid:   `{"kind":"available","value":{"path":"/workspace"}}`,
			invalid: `{"kind":"available","value":{"name":"main"}}`,
		},
		{
			name:    "branch",
			target:  func() any { return &SessionExecutionBranchField{} },
			valid:   `{"kind":"available","value":{"name":"main"}}`,
			invalid: `{"kind":"available","value":{"path":"/workspace"}}`,
		},
		{
			name:    "auth",
			target:  func() any { return &SessionExecutionAuthField{} },
			valid:   `{"kind":"available","value":{"provider":"openai","method":"none"}}`,
			invalid: `{"kind":"available","value":{"provider":"openai","method":"unknown"}}`,
		},
		{
			name:    "model",
			target:  func() any { return &SessionExecutionModelField{} },
			valid:   `{"kind":"available","value":{"name":"gpt-5","provider":"openai","locked":true}}`,
			invalid: `{"kind":"available","value":{"name":"gpt-5","provider":""}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(test.valid), test.target()); err != nil {
				t.Fatalf("valid typed field: %v", err)
			}
			if err := json.Unmarshal([]byte(test.invalid), test.target()); err == nil {
				t.Fatal("field accepted an invalid typed value")
			}
			withUnknown := test.valid[:len(test.valid)-1] + `,"unknown":true}`
			if err := json.Unmarshal([]byte(withUnknown), test.target()); err == nil {
				t.Fatal("field accepted an unknown member")
			}
			if err := json.Unmarshal([]byte(test.valid+` {}`), test.target()); err == nil {
				t.Fatal("field accepted trailing JSON")
			}
		})
	}
}

func TestSessionExecutionEnvironmentFieldDecoderRejectsNullAndIncompatibleMembers(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"available","value":null}`,
		`{"kind":"available","value":{"path":"/workspace"},"reason":null}`,
		`{"kind":"available","value":{"path":"/workspace"},"error":null}`,
		`{"kind":"available","value":{"path":"/workspace","unknown":true}}`,
		`{"kind":"unavailable","reason":null}`,
		`{"kind":"unavailable","reason":""}`,
		`{"kind":"unavailable","reason":"not_configured","value":null}`,
		`{"kind":"unavailable","reason":"not_configured","error":null}`,
		`{"kind":"failed","error":null}`,
		`{"kind":"failed","error":{"code":"source_failure","message":"failed"},"value":null}`,
		`{"kind":"failed","error":{"code":"source_failure","message":"failed"},"reason":null}`,
		`{"kind":"failed","error":{"code":"source_failure","message":"failed","unknown":true}}`,
	} {
		var field SessionExecutionWorkspaceField
		if err := json.Unmarshal([]byte(raw), &field); err == nil {
			t.Fatalf("Unmarshal(%s) unexpectedly succeeded: %+v", raw, field)
		}
	}
}

func TestSessionExecutionEnvironmentRequestUsesAuthoritativeSessionID(t *testing.T) {
	sessionID := mustExecutionEnvironmentSessionID(t, "legacy-environment-session")
	request := SessionExecutionEnvironmentRequest{SessionID: sessionID}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded SessionExecutionEnvironmentRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.SessionID != sessionID {
		t.Fatalf("session ID = %q, want %q", decoded.SessionID.String(), sessionID.String())
	}
}

func mustExecutionEnvironmentSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
