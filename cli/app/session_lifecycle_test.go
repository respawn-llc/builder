package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/metadata"
	"core/server/session"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

func sessionLifecycleStringPtr(value string) *string { return &value }

func sessionLifecycleSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return sessionID
}

func createAttachedAuthoritativeAppSession(t *testing.T, persistenceRoot, projectID, workspaceRoot string) *session.Store {
	t.Helper()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	if _, err := metadataStore.AttachWorkspaceToProject(context.Background(), projectID, workspaceRoot); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	store, err := session.Create(
		filepath.Join(persistenceRoot, "projects", projectID, "sessions"),
		filepath.Base(filepath.Clean(workspaceRoot)),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		_ = metadataStore.Close()
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("EnsureDurable: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	return store
}

func TestMaybeHandlePickedSessionWorkspaceChangeCanonicalizesAliases(t *testing.T) {
	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	action, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		&remoteAppServer{
			cfg:      config.App{WorkspaceRoot: aliasRoot, Settings: config.Settings{Theme: "dark"}},
			retarget: &sessionWorkspaceRetargetContext{workspaceRoot: aliasRoot, theme: "dark"},
		},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         realRoot,
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err != nil {
		t.Fatalf("maybeHandlePickedSessionWorkspaceChange: %v", err)
	}
	if action != sessionWorkspaceChangeProceed || promptCalls != 0 {
		t.Fatalf("action/prompt calls = %v/%d, want proceed/0", action, promptCalls)
	}
}

func TestMaybeHandlePickedSessionWorkspaceChangeUsesRemoteServerBindingRoot(t *testing.T) {
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	var promptedCurrentRoot string
	runWorkspaceChangePromptFlow = func(_ string, currentRoot string, _ string) (workspaceChangePromptResult, error) {
		promptedCurrentRoot = currentRoot
		return workspaceChangePromptResult{}, nil
	}

	action, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		&remoteAppServer{
			cfg:      config.App{WorkspaceRoot: "/source-client-workspace", Settings: config.Settings{Theme: "dark"}},
			retarget: &sessionWorkspaceRetargetContext{workspaceRoot: "/active-server-workspace", theme: "dark"},
		},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/target-server-workspace",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err != nil {
		t.Fatalf("maybeHandlePickedSessionWorkspaceChange: %v", err)
	}
	if action != sessionWorkspaceChangePickAgain || promptedCurrentRoot != "/active-server-workspace" {
		t.Fatalf("action/current root = %v/%q", action, promptedCurrentRoot)
	}
}

func TestMaybeHandlePickedSessionWorkspaceChangeRejectsMissingBindingContext(t *testing.T) {
	_, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		narrowSessionLifecycleServer{},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/target-server-workspace",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err == nil {
		t.Fatal("expected missing workspace retarget context error")
	}
}

func TestResolveSessionActionPreservesInitialPromptHistoryRecorded(t *testing.T) {
	client := &recordingSessionLifecycleClient{
		resolveTransition: func(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
			if !req.Transition.InitialPromptHistoryRecorded {
				t.Fatal("expected transition request to preserve initial prompt-history flag")
			}
			prompt := serverapi.SessionInitialPromptMetadata{
				Text:            req.Transition.InitialPrompt,
				HistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
			}
			return serverapi.LaunchSessionDirective(
				serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
				serverapi.NewSessionLaunchPreparation(
					&prompt,
					serverapi.RestoreStoredDraftSessionDraftDisposition(),
					serverapi.SessionAuthPreparationKeepCurrent,
				),
			), nil
		},
	}

	resolved, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{lifecycle: client},
		nil,
		"session-1",
		UITransition{Action: UIActionNewSession, InitialPrompt: "expanded prompt", InitialPromptHistoryRecorded: true},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	preparation, present := resolved.LaunchPreparation()
	if !present {
		t.Fatal("resolved transition omitted launch preparation")
	}
	prompt, present := preparation.InitialPrompt()
	if !present || !prompt.HistoryRecorded {
		t.Fatal("expected resolved transition to preserve initial prompt-history flag")
	}
}

