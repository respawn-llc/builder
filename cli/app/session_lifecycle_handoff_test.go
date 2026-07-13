package app

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func requireResolvedSessionHandoff(t *testing.T, handoff *resolvedSessionHandoff) *resolvedSessionHandoff {
	t.Helper()
	if handoff == nil {
		t.Fatal("expected session lifecycle to continue")
	}
	return handoff
}

func requireSessionPickerDestination(t *testing.T, handoff *resolvedSessionHandoff) {
	t.Helper()
	handoff = requireResolvedSessionHandoff(t, handoff)
	if _, ok := handoff.Destination.(sessionPickerDestination); !ok {
		t.Fatalf("destination = %T, want session picker", handoff.Destination)
	}
}

func requireSessionOpenDestination(t *testing.T, handoff *resolvedSessionHandoff) string {
	t.Helper()
	handoff = requireResolvedSessionHandoff(t, handoff)
	destination, ok := handoff.Destination.(sessionOpenDestination)
	if !ok {
		t.Fatalf("destination = %T, want existing session", handoff.Destination)
	}
	return destination.SessionID
}

func requireSessionCreateDestination(t *testing.T, handoff *resolvedSessionHandoff) *sessionParentReference {
	t.Helper()
	handoff = requireResolvedSessionHandoff(t, handoff)
	destination, ok := handoff.Destination.(sessionCreateDestination)
	if !ok {
		t.Fatalf("destination = %T, want new session", handoff.Destination)
	}
	return destination.Parent
}

func TestResolveAndReleaseSessionHandoffOwnsInitialInputPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		action         UIAction
		initialInput   string
		wantPrecedence sessionInitialInputPrecedence
	}{
		{
			name:           "open session preserves exact transition input",
			action:         UIActionOpenSession,
			initialInput:   " \nExact café 👩🏽‍💻\n尾  ",
			wantPrecedence: sessionInitialInputPreferTransition,
		},
		{
			name:           "open session preserves intentional empty transition input",
			action:         UIActionOpenSession,
			initialInput:   "",
			wantPrecedence: sessionInitialInputPreferTransition,
		},
		{
			name:           "other transitions prefer stored draft",
			action:         UIActionResume,
			initialInput:   "ordinary transition input",
			wantPrecedence: sessionInitialInputPreferStoredDraft,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var initialInputRequest serverapi.SessionInitialInputRequest
			lifecycle := &recordingSessionLifecycleClient{
				resolveTransition: func(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
					return serverapi.SessionResolveTransitionResponse{
						NextSessionID:  "parent-session",
						InitialInput:   req.Transition.InitialInput,
						ShouldContinue: true,
					}, nil
				},
				getInitialInput: func(_ context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
					initialInputRequest = req
					if req.OverrideStoredDraft {
						return serverapi.SessionInitialInputResponse{Input: req.TransitionInput}, nil
					}
					return serverapi.SessionInitialInputResponse{
						Input: "stored parent draft",
						RecoveryBuffers: []serverapi.SessionDraftRecoveryBuffer{
							{Kind: serverapi.SessionDraftRecoveryBufferQueuedInput, ID: "stale-parent-buffer", Text: "stale queued input"},
						},
					}, nil
				},
			}
			server := narrowSessionLifecycleServer{lifecycle: lifecycle}

			handoff, err := resolveAndReleaseSessionHandoff(
				context.Background(),
				server,
				nil,
				"child-session",
				UITransition{Action: tt.action, InitialInput: tt.initialInput},
				&runtimeLaunchPlan{close: func() error { return nil }},
			)
			if err != nil {
				t.Fatalf("resolve and release handoff: %v", err)
			}
			handoff = requireResolvedSessionHandoff(t, handoff)
			if handoff.InitialInput.TransitionInput != tt.initialInput {
				t.Fatalf("transition input = %q, want %q", handoff.InitialInput.TransitionInput, tt.initialInput)
			}
			if handoff.InitialInput.Precedence != tt.wantPrecedence {
				t.Fatalf("initial input precedence = %v, want %v", handoff.InitialInput.Precedence, tt.wantPrecedence)
			}
			if tt.action != UIActionOpenSession {
				return
			}
			parentSessionID := requireSessionOpenDestination(t, handoff)

			initialState, err := sessionLaunchInitialStateFromServer(
				context.Background(),
				server,
				parentSessionID,
				handoff.InitialInput,
			)
			if err != nil {
				t.Fatalf("resolve next-launch initial state: %v", err)
			}
			if initialInputRequest.TransitionInput != tt.initialInput {
				t.Fatalf("next-launch transition input = %q, want %q", initialInputRequest.TransitionInput, tt.initialInput)
			}
			if !initialInputRequest.OverrideStoredDraft {
				t.Fatal("next-launch request did not apply transition-input precedence")
			}
			if initialState.Input != tt.initialInput {
				t.Fatalf("resolved parent input = %q, want %q", initialState.Input, tt.initialInput)
			}
			if len(initialState.RecoveryBuffers) != 0 {
				t.Fatalf("resolved parent recovery buffers = %+v, want none", initialState.RecoveryBuffers)
			}

			runtimeClient := &runtimeControlFakeClient{
				mainView: clientui.RuntimeMainView{
					Session: clientui.RuntimeSessionView{SessionID: parentSessionID},
				},
			}
			initialPrompt, initialPromptHistoryRecorded := handoff.initialPromptFields()
			composition, err := composeUIProgram(uiLoopRequest{
				wiring: &runtimeWiring{
					runtimeClient: runtimeClient,
					runtimeEvents: closedProjectedRuntimeEvents(),
					askEvents:     closedAskEvents(),
				},
				active:                       config.Settings{Theme: "dark"},
				initialPrompt:                initialPrompt,
				initialPromptHistoryRecorded: initialPromptHistoryRecorded,
				initialInput:                 initialState.Input,
				recoveryBuffers:              initialState.RecoveryBuffers,
				statusConfig:                 uiStatusConfig{PersistenceRoot: t.TempDir()},
			}, io.Discard)
			if err != nil {
				t.Fatalf("compose parent UI: %v", err)
			}
			defer composition.close()

			model := composition.model
			if model.input != tt.initialInput {
				t.Fatalf("parent composer input = %q, want %q", model.input, tt.initialInput)
			}
			if model.startupSubmit != "" || model.activeSubmit.text != "" || model.isBusy() {
				t.Fatalf("prefill must stay idle and unsent: startup=%q active=%+v busy=%t", model.startupSubmit, model.activeSubmit, model.isBusy())
			}
			if len(model.recoveredDraftBuffers) != 0 || len(model.pendingInjected) != 0 || len(model.queued) != 0 {
				t.Fatalf("parent recovery state leaked: recovered=%+v pending=%+v queued=%+v", model.recoveredDraftBuffers, model.pendingInjected, model.queued)
			}

			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			edited := next.(*uiModel)
			if cmd != nil {
				t.Fatal("normal composer edit created a command")
			}
			if edited.input != tt.initialInput+"x" {
				t.Fatalf("edited parent input = %q, want %q", edited.input, tt.initialInput+"x")
			}
			if runtimeClient.submitText != "" || runtimeClient.queueUserMessageCalls != 0 || runtimeClient.submitQueuedCalls != 0 {
				t.Fatalf("normal edit submitted runtime work: submit=%q queued=%d flushed=%d", runtimeClient.submitText, runtimeClient.queueUserMessageCalls, runtimeClient.submitQueuedCalls)
			}
		})
	}
}

