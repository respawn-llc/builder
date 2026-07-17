package runprompt

import (
	"context"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type intentRecordingPromptRuntime struct{}

func (intentRecordingPromptRuntime) RecordPromptHistory(context.Context, string, string) error {
	return nil
}

func (intentRecordingPromptRuntime) SubmitUserMessage(context.Context, string) (PromptAssistantMessage, error) {
	return PromptAssistantMessage{SessionID: "session-result", Content: "done"}, nil
}

func (intentRecordingPromptRuntime) Logf(string, ...any) {}
func (intentRecordingPromptRuntime) Close() error        { return nil }

type intentRecordingPromptLauncher struct {
	request serverapi.RunPromptRequest
}

func (l *intentRecordingPromptLauncher) PrepareHeadlessPrompt(_ context.Context, req serverapi.RunPromptRequest, _ serverapi.RunPromptProgressSink) (PromptSessionRuntime, error) {
	l.request = req
	return intentRecordingPromptRuntime{}, nil
}

func TestRunPromptCreateAndOpenUseTypedLaunchIntent(t *testing.T) {
	parent := mustRunPromptSessionID(t, "parent-session")
	selected := mustRunPromptSessionID(t, "selected-session")

	tests := []struct {
		name   string
		intent serverapi.SessionLaunchIntent
		check  func(t *testing.T, got serverapi.SessionLaunchIntent)
	}{
		{
			name:   "create without parent",
			intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
			check: func(t *testing.T, got serverapi.SessionLaunchIntent) {
				if got.Kind() != serverapi.SessionLaunchIntentCreateNew {
					t.Fatalf("intent kind = %q, want create_new", got.Kind())
				}
				origin, present := got.CreateOrigin()
				if !present || origin.Kind() != serverapi.SessionCreateOriginIndependent {
					t.Fatalf("create origin = %+v/%v, want independent", origin, present)
				}
			},
		},
		{
			name:   "create with parent",
			intent: serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(parent)),
			check: func(t *testing.T, got serverapi.SessionLaunchIntent) {
				origin, present := got.CreateOrigin()
				parentID, hasSource := origin.SessionID()
				if !present || origin.Kind() != serverapi.SessionCreateOriginParentAgent || !hasSource || parentID != parent {
					t.Fatalf("parent-agent origin = %+v/%v, want %q", origin, present, parent.String())
				}
			},
		},
		{
			name:   "open existing",
			intent: serverapi.OpenExistingSessionLaunchIntent(selected),
			check: func(t *testing.T, got serverapi.SessionLaunchIntent) {
				sessionID, present := got.SessionID()
				if !present || sessionID != selected {
					t.Fatalf("open intent = %q/%v, want %q/true", sessionID.String(), present, selected.String())
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			launcher := &intentRecordingPromptLauncher{}
			service := NewPromptService(launcher)
			_, err := service.RunPrompt(context.Background(), serverapi.RunPromptRequest{
				ClientRequestID: "request-" + test.name,
				Intent:          test.intent,
				Prompt:          "run the requested work",
			}, nil)
			if err != nil {
				t.Fatalf("RunPrompt: %v", err)
			}
			test.check(t, launcher.request.Intent)
		})
	}
}

func TestRunPromptRejectsMissingAndInvalidTypedSessionIdentity(t *testing.T) {
	var zero runtimeids.SessionID
	for _, intent := range []serverapi.SessionLaunchIntent{
		serverapi.SessionLaunchIntent{},
		serverapi.OpenExistingSessionLaunchIntent(zero),
	} {
		launcher := &intentRecordingPromptLauncher{}
		_, err := NewPromptService(launcher).RunPrompt(context.Background(), serverapi.RunPromptRequest{
			ClientRequestID: "invalid-intent",
			Intent:          intent,
			Prompt:          "work",
		}, nil)
		if err == nil {
			t.Fatalf("RunPrompt(%+v) succeeded; want invalid typed identity error", intent)
		}
	}
}

func TestRunPromptSessionPlanRequestCarriesOnlyTypedLaunchIntent(t *testing.T) {
	target := mustRunPromptSessionID(t, "target-session")
	request := serverapi.SessionPlanRequest{
		ClientRequestID: "plan-request",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(target),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("SessionPlanRequest.Validate: %v", err)
	}
	if got, present := request.Intent.SessionID(); !present || got != target {
		t.Fatalf("plan intent session = %q/%v, want %q/true", got.String(), present, target.String())
	}
}

func mustRunPromptSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