func TestPersistSessionDraftIncludesStructuredRecoveryBuffers(t *testing.T) {
	var captured serverapi.SessionPersistInputDraftRequest
	client := &recordingSessionLifecycleClient{
		persistInputDraft: func(_ context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
			captured = req
			return serverapi.SessionPersistInputDraftResponse{}, nil
		},
	}
	model := newUIModelDefaults(nil)
	testSetMainInput(model, "visible draft")
	model.activeSubmit = activeSubmitState{token: 1, text: "submitting now"}
	model.pendingInjected = queuedUserMessagesForTest("  pending injected\n")
	model.queued = queuedInputsForTest("\tqueued later  ")

	if err := persistSessionDraftToServer(context.Background(), narrowSessionLifecycleServer{lifecycle: client}, " session-1 ", model); err != nil {
		t.Fatalf("persistSessionDraftToServer: %v", err)
	}
	want := []serverapi.SessionDraftRecoveryBuffer{
		{Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit, Text: "submitting now"},
		{Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput, Text: "  pending injected\n"},
		{Kind: serverapi.SessionDraftRecoveryBufferQueuedInput, Text: "\tqueued later  "},
	}
	if captured.Input != "visible draft" || captured.SessionID != "session-1" || !reflect.DeepEqual(captured.RecoveryBuffers, want) {
		t.Fatalf("captured draft request = %+v, want buffers %+v", captured, want)
	}
	if _, err := json.Marshal(captured); err != nil {
		t.Fatalf("marshal captured draft request: %v", err)
	}
}

func TestInitialRecoveryBuffersRestoreRetryAffordancesWithoutStartupSubmit(t *testing.T) {
	model := NewProjectedUIModel(nil,
		WithUIInitialInput("visible draft"),
		WithUIInitialRecoveryBuffers([]serverapi.SessionDraftRecoveryBuffer{
			{Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit, Text: "submitted before forced exit"},
			{Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput, Text: "pending steering"},
			{Kind: serverapi.SessionDraftRecoveryBufferQueuedInput, Text: "queued later"},
		}),
	).(*uiModel)

	if got := testMainInput(model); got != "visible draft\n\nsubmitted before forced exit\n\npending steering\n\nqueued later" {
		t.Fatalf("input = %q, want recovered visible retry input", got)
	}
	if model.startupSubmit != "" || model.activeSubmit.text != "" || len(model.pendingInjected) != 0 || len(model.queued) != 0 {
		t.Fatalf("recovery restored operational submission state: startup=%q active=%+v pending=%+v queued=%+v", model.startupSubmit, model.activeSubmit, model.pendingInjected, model.queued)
	}
	if len(model.recoveredDraftBuffers) != 3 || model.transientStatus != "" {
		t.Fatalf("recovered buffers/status = %+v/%q", model.recoveredDraftBuffers, model.transientStatus)
	}
}

func TestSubmissionPersistsActiveDraftBeforeRuntimeDispatch(t *testing.T) {
	for _, test := range []struct {
		name      string
		text      string
		wantShell bool
	}{
		{name: "user turn", text: "submit me"},
		{name: "shell", text: "$ echo hello", wantShell: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &runtimeControlFakeClient{}
			model := newProjectedClosedUIModel(runtimeClient)
			model.sessionID = "session-1"
			var captured serverapi.SessionPersistInputDraftRequest
			model.sessionDrafts = &recordingSessionLifecycleClient{
				persistInputDraft: func(_ context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
					if runtimeClient.submitCalls != 0 || runtimeClient.shellCalls != 0 {
						t.Fatal("RuntimeControl dispatch ran before active draft persistence")
					}
					captured = req
					return serverapi.SessionPersistInputDraftResponse{}, nil
				},
			}
			testSetMainInput(model, test.text)
			model.clearInput()

			prepareCmd := model.inputController().startSubmissionWithPromptHistoryAndQueuePositionAndID(test.text, preSubmitQueueBack, "")
			if got := testMainInput(model); got != test.text {
				t.Fatalf("input while active draft persists = %q, want %q", got, test.text)
			}
			prepared := findSubmitDraftPreparedMessage(t, prepareCmd)
			wantRecovery := []serverapi.SessionDraftRecoveryBuffer{{
				Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit,
				Text: test.text,
			}}
			if captured.Input != "" || !reflect.DeepEqual(captured.RecoveryBuffers, wantRecovery) {
				t.Fatalf("persisted active draft = %+v, want input empty and recovery %+v", captured, wantRecovery)
			}
			if runtimeClient.submitCalls != 0 || runtimeClient.shellCalls != 0 {
				t.Fatal("RuntimeControl dispatch ran before draft-prepared message was reduced")
			}

			next, dispatchCmd := model.Update(prepared)
			updated := next.(*uiModel)
			if got := testMainInput(updated); got != "" {
				t.Fatalf("input after active draft persisted = %q, want cleared", got)
			}
			collectCmdMessages(t, dispatchCmd)
			if test.wantShell {
				if runtimeClient.shellCalls != 1 || runtimeClient.submitCalls != 0 {
					t.Fatalf("runtime calls after shell preparation = submit:%d shell:%d", runtimeClient.submitCalls, runtimeClient.shellCalls)
				}
			} else if runtimeClient.submitCalls != 1 || runtimeClient.shellCalls != 0 {
				t.Fatalf("runtime calls after turn preparation = submit:%d shell:%d", runtimeClient.submitCalls, runtimeClient.shellCalls)
			}
		})
	}
}