func TestResolvedSessionHandoffRejectsInvalidWireCombinations(t *testing.T) {
	tests := []struct {
		name     string
		response serverapi.SessionResolveTransitionResponse
	}{
		{
			name: "non-continuing response with destination",
			response: serverapi.SessionResolveTransitionResponse{
				ShouldContinue: false,
				NextSessionID:  "session-1",
			},
		},
		{
			name: "force new with existing destination",
			response: serverapi.SessionResolveTransitionResponse{
				ShouldContinue:  true,
				ForceNewSession: true,
				NextSessionID:   "session-1",
			},
		},
		{
			name: "existing destination with parent",
			response: serverapi.SessionResolveTransitionResponse{
				ShouldContinue:  true,
				NextSessionID:   "session-1",
				ParentSessionID: "parent-1",
			},
		},
		{
			name: "picker destination with parent",
			response: serverapi.SessionResolveTransitionResponse{
				ShouldContinue:  true,
				ParentSessionID: "parent-1",
			},
		},
		{
			name: "history flag without prompt",
			response: serverapi.SessionResolveTransitionResponse{
				ShouldContinue:               true,
				InitialPromptHistoryRecorded: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handoff, err := resolvedSessionHandoffFromResponse(tt.response)
			if err == nil {
				t.Fatalf("invalid response produced handoff %+v", handoff)
			}
		})
	}
}

func TestSessionLaunchInitialStateReturnsLifecycleError(t *testing.T) {
	lookupErr := errors.New("initial input lookup failed")
	server := narrowSessionLifecycleServer{
		lifecycle: &recordingSessionLifecycleClient{
			getInitialInput: func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
				return serverapi.SessionInitialInputResponse{}, lookupErr
			},
		},
	}

	state, err := sessionLaunchInitialStateFromServer(
		context.Background(),
		server,
		"parent-session",
		sessionInitialInputDirective{
			TransitionInput: "child final",
			Precedence:      sessionInitialInputPreferTransition,
		},
	)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("initial state error = %v, want %v", err, lookupErr)
	}
	if state.Input != "" || len(state.RecoveryBuffers) != 0 {
		t.Fatalf("failed initial state lookup returned state %+v", state)
	}
}
