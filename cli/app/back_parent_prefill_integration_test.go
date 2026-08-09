package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"core/server/session"
	serverstartup "core/server/startup"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type backParentPrefillScenarioServer interface {
	interactiveSessionServer
	ProjectID() string
	SessionViewClient() apicontract.SessionViewService
}

func appStringPointer(value string) *string {
	return &value
}

func TestBackParentPrefillOverServedRemote(t *testing.T) {
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
	bound, err := ensureInteractiveProjectBinding(context.Background(), remote)
	if err != nil {
		t.Fatalf("bind remote server to project workspace: %v", err)
	}
	defer func() { _ = bound.Close() }()
	runBackParentPrefillScenario(t, bound.(backParentPrefillScenarioServer))
}

func TestRemoteBackRebindsToParentProjectBeforeRuntimePreparation(t *testing.T) {
	_, workspaceA := newRegisteredAppWorkspace(t)
	workspaceB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspaceB, config.ConfigDirName), 0o755); err != nil {
		t.Fatalf("create target workspace config dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(workspaceB, config.ConfigDirName, "config.toml"),
		[]byte("model = \"target-project-model\"\nprovider_override = \"openai\"\nthinking_level = \"high\"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write target workspace config: %v", err)
	}
	sourceConfig, err := config.Load(workspaceA, config.LoadOptions{})
	if err != nil {
		t.Fatalf("load source config: %v", err)
	}
	bindingB := mustRegisterAppBinding(t, sourceConfig.PersistenceRoot, workspaceB)

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspaceA,
		WorkspaceRootExplicit: true,
		Model:                 "source-project-model",
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("start served app server: %v", err)
	}
	defer func() { _ = srv.Close() }()
	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspaceA)

	server, err := startSessionServer(
		context.Background(),
		Options{WorkspaceRoot: workspaceA, WorkspaceRootExplicit: true},
		newHeadlessAuthInteractor(),
		false,
	)
	if err != nil {
		t.Fatalf("start remote session server: %v", err)
	}
	defer func() { _ = server.Close() }()
	boundServer, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("bind source project: %v", err)
	}
	sourceServer := boundServer.(*remoteAppServer)

	parent := createAttachedAuthoritativeAppSession(t, sourceServer.Config().PersistenceRoot, sourceServer.ProjectID(), workspaceA)
	if err := parent.SetInputDraft("target project draft"); err != nil {
		t.Fatalf("set target parent draft: %v", err)
	}
	parentLog, err := parent.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize parent event log: %v", err)
	}
	child, err := session.CloneSession(parentLog, "", sessioncontract.SessionCategoryMain)
	if err != nil {
		t.Fatalf("clone source child: %v", err)
	}
	childLog, err := child.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize child event log: %v", err)
	}
	childText := "child task"
	if _, _, err := childLog.AppendRecord(
		appStringPointer("child-step"),
		session.MessageRecord{Role: session.MessageRoleUser, Content: &childText},
	); err != nil {
		t.Fatalf("append child event: %v", err)
	}
	targetProjectID := bindingB.ProjectID
	if _, err := sourceServer.SessionLifecycleClient().RetargetSessionWorkspace(
		context.Background(),
		serverapi.SessionRetargetWorkspaceRequest{
			ClientRequestID: uuid.NewString(),
			SessionID:       parent.Meta().SessionID,
			WorkspaceRoot:   workspaceB,
			ProjectID:       &targetProjectID,
		},
	); err != nil {
		t.Fatalf("move parent to target project: %v", err)
	}

	childView, err := sourceServer.SessionViewClient().GetSessionMainView(
		context.Background(),
		serverapi.SessionMainViewRequest{SessionID: child.Meta().SessionID},
	)
	if err != nil {
		t.Fatalf("load child main view: %v", err)
	}
	childModel := newProjectedClosedUIModel(&runtimeControlFakeClient{
		mainView: clientui.RuntimeMainView{Status: childView.MainView.Status, Session: childView.MainView.Session},
	}, WithUISessionID(child.Meta().SessionID))
	childModel.statusConfig.SessionViews = sourceServer.SessionViewClient()
	next, lookupCmd := childModel.inputController().handleBackCommand()
	childModel = next.(*uiModel)
	batch := lookupCmd().(tea.BatchMsg)
	lookupDone := batch[0]().(latestFinalAnswerDoneMsg)
	next, _ = childModel.Update(lookupDone)
	transition := next.(*uiModel).Transition()
	handoff, err := resolveSessionAction(context.Background(), sourceServer, nil, child.Meta().SessionID, transition)
	if err != nil {
		t.Fatalf("resolve /back transition: %v", err)
	}
	intent, _ := requireAppLifecycleLaunch(t, handoff)
	preparation, _ := handoff.LaunchPreparation()
	navigationBinding, present := preparation.NavigationBinding()
	if !present || navigationBinding.ProjectID != bindingB.ProjectID || navigationBinding.WorkspaceID != bindingB.WorkspaceID {
		t.Fatalf("remote /back navigation binding = %+v/%t, want project=%q workspace=%q", navigationBinding, present, bindingB.ProjectID, bindingB.WorkspaceID)
	}

	targetServer, rebound, err := bindNavigationSessionContext(context.Background(), sourceServer, preparation)
	if err != nil {
		t.Fatalf("bind target session context: %v", err)
	}
	if !rebound || targetServer.ProjectID() != bindingB.ProjectID {
		t.Fatalf("target server context = rebound %t project %q, want project %q", rebound, targetServer.ProjectID(), bindingB.ProjectID)
	}
	defer func() { _ = targetServer.Close() }()
	if targetServer.Config().WorkspaceRoot != sourceConfig.WorkspaceRoot {
		t.Fatalf("remote bootstrap workspace root = %q, want retained source root %q", targetServer.Config().WorkspaceRoot, sourceConfig.WorkspaceRoot)
	}
	remoteRetargetContext := targetServer.(sessionWorkspaceRetargetContextProvider).workspaceRetargetContext()
	if remoteRetargetContext == nil || comparableWorkspaceChangeRoot(remoteRetargetContext.workspaceRoot) != comparableWorkspaceChangeRoot(workspaceB) {
		t.Fatalf("remote binding retarget root = %+v, want %q", remoteRetargetContext, workspaceB)
	}

	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}
	workspaceChangeAction, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		targetServer,
		parent.Meta().SessionID,
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         workspaceB,
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err != nil {
		t.Fatalf("handle target picker selection after /back: %v", err)
	}
	if workspaceChangeAction != sessionWorkspaceChangeProceed {
		t.Fatalf("target picker action after /back = %v, want proceed", workspaceChangeAction)
	}
	if promptCalls != 0 {
		t.Fatalf("workspace-change prompts after remote /back = %d, want 0", promptCalls)
	}
	planner := newSessionLaunchPlanner(targetServer)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, Intent: intent})
	if err != nil {
		t.Fatalf("plan target parent: %v", err)
	}
	if plan.ActiveSettings.Model != "target-project-model" || plan.Source.Sources["model"] != "file" {
		t.Fatalf("target plan model/source = %q/%q, want target-project-model/file", plan.ActiveSettings.Model, plan.Source.Sources["model"])
	}
	runtimePlan, request, err := prepareSessionUIRun(
		context.Background(),
		targetServer,
		planner,
		plan,
		"",
		false,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("prepare target parent UI: %v", err)
	}
	defer func() { _ = runtimePlan.Close() }()
	if request.initialInput != "target project draft" || request.active.Model != "target-project-model" {
		t.Fatalf("prepared target UI input/model = %q/%q", request.initialInput, request.active.Model)
	}
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
			parentLog, err := parent.MaterializeEventLog()
			if err != nil {
				t.Fatalf("materialize parent event log: %v", err)
			}
			child, err := session.CloneSession(parentLog, "", sessioncontract.SessionCategoryMain)
			if err != nil {
				t.Fatalf("clone child from parent: %v", err)
			}
			childLog, err := child.MaterializeEventLog()
			if err != nil {
				t.Fatalf("materialize child event log: %v", err)
			}
			childText := "child task"
			if _, _, err := childLog.AppendRecord(
				appStringPointer("child-step"),
				session.MessageRecord{Role: session.MessageRoleUser, Content: &childText},
			); err != nil {
				t.Fatalf("append child user message: %v", err)
			}
			if tt.finalAnswer != nil {
				finalText := *tt.finalAnswer
				finalPhase := session.MessagePhaseFinal
				if _, _, err := childLog.AppendRecord(
					appStringPointer("child-step"),
					session.MessageRecord{
						Role:    session.MessageRoleAssistant,
						Phase:   &finalPhase,
						Content: &finalText,
					},
				); err != nil {
					t.Fatalf("append child final answer: %v", err)
				}
			}

			_, err = server.SessionLifecycleClient().PersistInputDraft(
				context.Background(),
				serverapi.SessionPersistInputDraftRequest{
					ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
					SessionID:       parent.Meta().SessionID,
					Input:           "conflicting parent draft",
					RecoveryBuffers: []serverapi.SessionDraftRecoveryBuffer{
						{
							Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
							Text: "conflicting pending input",
						},
						{
							Kind: serverapi.SessionDraftRecoveryBufferQueuedInput,
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
			wantInput := "conflicting parent draft"
			if tt.finalAnswer != nil {
				wantInput = *tt.finalAnswer
			}
			if !textutil.EqualOptional(transition.InitialInput, tt.finalAnswer) {
				t.Fatalf("/back transition input = %v, want %v", transition.InitialInput, tt.finalAnswer)
			}

			originReleased := false
			handoff, err := resolveAndReleaseSessionAction(
				context.Background(),
				server,
				nil,
				child.Meta().SessionID,
				transition,
				&runtimeLaunchPlan{close: func() error {
					originReleased = true
					return nil
				}},
				nil,
			)
			if err != nil {
				t.Fatalf("resolve and release child handoff: %v", err)
			}
			if !originReleased {
				t.Fatal("child runtime was not released before parent launch")
			}
			resolvedParentSessionID := requireSessionOpenDestination(t, handoff)
			if resolvedParentSessionID != parent.Meta().SessionID {
				t.Fatalf("resolved handoff = %+v, want parent", handoff)
			}

			planner := newSessionLaunchPlanner(server)
			parentPlan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{
				Mode:   launchModeInteractive,
				Intent: serverapi.OpenExistingSessionLaunchIntent(sessionLifecycleSessionIDForTest(t, parent.Meta().SessionID)),
			})
			if err != nil {
				t.Fatalf("plan parent session: %v", err)
			}
			parentRuntimePlan, request, err := prepareSessionUIRun(
				context.Background(),
				server,
				planner,
				parentPlan,
				"",
				false,
				wantInput,
				true,
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

			if testMainInput(parentModel) != wantInput {
				t.Fatalf("parent composer input = %q, want %q", testMainInput(parentModel), wantInput)
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
			if testMainInput(edited) != wantInput+"x" {
				t.Fatalf("edited parent input = %q, want %q", testMainInput(edited), wantInput+"x")
			}
			afterEdit, err := parentRuntimePlan.Wiring.runtimeClient.RefreshMainView()
			if err != nil {
				t.Fatalf("refresh parent runtime after edit: %v", err)
			}
			if afterEdit.Activity != beforeEdit.Activity || afterEdit.Activity.State != clientui.RuntimeActivityRegisteredIdle {
				t.Fatalf("normal edit changed parent runtime activity: before=%+v after=%+v", beforeEdit.Activity, afterEdit.Activity)
			}
		})
	}
}