func TestSubmissionDraftPersistenceFailureKeepsTextEditableWithoutRuntimeDispatch(t *testing.T) {
	persistErr := errors.New("draft persistence failed")
	for _, text := range []string{"submit me", "$ echo hello"} {
		t.Run(text, func(t *testing.T) {
			runtimeClient := &runtimeControlFakeClient{}
			model := newProjectedClosedUIModel(runtimeClient)
			model.sessionID = "session-1"
			model.sessionDrafts = &recordingSessionLifecycleClient{
				persistInputDraft: func(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
					return serverapi.SessionPersistInputDraftResponse{}, persistErr
				},
			}
			testSetMainInput(model, text)
			model.clearInput()

			prepared := findSubmitDraftPreparedMessage(t, model.inputController().startSubmissionWithPromptHistoryAndQueuePositionAndID(text, preSubmitQueueBack, ""))
			next, cmd := model.Update(prepared)
			updated := next.(*uiModel)
			collectCmdMessages(t, cmd)

			if runtimeClient.submitCalls != 0 || runtimeClient.shellCalls != 0 {
				t.Fatalf("RuntimeControl calls after draft failure = submit:%d shell:%d", runtimeClient.submitCalls, runtimeClient.shellCalls)
			}
			if got := testMainInput(updated); got != text {
				t.Fatalf("editable input after draft failure = %q, want %q", got, text)
			}
			if updated.activeSubmit.token != 0 {
				t.Fatalf("active submit remained after draft failure: %+v", updated.activeSubmit)
			}
			if updated.activity != uiActivityError {
				t.Fatalf("activity after draft failure = %v, want error", updated.activity)
			}
		})
	}
}

func TestSubmissionDraftCompletionPreservesConcurrentComposerEdit(t *testing.T) {
	persistErr := errors.New("draft persistence failed")
	for _, test := range []struct {
		name           string
		persistErr     error
		wantDispatches int
	}{
		{name: "success", wantDispatches: 1},
		{name: "failure", persistErr: persistErr},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &runtimeControlFakeClient{}
			model := newProjectedClosedUIModel(runtimeClient)
			model.sessionID = "session-1"
			var persisted []serverapi.SessionPersistInputDraftRequest
			model.sessionDrafts = &recordingSessionLifecycleClient{
				persistInputDraft: func(_ context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
					persisted = append(persisted, req)
					return serverapi.SessionPersistInputDraftResponse{}, test.persistErr
				},
			}
			testSetMainInput(model, "submit me")
			model.clearInput()

			prepared := findSubmitDraftPreparedMessage(t, model.inputController().startSubmissionWithPromptHistoryAndQueuePositionAndID("submit me", preSubmitQueueBack, ""))
			model.replaceMainInputAtEnd("edited while waiting")
			next, cmd := model.Update(prepared)
			updated := next.(*uiModel)
			if test.persistErr == nil {
				if runtimeClient.submitCalls != 0 {
					t.Fatal("RuntimeControl dispatched before the concurrent edit was persisted")
				}
				reconciled := findSubmitDraftPreparedMessage(t, cmd)
				if len(persisted) != 2 || persisted[1].Input != "edited while waiting" {
					t.Fatalf("reconciled draft requests = %+v, want second request with concurrent edit", persisted)
				}
				next, cmd = updated.Update(reconciled)
				updated = next.(*uiModel)
			}
			collectCmdMessages(t, cmd)

			if got := testMainInput(updated); got != "edited while waiting" {
				t.Fatalf("composer after draft completion = %q, want concurrent edit", got)
			}
			if got := runtimeClient.submitCalls; got != test.wantDispatches {
				t.Fatalf("RuntimeControl dispatch count = %d, want %d", got, test.wantDispatches)
			}
		})
	}
}

