package app

import (
	"context"
	"io"
	"testing"

	"core/server/llm"
	serverstartup "core/server/startup"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type backParentPrefillScenarioServer interface {
	interactiveSessionServer
	ProjectID() string
	SessionViewClient() client.SessionViewClient
}

func TestBackParentPrefillTransportParity(t *testing.T) {
	t.Run("embedded loopback", func(t *testing.T) {
		_, workspace := newRegisteredAppWorkspace(t)
		server, err := startEmbeddedServer(context.Background(), Options{
			WorkspaceRoot:         workspace,
			WorkspaceRootExplicit: true,
			Model:                 "gpt-5",
		}, readyMemoryAuthHandler(), false)
		if err != nil {
			t.Fatalf("start embedded server: %v", err)
		}
		defer func() { _ = server.Close() }()

		runBackParentPrefillScenario(t, server)
	})

	t.Run("served remote", func(t *testing.T) {
		_, workspace := newRegisteredAppWorkspace(t)
		srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
			WorkspaceRoot:         workspace,
			WorkspaceRootExplicit: true,
			Model:                 "gpt-5",
		}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
		if err != nil {
			t.Fatalf("start served app server: %v", err)
		}
		defer func() { _ = srv.Close() }()

		stopServing := serveAppServer(t, srv)
		defer stopServing()
		waitForConfiguredRemoteIdentity(t, workspace)

		server, err := startSessionServer(
			context.Background(),
			Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true},
			newHeadlessAuthInteractor(),
			false,
		)
		if err != nil {
			t.Fatalf("start remote session server: %v", err)
		}
		defer func() { _ = server.Close() }()
		remote, ok := server.(*remoteAppServer)
		if !ok {
			t.Fatalf("session server = %T, want remote app server", server)
		}
		boundServer, err := ensureInteractiveProjectBinding(context.Background(), remote)
		if err != nil {
			t.Fatalf("bind remote server to project workspace: %v", err)
		}
		boundRemote, ok := boundServer.(*remoteAppServer)
		if !ok {
			t.Fatalf("bound session server = %T, want remote app server", boundServer)
		}
		defer func() { _ = boundRemote.Close() }()

		runBackParentPrefillScenario(t, boundRemote)
	})
}

