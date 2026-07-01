package app

import (
	"context"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEmbeddedAppServerDeliversBackgroundCompletionWhileIdle(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()
	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test background completion while idle")
	defer runtimePlan.Close()

	activity := server.inner.SessionActivityClient()
	if activity == nil {
		t.Fatal("expected session activity client")
	}
	sub, err := activity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: plan.SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	processID := "bg-1000"
	server.inner.BackgroundRouter().Handle(shelltool.Event{
		Type:             shelltool.EventCompleted,
		NoticeSuppressed: true,
		Snapshot: shelltool.Snapshot{
			ID:             processID,
			OwnerSessionID: plan.SessionID,
			State:          "completed",
			Command:        "sleep 1; printf done",
			Workdir:        workspace,
			LogPath:        "/tmp/bg-1000.log",
		},
		Preview: "done",
	})

	evt := waitForSessionActivityEvent(t, sub, 5*time.Second, func(evt clientui.Event) bool {
		return evt.Kind == clientui.EventBackgroundUpdated && evt.Background != nil && evt.Background.ID == processID && evt.Background.Type == "completed"
	})
	if evt.Background.State != "completed" {
		t.Fatalf("background state = %q, want completed", evt.Background.State)
	}
	if !evt.Background.NoticeSuppressed {
		t.Fatal("expected delivery-only test event to stay suppressed")
	}
}

func TestPrepareRuntimeForwardsBackgroundCompletionIntoProjectedRuntimeEvents(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test projected background completion while idle")
	defer runtimePlan.Close()

	processID := "bg-1001"
	server.inner.BackgroundRouter().Handle(shelltool.Event{
		Type:             shelltool.EventCompleted,
		NoticeSuppressed: true,
		Snapshot: shelltool.Snapshot{
			ID:             processID,
			OwnerSessionID: plan.SessionID,
			State:          "completed",
			Command:        "sleep 1; printf done",
			Workdir:        workspace,
			LogPath:        "/tmp/bg-1001.log",
		},
		Preview: "done",
	})

	select {
	case evt := <-runtimePlan.Wiring.runtimeEvents:
		if evt.Kind != clientui.EventBackgroundUpdated {
			t.Fatalf("projected event kind = %q, want %q", evt.Kind, clientui.EventBackgroundUpdated)
		}
		if evt.Background == nil || evt.Background.ID != processID || evt.Background.Type != "completed" {
			t.Fatalf("unexpected projected background event: %+v", evt.Background)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for projected background completion event")
	}
}

func TestEmbeddedAppServerPrepareRuntimeWiresProcessControlForUIActions(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test prepare runtime process control")
	defer runtimePlan.Close()
	if runtimePlan.Wiring.processControls == nil {
		t.Fatal("expected PrepareRuntime to wire process control client")
	}

	controls := &stubEmbeddedProcessControlClient{inlineResp: serverapi.ProcessInlineOutputResponse{Output: "remote preview", LogPath: "/tmp/remote.log"}}
	runtimePlan.Wiring.processControls = controls
	processClient := newUIProcessClientWithReads(runtimePlan.Wiring.processViews, runtimePlan.Wiring.processControls)

	preview, logPath, err := processClient.InlineOutput(context.Background(), "proc-1", 12_000)
	if err != nil {
		t.Fatalf("InlineOutput: %v", err)
	}
	if preview != "remote preview" || logPath != "/tmp/remote.log" {
		t.Fatalf("unexpected inline output payload preview=%q logPath=%q", preview, logPath)
	}
	if err := processClient.KillProcess(context.Background(), "proc-1"); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}
	if len(controls.killed) != 1 || controls.killed[0] != "proc-1" {
		t.Fatalf("expected shared process control client to handle kill, got %+v", controls.killed)
	}
}

func TestEmbeddedAppServerPrepareRuntimeWiresProcessOutputClient(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test prepare runtime process output")
	defer runtimePlan.Close()
	if runtimePlan.Wiring.processOutput == nil {
		t.Fatal("expected PrepareRuntime to wire process output client")
	}
}

func TestEmbeddedAppServerPromptActivityStreamsAndHydratesPendingResources(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test embedded prompt activity parity")
	defer runtimePlan.Close()

	askDone := make(chan struct {
		resp askquestion.AskQuestionResponse
		err  error
	}, 1)
	go func() {
		resp, err := server.inner.AwaitPromptResponse(context.Background(), plan.SessionID, askquestion.AskQuestionRequest{
			ID:                     "ask-embedded-1",
			Question:               "Pick one",
			Suggestions:            []string{"one", "two"},
			RecommendedOptionIndex: 2,
		})
		askDone <- struct {
			resp askquestion.AskQuestionResponse
			err  error
		}{resp: resp, err: err}
	}()
	runtimeClients := server.RuntimeAttachmentClients()
	waitForPendingAskResources(t, runtimeClients.AskViews, plan.SessionID, 1)
	askEvt := waitForRemoteAskEvent(t, runtimePlan.Wiring.askEvents)
	if askEvt.req.PromptID != "ask-embedded-1" || askEvt.req.Question != "Pick one" {
		t.Fatalf("unexpected ask event: %+v", askEvt.req)
	}
	askEvt.reply <- askReply{response: clientui.PromptAnswer{PromptID: askEvt.req.PromptID, SelectedOptionNumber: 2}}
	select {
	case result := <-askDone:
		if result.err != nil {
			t.Fatalf("AwaitPromptResponse ask: %v", result.err)
		}
		if result.resp.RequestID != "ask-embedded-1" || result.resp.SelectedOptionNumber != 2 {
			t.Fatalf("unexpected ask response: %+v", result.resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for embedded ask response")
	}
	waitForPendingAskResources(t, runtimeClients.AskViews, plan.SessionID, 0)

	approvalDone := make(chan struct {
		resp askquestion.AskQuestionResponse
		err  error
	}, 1)
	go func() {
		resp, err := server.inner.AwaitPromptResponse(context.Background(), plan.SessionID, askquestion.AskQuestionRequest{
			ID:              "approval-embedded-1",
			Question:        "Approve it?",
			Approval:        true,
			ApprovalOptions: []askquestion.AskQuestionApprovalOption{{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"}},
		})
		approvalDone <- struct {
			resp askquestion.AskQuestionResponse
			err  error
		}{resp: resp, err: err}
	}()
	waitForPendingApprovalResources(t, runtimeClients.ApprovalViews, plan.SessionID, 1)
	approvalEvt := waitForRemoteAskEvent(t, runtimePlan.Wiring.askEvents)
	if !approvalEvt.req.Approval || approvalEvt.req.PromptID != "approval-embedded-1" {
		t.Fatalf("unexpected approval event: %+v", approvalEvt.req)
	}
	approvalEvt.reply <- askReply{response: clientui.PromptAnswer{PromptID: approvalEvt.req.PromptID, Approval: &clientui.ApprovalPromptAnswer{Decision: clientui.ApprovalDecisionAllowOnce, Commentary: "trusted"}}}
	select {
	case result := <-approvalDone:
		if result.err != nil {
			t.Fatalf("AwaitPromptResponse approval: %v", result.err)
		}
		if result.resp.RequestID != "approval-embedded-1" || result.resp.Approval == nil || result.resp.Approval.Decision != askquestion.AskQuestionApprovalDecisionAllowOnce || result.resp.Approval.Commentary != "trusted" {
			t.Fatalf("unexpected approval response: %+v", result.resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for embedded approval response")
	}
	waitForPendingApprovalResources(t, runtimeClients.ApprovalViews, plan.SessionID, 0)
}

func TestEmbeddedAppServerPendingPromptsNotifyUIAskHook(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test embedded ask notification")
	defer runtimePlan.Close()

	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	model := newProjectedTestUIModel(runtimePlan.Wiring.runtimeClient, closedProjectedRuntimeEvents(), runtimePlan.Wiring.askEvents, WithUIAskNotificationHook(hooks))

	firstDone := make(chan error, 1)
	go func() {
		_, err := server.inner.AwaitPromptResponse(context.Background(), plan.SessionID, askquestion.AskQuestionRequest{ID: "ask-notify-1", Question: "First?"})
		firstDone <- err
	}()
	secondDone := make(chan error, 1)
	go func() {
		_, err := server.inner.AwaitPromptResponse(context.Background(), plan.SessionID, askquestion.AskQuestionRequest{ID: "ask-notify-2", Question: "Second?"})
		secondDone <- err
	}()

	first := waitForRemoteAskEvent(t, runtimePlan.Wiring.askEvents)
	second := waitForRemoteAskEvent(t, runtimePlan.Wiring.askEvents)
	next, _ := model.Update(askEventMsg{event: first})
	model = next.(*uiModel)
	next, _ = model.Update(askEventMsg{event: second})
	_ = next.(*uiModel)

	if got := ringer.Count(); got != 2 {
		t.Fatalf("ask notification count = %d, want 2", got)
	}
	wantLast := "kent: Question: " + second.req.Question
	if got := ringer.Last(); got != wantLast {
		t.Fatalf("last ask notification = %q, want %q", got, wantLast)
	}

	for _, evt := range []askEvent{first, second} {
		evt.reply <- askReply{response: clientui.PromptAnswer{PromptID: evt.req.PromptID, Answer: "ok"}}
	}
	for name, ch := range map[string]chan error{"first": firstDone, "second": secondDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("%s AwaitPromptResponse: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s ask response", name)
		}
	}
}

func TestEmbeddedAppServerProcessOutputStreamsAndInlineSnapshot(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}

	manager := server.inner.Background()
	if manager == nil {
		t.Fatal("expected server background manager")
	}
	manager.SetMinimumExecToBgTime(fastBackgroundTestYield)
	result, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf 'embedded process output\n'; sleep 0.2"},
		DisplayCommand: "printf 'embedded process output'; sleep 0.2",
		Workdir:        workspace,
		YieldTime:      fastBackgroundTestYield,
		OwnerSessionID: plan.SessionID,
	})
	if err != nil {
		t.Fatalf("Background().Start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatal("expected backgrounded process")
	}

	runtimeClients := server.RuntimeAttachmentClients()
	proc := waitForRemoteProcess(t, runtimeClients.ProcessViews, plan.SessionID, result.SessionID)
	if proc.OwnerSessionID != plan.SessionID {
		t.Fatalf("unexpected process owner: %+v", proc)
	}

	outputSub, err := runtimeClients.ProcessOutput.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: result.SessionID, OffsetBytes: 0})
	if err != nil {
		t.Fatalf("SubscribeProcessOutput: %v", err)
	}
	defer func() { _ = outputSub.Close() }()
	chunk, err := outputSub.Next(context.Background())
	if err != nil {
		t.Fatalf("ProcessOutput Next: %v", err)
	}
	if !strings.Contains(chunk.Text, "embedded process output") {
		t.Fatalf("unexpected process output chunk: %+v", chunk)
	}

	inlineResp := waitForRemoteInlineOutput(t, runtimeClients.ProcessControls, result.SessionID)
	if !strings.Contains(inlineResp.Output, "embedded process output") {
		t.Fatalf("unexpected inline output: %q", inlineResp.Output)
	}

	if _, err := runtimeClients.ProcessControls.KillProcess(context.Background(), serverapi.ProcessKillRequest{ClientRequestID: uuid.NewString(), ProcessID: result.SessionID}); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}
	waitForRemoteProcessExit(t, runtimeClients.ProcessViews, result.SessionID)
}

func TestPrepareSharedRuntimeUsesCallerContextForAttachRPCs(t *testing.T) {
	ctxKey := struct{}{}
	ctxValue := "attach-context"
	promptErr := errors.New("prompt subscribe failed")
	server := &testEmbeddedServer{
		sessionRuntime: &recordingSessionRuntimeClient{
			activate: func(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				if got := ctx.Value(ctxKey); got != ctxValue {
					t.Fatalf("activate context value = %v, want %v", got, ctxValue)
				}
				if req.SessionID != "session-1" {
					t.Fatalf("unexpected activate request: %+v", req)
				}
				return serverapi.SessionRuntimeActivateResponse{}, nil
			},
			release: func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
		sessionActivity: &recordingSessionActivityClient{
			subscribe: func(ctx context.Context, req serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
				if got := ctx.Value(ctxKey); got != ctxValue {
					t.Fatalf("session activity context value = %v, want %v", got, ctxValue)
				}
				if req.SessionID != "session-1" {
					t.Fatalf("unexpected session activity request: %+v", req)
				}
				return noOpSessionActivitySubscription{}, nil
			},
		},
		promptActivityClient: &recordingPromptActivityClient{
			subscribe: func(ctx context.Context, req serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
				if got := ctx.Value(ctxKey); got != ctxValue {
					t.Fatalf("prompt activity context value = %v, want %v", got, ctxValue)
				}
				if req.SessionID != "session-1" {
					t.Fatalf("unexpected prompt activity request: %+v", req)
				}
				return nil, promptErr
			},
		},
	}

	_, err := prepareSharedRuntime(context.WithValue(context.Background(), ctxKey, ctxValue), server, sessionLaunchPlan{SessionID: "session-1", WorkspaceRoot: "/tmp/workspace"}, io.Discard, "test")
	if !errors.Is(err, promptErr) {
		t.Fatalf("prepareSharedRuntime error = %v, want %v", err, promptErr)
	}
}

func TestPrepareSharedRuntimeSubscribeFailureReleasesOnceWithBoundedContext(t *testing.T) {
	for _, tc := range []struct {
		name            string
		sessionErr      error
		promptErr       error
		wantPromptStart bool
	}{
		{name: "session subscribe failure", sessionErr: errors.New("session subscribe failed")},
		{name: "prompt subscribe failure", promptErr: errors.New("prompt subscribe failed"), wantPromptStart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			released := make(chan context.Context, 2)
			releaseCount := 0
			promptStarted := false
			server := &testEmbeddedServer{
				sessionRuntime: &recordingSessionRuntimeClient{
					activate: func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
						return serverapi.SessionRuntimeActivateResponse{}, nil
					},
					release: func(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
						releaseCount++
						released <- ctx
						if req.SessionID != "session-1" {
							t.Fatalf("unexpected release request: %+v", req)
						}
						return serverapi.SessionRuntimeReleaseResponse{}, nil
					},
				},
				sessionActivity: &recordingSessionActivityClient{
					subscribe: func(context.Context, serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
						if tc.sessionErr != nil {
							return nil, tc.sessionErr
						}
						return noOpSessionActivitySubscription{}, nil
					},
				},
				promptActivityClient: &recordingPromptActivityClient{
					subscribe: func(context.Context, serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
						promptStarted = true
						if tc.promptErr != nil {
							return nil, tc.promptErr
						}
						return nil, nil
					},
				},
			}

			_, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1", WorkspaceRoot: "/tmp/workspace"}, io.Discard, "test")
			wantErr := tc.sessionErr
			if wantErr == nil {
				wantErr = tc.promptErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("prepareSharedRuntime error = %v, want %v", err, wantErr)
			}
			if promptStarted != tc.wantPromptStart {
				t.Fatalf("prompt started = %v, want %v", promptStarted, tc.wantPromptStart)
			}
			if releaseCount != 1 {
				t.Fatalf("release count = %d, want exactly 1", releaseCount)
			}
			select {
			case ctx := <-released:
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("expected bounded release context deadline")
				}
				remaining := time.Until(deadline)
				if remaining <= 0 || remaining > runtimeReleaseTimeout {
					t.Fatalf("unexpected bounded release deadline remaining=%v timeout=%v", remaining, runtimeReleaseTimeout)
				}
			default:
				t.Fatal("expected runtime release on subscribe failure")
			}
		})
	}
}

func TestPrepareSharedRuntimeInstallsTurnQueueHook(t *testing.T) {
	server := &testEmbeddedServer{
		sessionRuntime: &recordingSessionRuntimeClient{
			activate: func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return serverapi.SessionRuntimeActivateResponse{}, nil
			},
			release: func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
		sessionActivity: &recordingSessionActivityClient{
			subscribe: func(context.Context, serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
				return noOpSessionActivitySubscription{}, nil
			},
		},
		promptActivityClient: &recordingPromptActivityClient{
			subscribe: func(context.Context, serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
				return nil, nil
			},
		},
		sessionViewClient: &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionName: "shared session"}}},
	}

	plan, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1", SessionName: "fallback session", WorkspaceRoot: "/tmp/workspace", ActiveSettings: config.Settings{NotificationMethod: "bel"}}, io.Discard, "test")
	if err != nil {
		t.Fatalf("prepareSharedRuntime: %v", err)
	}
	defer plan.Close()
	if plan.Wiring == nil || plan.Wiring.turnQueueHook == nil {
		t.Fatal("expected shared runtime wiring to install turn queue hook")
	}
}