func findSubmitDraftPreparedMessage(t *testing.T, cmd tea.Cmd) submitDoneMsg {
	t.Helper()
	for _, msg := range collectCmdMessages(t, cmd) {
		if prepared, ok := msg.(submitDoneMsg); ok && prepared.phase == submitPhaseDraftPrepared {
			return prepared
		}
	}
	t.Fatal("submit command did not produce a draft-prepared message")
	return submitDoneMsg{}
}

func TestRuntimeReleaseUsesFinalModelPolicyAndPreservesErrors(t *testing.T) {
	t.Run("normal release after UI loop", func(t *testing.T) {
		releaseErr := errors.New("release failed")
		uiLoopReturned := false
		plan := &runtimeLaunchPlan{close: func() error {
			if !uiLoopReturned {
				t.Fatal("runtime release ran before UI loop returned")
			}
			return releaseErr
		}}
		uiLoopReturned = true
		if err := closeRuntimePlanAfterUIExit(plan, &uiModel{}); !errors.Is(err, releaseErr) {
			t.Fatalf("close error = %v, want release failure", err)
		}
	})

	t.Run("forced exit detaches and joins draft error", func(t *testing.T) {
		persistErr := errors.New("persist draft failed")
		detachErr := errors.New("detach failed")
		plan := &runtimeLaunchPlan{
			close:       func() error { t.Fatal("normal close must not run"); return nil },
			detachClose: func() error { return detachErr },
		}
		err := releaseRuntimePlanAfterUIResult(plan, projectionFailureFinalModel(t), persistErr)
		if !errors.Is(err, persistErr) || !errors.Is(err, detachErr) {
			t.Fatalf("release error = %v, want persistence and detach failures", err)
		}
	})
}

type narrowSessionLifecycleServer struct {
	lifecycle      apicontract.SessionLifecycleService
	cfg            config.App
	reauthenticate func(context.Context, authInteractor) error
}

func (s narrowSessionLifecycleServer) SessionLifecycleClient() apicontract.SessionLifecycleService {
	return s.lifecycle
}

func (s narrowSessionLifecycleServer) Config() config.App { return s.cfg }

func (s narrowSessionLifecycleServer) Reauthenticate(ctx context.Context, interactor authInteractor, _ bool) error {
	if s.reauthenticate == nil {
		return nil
	}
	return s.reauthenticate(ctx, interactor)
}

type recordingSessionLifecycleClient struct {
	getInitialInput          func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error)
	persistInputDraft        func(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error)
	retargetSessionWorkspace func(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error)
	resolveTransition        func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error)
}

func (c *recordingSessionLifecycleClient) Close() error { return nil }

func (c *recordingSessionLifecycleClient) GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	if c.getInitialInput == nil {
		return serverapi.SessionInitialInputResponse{}, errors.New("unexpected GetInitialInput call")
	}
	return c.getInitialInput(ctx, req)
}

func (c *recordingSessionLifecycleClient) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	if c.persistInputDraft == nil {
		return serverapi.SessionPersistInputDraftResponse{}, errors.New("unexpected PersistInputDraft call")
	}
	return c.persistInputDraft(ctx, req)
}

func (c *recordingSessionLifecycleClient) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	if c.retargetSessionWorkspace == nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, errors.New("unexpected RetargetSessionWorkspace call")
	}
	return c.retargetSessionWorkspace(ctx, req)
}

func (c *recordingSessionLifecycleClient) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	if c.resolveTransition == nil {
		return serverapi.SessionResolveTransitionResponse{}, errors.New("unexpected ResolveTransition call")
	}
	return c.resolveTransition(ctx, req)
}