func runBackParentPrefillScenario(t *testing.T, server backParentPrefillScenarioServer) {
	t.Helper()

	exactFinal := " \nExact café 👩🏽‍💻\n尾  "
	tests := []struct {
		name        string
		finalAnswer *string
	}{
		{
			name:        "exact durable final",
			finalAnswer: &exactFinal,
		},
		{
			name: "absent durable final",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := createAttachedAuthoritativeAppSession(t, server.Config().PersistenceRoot, server.ProjectID(), server.Config().WorkspaceRoot)
			child := createAttachedAuthoritativeAppSession(t, server.Config().PersistenceRoot, server.ProjectID(), server.Config().WorkspaceRoot)
			if err := child.SetParentSessionID(parent.Meta().SessionID); err != nil {
				t.Fatalf("link child to parent: %v", err)
			}
			if _, _, err := child.AppendEvent("child-step", "message", llm.Message{
				Role:    llm.RoleUser,
				Content: "child task",
			}); err != nil {
				t.Fatalf("append child user message: %v", err)
			}
			if tt.finalAnswer != nil {
				if _, _, err := child.AppendEvent("child-step", "message", llm.Message{
					Role:    llm.RoleAssistant,
					Phase:   llm.MessagePhaseFinal,
					Content: *tt.finalAnswer,
				}); err != nil {
					t.Fatalf("append child final answer: %v", err)
				}
			}

			_, err := server.SessionLifecycleClient().PersistInputDraft(
				context.Background(),
				serverapi.SessionPersistInputDraftRequest{
					ClientRequestID: uuid.NewString(),
					SessionID:       parent.Meta().SessionID,
					Input:           "conflicting parent draft",
					RecoveryBuffers: []serverapi.SessionDraftRecoveryBuffer{
						{
							Kind:            serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
							ID:              "pending-parent-input",
							ClientRequestID: "pending-parent-request",
							Text:            "conflicting pending input",
						},
						{
							Kind: serverapi.SessionDraftRecoveryBufferQueuedInput,
							ID:   "queued-parent-input",
							Text: "conflicting queued input",
						},
					},
				},
			)
			if err != nil {
				t.Fatalf("persist conflicting parent draft: %v", err)
			}

			childView, err := server.SessionViewClient().GetSessionMainView(
				context.Background(),
				serverapi.SessionMainViewRequest{SessionID: child.Meta().SessionID},
			)
			if err != nil {
				t.Fatalf("load child main view: %v", err)
			}
			childRuntime := &runtimeControlFakeClient{
				mainView: clientui.RuntimeMainView{
					Status:  childView.MainView.Status,
					Session: childView.MainView.Session,
				},
			}
			childModel := newProjectedClosedUIModel(childRuntime, WithUISessionID(child.Meta().SessionID))
			childModel.statusConfig.SessionViews = server.SessionViewClient()

			next, lookupCmd := childModel.inputController().handleBackCommand()
			childModel = next.(*uiModel)
			if lookupCmd == nil || childModel.finalAnswerOperation == nil {
				t.Fatal("/back did not start the final-answer lookup")
			}
			lookupBatchMsg := lookupCmd()
			batch, ok := lookupBatchMsg.(tea.BatchMsg)
			if !ok || len(batch) == 0 {
				t.Fatalf("/back command result = %T, want lookup batch", lookupBatchMsg)
			}
			firstLookupMsg := batch[0]()
			lookupDone, ok := firstLookupMsg.(latestFinalAnswerDoneMsg)
			if !ok {
				t.Fatalf("first /back operation result = %T, want final-answer lookup result", firstLookupMsg)
			}
			next, quitCmd := childModel.Update(lookupDone)
			childModel = next.(*uiModel)
			if quitCmd == nil {
				t.Fatal("successful /back lookup did not request child UI exit")
			}
			transition := childModel.Transition()
			if transition.Action != UIActionOpenSession || transition.TargetSessionID != parent.Meta().SessionID {
				t.Fatalf("/back transition = %+v, want open parent", transition)
			}
			wantInput := ""
			if tt.finalAnswer != nil {
				wantInput = *tt.finalAnswer
			}
			if transition.InitialInput != wantInput {
				t.Fatalf("/back transition input = %q, want %q", transition.InitialInput, wantInput)
			}

			originReleased := false
			handoff, err := resolveAndReleaseSessionHandoff(
				context.Background(),
				server,
				nil,
				child.Meta().SessionID,
				transition,
				&runtimeLaunchPlan{close: func() error {
					originReleased = true
					return nil
				}},
			)
			if err != nil {
				t.Fatalf("resolve and release child handoff: %v", err)
			}
			if !originReleased {
				t.Fatal("child runtime was not released before parent launch")
			}
			parentSessionID := requireSessionOpenDestination(t, handoff)
			if parentSessionID != parent.Meta().SessionID || handoff.InitialInput.TransitionInput != wantInput {
				t.Fatalf("resolved handoff = %+v, want parent with exact transition input", handoff)
			}

			planner := newSessionLaunchPlanner(server)
			parentPlan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{
				Mode:        launchModeInteractive,
				Destination: sessionOpenDestinationForTest(t, parentSessionID),
			})
			if err != nil {
				t.Fatalf("plan parent session: %v", err)
			}
			parentRuntimePlan, request, err := prepareSessionUIRun(
				context.Background(),
				server,
				planner,
				parentPlan,
				*handoff,
				false,
			)
			if err != nil {
				t.Fatalf("prepare parent UI: %v", err)
			}
			defer func() { _ = parentRuntimePlan.Close() }()

			composition, err := composeUIProgram(request, io.Discard)
			if err != nil {
				t.Fatalf("compose parent UI: %v", err)
			}
			defer composition.close()
			parentModel := composition.model

			if parentModel.input != wantInput {
				t.Fatalf("parent composer input = %q, want %q", parentModel.input, wantInput)
			}
			if parentModel.startupSubmit != "" || parentModel.activeSubmit.text != "" || parentModel.isBusy() {
				t.Fatalf("parent prefill submitted on startup: startup=%q active=%+v busy=%t", parentModel.startupSubmit, parentModel.activeSubmit, parentModel.isBusy())
			}
			if len(parentModel.recoveredDraftBuffers) != 0 || len(parentModel.pendingInjected) != 0 || len(parentModel.queued) != 0 {
				t.Fatalf("parent draft recovery leaked: recovered=%+v pending=%+v queued=%+v", parentModel.recoveredDraftBuffers, parentModel.pendingInjected, parentModel.queued)
			}

			beforeEdit, err := parentRuntimePlan.Wiring.runtimeClient.RefreshMainView()
			if err != nil {
				t.Fatalf("refresh parent runtime before edit: %v", err)
			}
			next, editCmd := parentModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			edited := next.(*uiModel)
			if editCmd != nil {
				t.Fatal("normal parent composer edit created a runtime command")
			}
			if edited.input != wantInput+"x" {
				t.Fatalf("edited parent input = %q, want %q", edited.input, wantInput+"x")
			}
			afterEdit, err := parentRuntimePlan.Wiring.runtimeClient.RefreshMainView()
			if err != nil {
				t.Fatalf("refresh parent runtime after edit: %v", err)
			}
			if afterEdit.Activity != beforeEdit.Activity || afterEdit.Activity.State != clientui.RuntimeActivityRegisteredIdle {
				t.Fatalf("normal edit changed parent runtime activity: before=%+v after=%+v", beforeEdit.Activity, afterEdit.Activity)
			}
			hasQueuedWork, err := parentRuntimePlan.Wiring.runtimeClient.HasQueuedUserWork()
			if err != nil {
				t.Fatalf("read parent queued work after edit: %v", err)
			}
			if hasQueuedWork {
				t.Fatal("normal parent composer edit created queued runtime work")
			}
		})
	}
}
