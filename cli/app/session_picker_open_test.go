package app

import (
	"context"
	"errors"
	"testing"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerOpenControllerStub struct {
	inspect  func(context.Context, runtimeids.SessionID) (sessionPickerOpenInspection, error)
	retarget func(context.Context, runtimeids.SessionID, string) error
	plan     func(context.Context, runtimeids.SessionID) (sessionLaunchPlan, error)
}

func successfulSessionPickerOpenController(
	sessionID runtimeids.SessionID,
) sessionPickerOpenController {
	return sessionPickerOpenControllerStub{
		inspect: func(
			context.Context,
			runtimeids.SessionID,
		) (sessionPickerOpenInspection, error) {
			return sessionPickerOpenInspection{}, nil
		},
		plan: func(
			context.Context,
			runtimeids.SessionID,
		) (sessionLaunchPlan, error) {
			return sessionLaunchPlan{SessionID: sessionID.String()}, nil
		},
	}
}

func (s sessionPickerOpenControllerStub) Inspect(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (sessionPickerOpenInspection, error) {
	return s.inspect(ctx, sessionID)
}

func (s sessionPickerOpenControllerStub) Retarget(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	workspaceRoot string,
) error {
	return s.retarget(ctx, sessionID, workspaceRoot)
}

func (s sessionPickerOpenControllerStub) Plan(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (sessionLaunchPlan, error) {
	return s.plan(ctx, sessionID)
}

func TestSessionPickerKeepsSelectedRowMountedWhileOpenIsPending(t *testing.T) {
	sessionID := mustPickerSessionID(t, "pending-open")
	model := newSessionPickerModel(
		context.Background(),
		&recordingSessionPageLoader{},
		"dark",
		sessionPickerHeaderInfo{},
	)
	model.main.replaceSegments(serverapi.SessionPageResponse{
		ProjectID: model.loader.ProjectID(),
		Category:  sessioncontract.SessionCategoryMain,
		Sessions: []clientui.SessionSummary{{
			SessionID: sessionID,
			Category:  sessioncontract.SessionCategoryMain,
		}},
	})
	model.main.selected = newSessionPickerSessionSelection(sessionID)
	model.openController = sessionPickerOpenControllerStub{
		inspect: func(context.Context, runtimeids.SessionID) (sessionPickerOpenInspection, error) {
			t.Fatal("open command ran before the test executed it")
			return sessionPickerOpenInspection{}, nil
		},
	}

	command := pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("Enter did not start the open operation")
	}
	if model.pendingOpen == nil || model.pendingOpen.sessionID != sessionID {
		t.Fatalf("pending open = %+v, want %q", model.pendingOpen, sessionID)
	}
	if model.result != nil {
		t.Fatalf("pending open dismissed picker with result %+v", model.result)
	}
	if model.scheduledSpinnerGeneration == nil {
		t.Fatal("pending open did not schedule the existing spinner")
	}
	viewBeforeTick := model.View()
	generation := *model.scheduledSpinnerGeneration
	pickerUpdateCommand(
		t,
		model,
		sessionPickerSpinnerTickMsg{generation: generation},
	)
	if viewAfterTick := model.View(); viewAfterTick == viewBeforeTick {
		t.Fatal("pending open spinner tick did not change rendered output")
	}
	status := projectStartupPickerStatusRender(
		projectStartupPickerStatus(model.startupStatus),
	)
	if status == nil ||
		status.Kind != startupPickerStatusRenderNotice ||
		status.IsError {
		t.Fatalf("pending open status = %+v, want neutral notice", status)
	}
	selected, ok := model.main.selected.(sessionPickerSessionSelection)
	if !ok || selected.sessionID != sessionID {
		t.Fatalf("selection while pending = %+v, want %q", model.main.selected, sessionID)
	}
	if duplicate := pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyEnter}); duplicate != nil {
		t.Fatal("second Enter started a duplicate open operation")
	}
}

