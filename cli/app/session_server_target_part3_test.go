package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	serverstartup "core/server/startup"
	shelltool "core/server/tools/shell"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/transcript"
)

func TestStartSessionServerListsPendingPromptSnapshotOverRemoteReads(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	t.Setenv("KENT_REVIEWER_FREQUENCY", "off")
	model := newAppTestModelServer(t,
		appTestModelStep{Calls: []appTestModelToolCall{
			appTestAskCall("ask-remote-1", "Ask?", nil, 0),
			appTestOutsidePatchCall("patch-remote-1", appTestOutsidePatchPath(t)),
		}},
		appTestModelStep{Final: "remote prompt snapshot complete"},
	)
	defer model.Close()

	fixture := startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         model.URL(),
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"))

	server := fixture.attachRemoteSessionServer(t, Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor())
	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test remote prompt snapshot reads")
	defer closeRuntimeLaunchPlan(t, runtimePlan)

	submissionDone, submissionFailed := startAppTestRuntimeSubmission(t, runtimePlan.Wiring.runtimeClient, "start prompt snapshot")
	requireQueuedAppTestRuntimeSubmission(t, submissionDone)
	prompts := waitForRemoteTranscriptPrompts(t, runtimePlan.Wiring.eventDispatcher.transcriptEvents, 2, "", submissionFailed)
	for _, prompt := range prompts {
		switch prompt.Kind {
		case clientui.TranscriptPromptKindQuestion:
			if prompt.PromptID != "ask-remote-1" {
				t.Fatalf("unexpected question prompt: %+v", prompt)
			}
			answerRemoteTranscriptPrompt(t, runtimePlan.Wiring.promptAnswers, prompt, clientui.PromptAnswer{
				PromptID:       string(prompt.PromptID),
				FreeformAnswer: "done",
			})
		case clientui.TranscriptPromptKindApproval:
			answerRemoteTranscriptPrompt(t, runtimePlan.Wiring.promptAnswers, prompt, clientui.PromptAnswer{
				PromptID: string(prompt.PromptID),
				Approval: &clientui.ApprovalPromptAnswer{Decision: clientui.ApprovalDecisionAllowOnce},
			})
		default:
			t.Fatalf("unexpected prompt: %+v", prompt)
		}
	}

	waitForRemoteTranscriptAssistantFinal(
		t,
		runtimePlan.Wiring.eventDispatcher.transcriptEvents,
		"remote prompt snapshot complete",
		submissionFailed,
	)

}

func TestStartSessionServerUsesConfiguredDaemonForProcessFlows(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	fixture := startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, apiKeyMemoryAuthHandler("test-key"))
	fixture.daemon.Background().SetMinimumExecToBgTime(time.Millisecond)

	server := fixture.attachRemoteSessionServer(t, Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor())
	processes := server.RuntimeAttachmentClients()

	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}

	result, err := fixture.daemon.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf 'daemon process output\n'; sleep 0.2"},
		DisplayCommand: "printf 'daemon process output'; sleep 0.2",
		Workdir:        workspace,
		YieldTime:      time.Millisecond,
		OwnerSessionID: plan.SessionID,
		OwnerRunID:     "run-1",
		OwnerStepID:    "step-1",
	})
	if err != nil {
		t.Fatalf("Background().Start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatal("expected backgrounded process")
	}

	proc := waitForRemoteProcess(
		t,
		processes.ProcessViews,
		fixture.daemon.ProjectID(),
		plan.SessionID,
		result.SessionID,
	)
	if proc.OwnerSessionID != plan.SessionID {
		t.Fatalf("unexpected process owner: %+v", proc)
	}

	getResp, err := processes.ProcessViews.GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: result.SessionID})
	if err != nil {
		t.Fatalf("GetProcess: %v", err)
	}
	if getResp.Process == nil || getResp.Process.ID != result.SessionID {
		t.Fatalf("unexpected get process response: %+v", getResp.Process)
	}

	inlineResp := waitForRemoteInlineOutput(t, processes.ProcessControls, result.SessionID)
	if !strings.Contains(inlineResp.Output, "daemon process output") {
		t.Fatalf("unexpected inline output: %q", inlineResp.Output)
	}

	if _, err := processes.ProcessControls.KillProcess(context.Background(), serverapi.ProcessKillRequest{ProcessID: result.SessionID}); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}
	waitForRemoteProcessExit(t, processes.ProcessViews, result.SessionID)

}

func waitForRemoteTranscriptPrompt(t *testing.T, events <-chan ongoingTranscriptEvent, promptID clientui.PromptID, earlyFailures ...<-chan error) clientui.TranscriptPrompt {
	t.Helper()
	return waitForRemoteTranscriptPrompts(t, events, 1, promptID, earlyFailures...)[0]
}

func waitForRemoteTranscriptPrompts(t *testing.T, events <-chan ongoingTranscriptEvent, count int, promptID clientui.PromptID, earlyFailures ...<-chan error) []clientui.TranscriptPrompt {
	t.Helper()
	if count < 1 {
		t.Fatal("remote transcript prompt count must be positive")
	}
	var earlyFailure <-chan error
	if len(earlyFailures) > 0 {
		earlyFailure = earlyFailures[0]
	}
	var seen []clientui.TranscriptMessage
	prompts := make([]clientui.TranscriptPrompt, 0, count)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("transcript event channel closed")
			}
			if evt.Kind == ongoingTranscriptEventLoss {
				t.Fatalf("transcript subscription lost while waiting for prompt: %v", evt.Err)
			}
			if evt.Kind != ongoingTranscriptEventMessage {
				continue
			}
			seen = append(seen, evt.Message)
			var candidates []clientui.TranscriptPrompt
			switch evt.Message.Kind() {
			case clientui.TranscriptMessageHydration:
				hydration := evt.Message.Payload().(clientui.TranscriptHydration)
				candidates = hydration.PendingPrompts
			case clientui.TranscriptMessagePrompt:
				prompt := evt.Message.Payload().(clientui.TranscriptPrompt)
				if prompt.Status == clientui.TranscriptPromptStatusPending {
					candidates = []clientui.TranscriptPrompt{prompt}
				}
			}
			for _, prompt := range candidates {
				if promptID == "" || prompt.PromptID == promptID {
					prompts = append(prompts, prompt)
					if len(prompts) == count {
						return prompts
					}
				}
			}
		case err := <-earlyFailure:
			t.Fatalf("prompt %q failed before publication: %v", promptID, err)
			return nil
		case <-deadline:
			t.Fatalf("timed out waiting for %d transcript prompt(s) %q after messages %+v", count, promptID, seen)
			return nil
		}
	}
}

func requireQueuedAppTestRuntimeSubmission(t *testing.T, done <-chan appTestRuntimeSubmissionResult) {
	t.Helper()
	select {
	case result := <-done:
		requireQueuedAppTestUserTurn(t, result.submission, result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for queued user-turn acceptance")
	}
}

func requireQueuedAppTestUserTurn(
	t *testing.T,
	submission clientui.UserTurnSubmission,
	err error,
) {
	t.Helper()
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued ||
		submission.Message != nil {
		t.Fatalf("SubmitUserMessage = %+v, want queued acceptance", submission)
	}
}

func waitForRemoteTranscriptAssistantFinal(
	t *testing.T,
	events <-chan ongoingTranscriptEvent,
	want string,
	earlyFailures ...<-chan error,
) {
	t.Helper()
	var earlyFailure <-chan error
	if len(earlyFailures) > 0 {
		earlyFailure = earlyFailures[0]
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("transcript event channel closed")
			}
			if evt.Kind == ongoingTranscriptEventLoss {
				t.Fatalf("transcript subscription lost while waiting for assistant final: %v", evt.Err)
			}
			if evt.Kind != ongoingTranscriptEventMessage {
				continue
			}
			var rows []clientui.TranscriptCommittedRow
			switch evt.Message.Kind() {
			case clientui.TranscriptMessageHydration:
				rows = evt.Message.Payload().(clientui.TranscriptHydration).TailSegment.Entries
			case clientui.TranscriptMessageCommittedRow:
				rows = []clientui.TranscriptCommittedRow{
					evt.Message.Payload().(clientui.TranscriptCommittedRow),
				}
			}
			for _, row := range rows {
				if row.Kind == clientui.TranscriptRowAssistant &&
					row.Assistant != nil &&
					row.Assistant.Phase == transcript.AssistantPhaseFinal &&
					row.Assistant.Text == want {
					return
				}
			}
		case err := <-earlyFailure:
			t.Fatalf("assistant final failed before publication: %v", err)
		case <-deadline:
			t.Fatalf("timed out waiting for assistant final %q", want)
		}
	}
}

func answerRemoteTranscriptPrompt(t *testing.T, answerer *transcriptPromptAnswerer, prompt clientui.TranscriptPrompt, answer clientui.PromptAnswer) {
	t.Helper()
	if answerer == nil {
		t.Fatal("transcript prompt answerer is required")
	}
	_, cmd, err := answerer.delivery(prompt, answer, nil)
	if err != nil {
		t.Fatalf("prepare transcript prompt answer: %v", err)
	}
	msg := cmd()
	result, ok := msg.(promptAnswerDeliveryResultMsg)
	if !ok {
		t.Fatalf("transcript prompt answer result type = %T", msg)
	}
	if result.err != nil {
		t.Fatalf("deliver transcript prompt answer: %v", result.err)
	}
}

func waitForRemoteProcess(
	t *testing.T,
	views apicontract.ProcessViewService,
	projectID string,
	sessionID string,
	processID string,
) clientui.BackgroundProcess {
	t.Helper()
	var process clientui.BackgroundProcess
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		resp, err := views.ListProcesses(context.Background(), serverapi.ProcessListRequest{
			ProjectID:      projectID,
			OwnerSessionID: &sessionID,
		})
		if err != nil {
			t.Fatalf("ListProcesses: %v", err)
		}
		for _, proc := range resp.Processes {
			if proc.ID == processID {
				process = proc
				return true
			}
		}
		return false
	}, "timed out waiting for process %s in session %s", processID, sessionID)
	return process
}

func waitForRemoteProcessExit(t *testing.T, views apicontract.ProcessViewService, processID string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		resp, err := views.GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: processID})
		if err != nil {
			t.Fatalf("GetProcess: %v", err)
		}
		return resp.Process != nil && !resp.Process.Running
	}, "timed out waiting for process %s to exit", processID)
}

func waitForRemoteInlineOutput(t *testing.T, controls apicontract.ProcessControlService, processID string) serverapi.ProcessInlineOutputResponse {
	t.Helper()
	var output serverapi.ProcessInlineOutputResponse
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		var err error
		output, err = controls.GetInlineOutput(context.Background(), serverapi.ProcessInlineOutputRequest{ProcessID: processID, MaxChars: 1024})
		if err != nil {
			t.Fatalf("GetInlineOutput: %v", err)
		}
		return strings.TrimSpace(output.Output) != ""
	}, "timed out waiting for inline output from %s", processID)
	return output
}