func TestSessionPickerRetriesTypedMaterializationFailureOnSameRow(t *testing.T) {
	for _, test := range []struct {
		name     string
		reason   protocol.SessionEventLogMaterializationReason
		wantKind sessionPickerOpenFailureKind
	}{
		{
			name:     "upgrade required",
			reason:   protocol.SessionEventLogMaterializationUnsupportedVersion,
			wantKind: sessionPickerOpenFailureUpgradeRequired,
		},
		{
			name:     "pre-commit failure",
			reason:   protocol.SessionEventLogMaterializationFailure,
			wantKind: sessionPickerOpenFailureGeneric,
		},
		{
			name:     "insufficient space",
			reason:   protocol.SessionEventLogMaterializationInsufficientSpace,
			wantKind: sessionPickerOpenFailureInsufficientSpace,
		},
		{
			name:     "structural failure",
			reason:   protocol.SessionEventLogMaterializationStructuralFailure,
			wantKind: sessionPickerOpenFailureStructural,
		},
		{
			name:     "committed pending repair",
			reason:   protocol.SessionEventLogMaterializationReconciliationPending,
			wantKind: sessionPickerOpenFailureReconciliationPending,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessionID := mustPickerSessionID(t, "retry-open")
			loader := &recordingSessionPageLoader{}
			model := newSessionPickerModel(
				context.Background(),
				loader,
				"dark",
				sessionPickerHeaderInfo{},
			)
			model.main.replaceSegments(serverapi.SessionPageResponse{
				ProjectID: loader.ProjectID(),
				Category:  sessioncontract.SessionCategoryMain,
				Sessions: []clientui.SessionSummary{{
					SessionID: sessionID,
					Category:  sessioncontract.SessionCategoryMain,
				}},
			})
			model.main.selected = newSessionPickerSessionSelection(sessionID)
			planCalls := 0
			model.openController = sessionPickerOpenControllerStub{
				inspect: func(
					context.Context,
					runtimeids.SessionID,
				) (sessionPickerOpenInspection, error) {
					return sessionPickerOpenInspection{}, nil
				},
				plan: func(
					context.Context,
					runtimeids.SessionID,
				) (sessionLaunchPlan, error) {
					planCalls++
					if planCalls == 1 {
						materialization := &protocol.SessionEventLogMaterializationError{
							Reason: test.reason,
							Stage:  protocol.SessionEventLogMaterializationPreparation,
						}
						if test.reason == protocol.SessionEventLogMaterializationUnsupportedVersion {
							found, supported := 2, 1
							materialization.FoundVersion = &found
							materialization.SupportedVersion = &supported
						}
						if test.reason == protocol.SessionEventLogMaterializationReconciliationPending {
							materialization.Stage = protocol.SessionEventLogMaterializationReconciliation
							materialization.Committed = true
							materialization.PendingRepair = true
						}
						return sessionLaunchPlan{}, materialization
					}
					return sessionLaunchPlan{SessionID: sessionID.String()}, nil
				},
			}

			runSessionPickerCommands(
				t,
				model,
				pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyEnter}),
			)
			if model.result != nil || model.pendingOpen != nil {
				t.Fatalf(
					"failed open state result=%+v pending=%+v",
					model.result,
					model.pendingOpen,
				)
			}
			if model.openFailure == nil ||
				model.openFailure.sessionID != sessionID ||
				model.openFailure.kind != test.wantKind ||
				!errors.As(model.openFailure.diagnostic, new(*protocol.SessionEventLogMaterializationError)) {
				t.Fatalf("typed open failure = %+v", model.openFailure)
			}
			status := projectStartupPickerStatusRender(
				projectStartupPickerStatus(model.startupStatus),
			)
			if status == nil ||
				status.Kind != startupPickerStatusRenderNotice ||
				!status.IsError {
				t.Fatalf("failed open status = %+v, want error notice", status)
			}
			selected, ok := model.main.selected.(sessionPickerSessionSelection)
			if !ok || selected.sessionID != sessionID {
				t.Fatalf("selection after failure = %+v", model.main.selected)
			}

			runSessionPickerCommands(
				t,
				model,
				pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyEnter}),
			)
			open, ok := model.result.(sessionPickerOpenResult)
			if !ok || open.sessionID != sessionID {
				t.Fatalf("successful retry result = %+v", model.result)
			}
			if model.openFailure != nil {
				t.Fatalf("successful retry retained failure %+v", model.openFailure)
			}
			if planCalls != 2 {
				t.Fatalf("plan calls = %d, want failure plus retry", planCalls)
			}
			if calls := loader.snapshotCalls(); len(calls) != 0 {
				t.Fatalf("open retry reloaded session pages: %+v", calls)
			}
		})
	}
}

func TestSessionPickerCtrlCCancelsPendingOpenAndIgnoresLateCompletion(t *testing.T) {
	sessionID := mustPickerSessionID(t, "cancel-open")
	model := newSessionPickerModel(
		context.Background(),
		&recordingSessionPageLoader{},
		"dark",
		sessionPickerHeaderInfo{},
	)
	model.main.replaceSegments(serverapi.SessionPageResponse{
		ProjectID: model.loader.ProjectID(),
		Category:  sessioncontract.SessionCategoryMain,
		Sessions: []clientui.SessionSummary{{
			SessionID: sessionID,
			Category:  sessioncontract.SessionCategoryMain,
		}},
	})
	model.main.selected = newSessionPickerSessionSelection(sessionID)
	model.openController = sessionPickerOpenControllerStub{
		inspect: func(
			context.Context,
			runtimeids.SessionID,
		) (sessionPickerOpenInspection, error) {
			return sessionPickerOpenInspection{}, nil
		},
	}

	openCommand := pickerUpdateCommand(
		t,
		model,
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	pending := *model.pendingOpen
	pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := model.result.(sessionPickerCancelResult); !ok {
		t.Fatalf("Ctrl+C result = %T, want cancel", model.result)
	}
	if model.pendingOpen != nil || model.workspacePrompt != nil {
		t.Fatalf(
			"Ctrl+C retained open state pending=%+v prompt=%+v",
			model.pendingOpen,
			model.workspacePrompt,
		)
	}

	batch, ok := openCommand().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("open command = %T, want batch", openCommand())
	}
	late := batch[0]()
	if command := pickerUpdateCommand(t, model, late); command != nil {
		t.Fatal("late canceled completion scheduled another operation")
	}
	if _, ok := model.result.(sessionPickerCancelResult); !ok {
		t.Fatalf(
			"late generation %d replaced cancel result with %T",
			pending.generation,
			model.result,
		)
	}
}

func TestSessionPickerWorkspaceChangeReturnsToSameSelectedRow(t *testing.T) {
	sessionID := mustPickerSessionID(t, "workspace-open")
	loader := &recordingSessionPageLoader{}
	model := newSessionPickerModel(
		context.Background(),
		loader,
		"dark",
		sessionPickerHeaderInfo{},
	)
	model.main.replaceSegments(serverapi.SessionPageResponse{
		ProjectID: loader.ProjectID(),
		Category:  sessioncontract.SessionCategoryMain,
		Sessions: []clientui.SessionSummary{{
			SessionID: sessionID,
			Category:  sessioncontract.SessionCategoryMain,
		}},
	})
	model.main.selected = newSessionPickerSessionSelection(sessionID)
	retargetCalls := 0
	model.openController = sessionPickerOpenControllerStub{
		inspect: func(
			context.Context,
			runtimeids.SessionID,
		) (sessionPickerOpenInspection, error) {
			return sessionPickerOpenInspection{
				workspaceChange: &sessionPickerWorkspaceChange{
					selectedRoot: "/old",
					currentRoot:  "/new",
				},
			}, nil
		},
		retarget: func(
			_ context.Context,
			gotSessionID runtimeids.SessionID,
			workspaceRoot string,
		) error {
			retargetCalls++
			if gotSessionID != sessionID || workspaceRoot != "/new" {
				t.Fatalf(
					"retarget = (%q, %q), want (%q, /new)",
					gotSessionID,
					workspaceRoot,
					sessionID,
				)
			}
			return nil
		},
		plan: func(
			context.Context,
			runtimeids.SessionID,
		) (sessionLaunchPlan, error) {
			return sessionLaunchPlan{SessionID: sessionID.String()}, nil
		},
	}

	openCommand := pickerUpdateCommand(
		t,
		model,
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	openBatch, ok := openCommand().(tea.BatchMsg)
	if !ok || len(openBatch) == 0 {
		t.Fatalf("workspace open command = %T, want non-empty batch", openCommand())
	}
	runSessionPickerCommands(t, model, openBatch[0])
	if model.workspacePrompt == nil || model.pendingOpen == nil {
		t.Fatalf(
			"workspace prompt state prompt=%+v pending=%+v",
			model.workspacePrompt,
			model.pendingOpen,
		)
	}
	if model.result != nil {
		t.Fatalf("workspace prompt dismissed picker with %+v", model.result)
	}
	if model.scheduledSpinnerGeneration == nil {
		t.Fatal("workspace prompt lost the pending open spinner")
	}
	spinnerGeneration := *model.scheduledSpinnerGeneration
	spinnerFrame := model.spinnerFrame
	pickerUpdateCommand(
		t,
		model,
		sessionPickerSpinnerTickMsg{generation: spinnerGeneration},
	)
	if model.spinnerFrame != spinnerFrame+1 ||
		model.scheduledSpinnerGeneration == nil {
		t.Fatalf(
			"workspace prompt spinner frame=%d scheduled=%v, want frame %d and rescheduled",
			model.spinnerFrame,
			model.scheduledSpinnerGeneration,
			spinnerFrame+1,
		)
	}
	model.width = 40
	model.height = 10
	model.workspacePrompt.width = model.width
	model.workspacePrompt.height = model.height
	if view := model.View(); view == "" {
		t.Fatal("workspace prompt did not render at minimum picker geometry")
	}

	runSessionPickerCommands(
		t,
		model,
		pickerUpdateCommand(
			t,
			model,
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}},
		),
	)
	open, ok := model.result.(sessionPickerOpenResult)
	if !ok || open.sessionID != sessionID {
		t.Fatalf("workspace-confirmed result = %+v", model.result)
	}
	if retargetCalls != 1 {
		t.Fatalf("retarget calls = %d, want 1", retargetCalls)
	}
}

func TestSessionPickerWorkspaceChangeNoClearsPendingAndKeepsSelection(t *testing.T) {
	sessionID := mustPickerSessionID(t, "workspace-decline")
	model := newSessionPickerModel(
		context.Background(),
		&recordingSessionPageLoader{},
		"dark",
		sessionPickerHeaderInfo{},
	)
	model.main.replaceSegments(serverapi.SessionPageResponse{
		ProjectID: model.loader.ProjectID(),
		Category:  sessioncontract.SessionCategoryMain,
		Sessions: []clientui.SessionSummary{{
			SessionID: sessionID,
			Category:  sessioncontract.SessionCategoryMain,
		}},
	})
	model.main.selected = newSessionPickerSessionSelection(sessionID)
	model.openController = sessionPickerOpenControllerStub{
		inspect: func(
			context.Context,
			runtimeids.SessionID,
		) (sessionPickerOpenInspection, error) {
			return sessionPickerOpenInspection{
				workspaceChange: &sessionPickerWorkspaceChange{
					selectedRoot: "/old",
					currentRoot:  "/new",
				},
			}, nil
		},
	}
	openCommand := pickerUpdateCommand(
		t,
		model,
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	openBatch, ok := openCommand().(tea.BatchMsg)
	if !ok || len(openBatch) == 0 {
		t.Fatalf("workspace open command = %T, want non-empty batch", openCommand())
	}
	runSessionPickerCommands(t, model, openBatch[0])
	runSessionPickerCommands(
		t,
		model,
		pickerUpdateCommand(
			t,
			model,
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}},
		),
	)
	if model.result != nil ||
		model.pendingOpen != nil ||
		model.workspacePrompt != nil ||
		model.openFailure != nil {
		t.Fatalf(
			"decline state result=%+v pending=%+v prompt=%+v failure=%+v",
			model.result,
			model.pendingOpen,
			model.workspacePrompt,
			model.openFailure,
		)
	}
	selected, ok := model.main.selected.(sessionPickerSessionSelection)
	if !ok || selected.sessionID != sessionID {
		t.Fatalf("selection after decline = %+v, want %q", model.main.selected, sessionID)
	}
}
