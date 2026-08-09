package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

type lifecycleSetupTestRemote struct {
	apicontract.WorkflowService
	subscription serverapi.WorktreeSetupSubscription
}

func (r lifecycleSetupTestRemote) SubscribeWorktreeSetup(context.Context, serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	return r.subscription, nil
}

func (lifecycleSetupTestRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (lifecycleSetupTestRemote) Close() error { return nil }

type lifecycleCommandTestRemote struct {
	apicontract.WorkflowService
	setupEvent     func(serverapi.WorktreeSetupOperationID) serverapi.WorktreeSetupEvent
	subscription   serverapi.WorktreeSetupSubscription
	startResponse  serverapi.WorkflowTaskStartResponse
	startError     error
	resumeResponse serverapi.WorkflowTaskResumeResponse
	resumeError    error
	startRequests  []serverapi.WorkflowTaskStartRequest
	resumeRequests []serverapi.WorkflowTaskResumeRequest
}

func (r *lifecycleCommandTestRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{
		Task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{
				ID:        req.TaskID,
				ShortID:   "KENT-453",
				ProjectID: "project-1",
			},
		},
	}, nil
}

func (r *lifecycleCommandTestRemote) StartWorkflowTask(_ context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	r.startRequests = append(r.startRequests, req)
	return r.startResponse, r.startError
}

func (r *lifecycleCommandTestRemote) ResumeWorkflowTask(_ context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	r.resumeRequests = append(r.resumeRequests, req)
	return r.resumeResponse, r.resumeError
}

func (r *lifecycleCommandTestRemote) SubscribeWorktreeSetup(_ context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if r.subscription != nil {
		return r.subscription, nil
	}
	return &terminalWorktreeSetupSubscription{event: r.setupEvent(req.SetupOperationID)}, nil
}

func (*lifecycleCommandTestRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (*lifecycleCommandTestRemote) Close() error { return nil }

type gatedSetupSubscription struct {
	terminal <-chan serverapi.WorktreeSetupEvent
}

func (s gatedSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	select {
	case event := <-s.terminal:
		return event, nil
	case <-ctx.Done():
		return serverapi.WorktreeSetupEvent{}, ctx.Err()
	}
}

func (gatedSetupSubscription) Close() error { return nil }

type observedGatedSetupSubscription struct {
	context  chan<- context.Context
	terminal <-chan serverapi.WorktreeSetupEvent
}

func (s observedGatedSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	s.context <- ctx
	select {
	case event := <-s.terminal:
		return event, nil
	case <-ctx.Done():
		return serverapi.WorktreeSetupEvent{}, ctx.Err()
	}
}

func (observedGatedSetupSubscription) Close() error { return nil }

type deadlineSetupSubscription struct {
	context chan<- context.Context
}

func (s deadlineSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	s.context <- ctx
	<-ctx.Done()
	return serverapi.WorktreeSetupEvent{}, ctx.Err()
}

func (deadlineSetupSubscription) Close() error { return nil }

func TestWorkflowMutationWaitsForBufferedSetupTerminalAfterAppliedResponse(t *testing.T) {
	terminal := make(chan serverapi.WorktreeSetupEvent, 1)
	remote := lifecycleSetupTestRemote{subscription: gatedSetupSubscription{terminal: terminal}}
	returned := make(chan error, 1)
	setupIDs := make(chan serverapi.WorktreeSetupOperationID, 1)

	go func() {
		_, _, err := runWorkflowMutationWithSetupProgress(
			context.Background(),
			remote,
			io.Discard,
			func(_ context.Context, setupOperationID serverapi.WorktreeSetupOperationID) (serverapi.WorkflowTaskResumeResponse, error) {
				setupIDs <- setupOperationID
				return serverapi.WorkflowTaskResumeResponse{
					Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
					Applied: &serverapi.WorkflowTaskResumeApplied{
						CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
					},
				}, nil
			},
			func(resp serverapi.WorkflowTaskResumeResponse) bool {
				return resp.Outcome == serverapi.WorkflowExecutionTargetActionOutcomeApplied
			},
		)
		returned <- err
	}()

	select {
	case err := <-returned:
		t.Fatalf("mutation returned before terminal setup event: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	terminal <- serverapi.WorktreeSetupEvent{
		SetupOperationID: <-setupIDs,
		Phase:            serverapi.WorktreeSetupPhaseCompleted,
		Completed:        &serverapi.WorktreeSetupCompleted{},
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("mutation returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("mutation did not return after terminal setup event")
	}
}

func TestWorkflowMutationStartsSetupObservationTimeoutAfterAppliedResponse(t *testing.T) {
	terminal := make(chan serverapi.WorktreeSetupEvent, 1)
	contexts := make(chan context.Context, 1)
	remote := lifecycleSetupTestRemote{
		subscription: observedGatedSetupSubscription{
			context:  contexts,
			terminal: terminal,
		},
	}
	mutationPending := make(chan struct{})
	releaseMutation := make(chan struct{})
	prematureDeadline := make(chan bool, 1)
	returned := make(chan error, 1)
	setupIDs := make(chan serverapi.WorktreeSetupOperationID, 1)

	go func() {
		_, _, err := runWorkflowMutationWithSetupProgress(
			context.Background(),
			remote,
			io.Discard,
			func(_ context.Context, setupOperationID serverapi.WorktreeSetupOperationID) (serverapi.WorkflowTaskResumeResponse, error) {
				setupIDs <- setupOperationID
				observationCtx := <-contexts
				_, hasDeadline := observationCtx.Deadline()
				prematureDeadline <- hasDeadline
				close(mutationPending)
				<-releaseMutation
				return serverapi.WorkflowTaskResumeResponse{
					Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
					Applied: &serverapi.WorkflowTaskResumeApplied{
						CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
					},
				}, nil
			},
			func(resp serverapi.WorkflowTaskResumeResponse) bool {
				return resp.Outcome == serverapi.WorkflowExecutionTargetActionOutcomeApplied
			},
		)
		returned <- err
	}()

	<-mutationPending
	terminal <- serverapi.WorktreeSetupEvent{
		SetupOperationID: <-setupIDs,
		Phase:            serverapi.WorktreeSetupPhaseCompleted,
		Completed:        &serverapi.WorktreeSetupCompleted{},
	}
	close(releaseMutation)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("mutation returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("mutation did not return after its buffered terminal setup event")
	}
	if <-prematureDeadline {
		t.Fatal("setup observation timeout started before mutation applied")
	}
}

func TestWorkflowMutationUsesTerminalFailureScriptPath(t *testing.T) {
	events := make(chan serverapi.WorktreeSetupEvent, 3)
	remote := lifecycleSetupTestRemote{subscription: gatedSetupSubscription{terminal: events}}
	results := make(chan *worktreeSetupTerminalObservation, 1)
	setupIDs := make(chan serverapi.WorktreeSetupOperationID, 1)

	go func() {
		_, terminal, _ := runWorkflowMutationWithSetupProgress(
			context.Background(),
			remote,
			io.Discard,
			func(_ context.Context, setupOperationID serverapi.WorktreeSetupOperationID) (serverapi.WorkflowTaskResumeResponse, error) {
				setupIDs <- setupOperationID
				return serverapi.WorkflowTaskResumeResponse{
					Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
					Applied: &serverapi.WorkflowTaskResumeApplied{
						CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
					},
				}, nil
			},
			func(resp serverapi.WorkflowTaskResumeResponse) bool {
				return resp.Outcome == serverapi.WorkflowExecutionTargetActionOutcomeApplied
			},
		)
		results <- terminal
	}()

	id := <-setupIDs
	retained := lifecycleSetupTestWorktree("/tmp/worktree")
	events <- serverapi.WorktreeSetupEvent{
		SetupOperationID: id,
		Phase:            serverapi.WorktreeSetupPhaseFailed,
		Failed: &serverapi.WorktreeSetupFailed{
			RetryReadiness: serverapi.WorktreeSetupRetryReady,
			Cause: serverapi.WorktreeSetupFailureCause{
				Kind:    serverapi.WorktreeSetupFailureTimeout,
				Timeout: &serverapi.WorktreeSetupTimeout{},
			},
			Diagnostic:       "setup retry timed out",
			ScriptPath:       lifecycleStringPointer("/repo/setup-retry.sh"),
			ExecutionTarget:  lifecycleSetupExecutionTarget(serverapi.WorkflowExecutionTargetModeHead),
			RetainedWorktree: &retained,
		},
	}

	terminal := <-results
	if terminal == nil || terminal.Event.Failed == nil ||
		terminal.Event.Failed.ScriptPath == nil ||
		*terminal.Event.Failed.ScriptPath != "/repo/setup-retry.sh" {
		t.Fatalf("terminal observation = %+v", terminal)
	}
}

func TestStartSetupFailurePresentationProvidesTypedResumeActions(t *testing.T) {
	retained := lifecycleSetupTestWorktree("/tmp/KENT-453")
	event := serverapi.WorktreeSetupEvent{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		Phase:            serverapi.WorktreeSetupPhaseFailed,
		Failed: &serverapi.WorktreeSetupFailed{
			RetryReadiness: serverapi.WorktreeSetupRetryReady,
			Cause: serverapi.WorktreeSetupFailureCause{
				Kind: serverapi.WorktreeSetupFailureProcessExit,
				ProcessExit: &serverapi.WorktreeSetupProcessExit{
					ExitCode: 7,
				},
			},
			Diagnostic: "setup exited with status 7",
			ScriptPath: lifecycleStringPointer("/repo/.kent/setup.sh"),
			ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
				Mode:      serverapi.WorkflowExecutionTargetModeCustomRef,
				CustomRef: lifecycleStringPointer("refs/heads/dev"),
			},
			RetainedWorktree: &retained,
		},
	}

	presentation, err := taskLifecycleSetupPresentation(
		taskLifecycleOperationStart,
		taskLifecycleCommandContext{TaskRef: "KENT-453"},
		worktreeSetupTerminalObservation{Event: event},
	)
	if err != nil {
		t.Fatalf("taskLifecycleSetupPresentation: %v", err)
	}
	if presentation.Kind != taskLifecyclePresentationSetupRecovery ||
		presentation.Operation != taskLifecycleOperationStart ||
		presentation.Diagnostic != event.Failed.Diagnostic ||
		presentation.SetupScriptPath == nil ||
		*presentation.SetupScriptPath != "/repo/.kent/setup.sh" ||
		presentation.RetainedWorktree == nil ||
		presentation.RetainedWorktree.Path != "/tmp/KENT-453" {
		t.Fatalf("presentation = %+v", presentation)
	}
	wantActions := []taskLifecycleAction{
		{Kind: taskLifecycleActionRetryCurrentTarget, Args: []string{"kent", "task", "resume", "KENT-453", "--execution-target", "ref:refs/heads/dev"}},
		{Kind: taskLifecycleActionChooseNoWorktree, Args: []string{"kent", "task", "resume", "KENT-453", "--execution-target", "none"}},
		{Kind: taskLifecycleActionChooseHead, Args: []string{"kent", "task", "resume", "KENT-453", "--execution-target", "head"}},
		{Kind: taskLifecycleActionChooseDefaultBranch, Args: []string{"kent", "task", "resume", "KENT-453", "--execution-target", "default-branch"}},
		{Kind: taskLifecycleActionChooseCustomRef, Args: []string{"kent", "task", "resume", "KENT-453", "--execution-target", "ref:<revision>"}},
	}
	if !reflect.DeepEqual(presentation.Actions, wantActions) {
		t.Fatalf("actions = %+v, want %+v", presentation.Actions, wantActions)
	}
}

func TestWorkflowSetupObservationTimeoutUsesTwoMinuteBudgetAndInspectionOnlyPresentation(t *testing.T) {
	contexts := make(chan context.Context, 1)
	remote := lifecycleSetupTestRemote{subscription: deadlineSetupSubscription{context: contexts}}
	observation, err := subscribeWorktreeSetupProgress(
		context.Background(),
		remote,
		serverapi.NewWorktreeSetupOperationID(),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("subscribeWorktreeSetupProgress: %v", err)
	}
	observationCtx := <-contexts
	if _, hasDeadline := observationCtx.Deadline(); hasDeadline {
		t.Fatal("setup observation timeout started before being armed")
	}
	const testTimeout = 20 * time.Millisecond
	startedAt := time.Now()
	observation.startTimeout(testTimeout)
	result := <-observation.done
	if !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("observation error = %v, want deadline exceeded", result.err)
	}
	elapsed := time.Since(startedAt)
	if elapsed < testTimeout || elapsed > time.Second {
		t.Fatalf("observation timeout elapsed = %s, want at least %s", elapsed, testTimeout)
	}
	if workflowTaskSetupObservationTimeout != 2*time.Minute {
		t.Fatalf("observation budget = %s, want two minutes", workflowTaskSetupObservationTimeout)
	}

	presentation := taskLifecycleObservationPresentation(
		taskLifecycleOperationResume,
		taskLifecycleCommandContext{TaskRef: "KENT-453"},
		&worktreeSetupObservationError{cause: result.err},
	)
	if presentation.Kind != taskLifecyclePresentationObservationTimedOut ||
		presentation.Operation != taskLifecycleOperationResume {
		t.Fatalf("presentation = %+v", presentation)
	}
	wantActions := taskLifecycleInspectionActions("KENT-453", nil)
	if !reflect.DeepEqual(presentation.Actions, wantActions) {
		t.Fatalf("actions = %+v, want inspection only %+v", presentation.Actions, wantActions)
	}
	for _, action := range presentation.Actions {
		switch action.Kind {
		case taskLifecycleActionRetryCurrentTarget,
			taskLifecycleActionChooseNoWorktree,
			taskLifecycleActionChooseHead,
			taskLifecycleActionChooseDefaultBranch,
			taskLifecycleActionChooseCustomRef:
			t.Fatalf("timeout presentation contains retry-ready action %+v", action)
		}
	}
}

func TestNotRequiredSetupResultSucceedsAndKeepsRetainedWorktreeWarning(t *testing.T) {
	retained := &serverapi.RetainedPreviousWorktree{
		Worktree: lifecycleSetupTestWorktree("/tmp/orphan"),
	}
	event := serverapi.WorktreeSetupEvent{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		Phase:            serverapi.WorktreeSetupPhaseNotRequired,
		NotRequired: &serverapi.WorktreeSetupNotRequired{
			Reason:                   serverapi.WorktreeSetupNotRequiredNoConfiguredScript,
			RetainedPreviousWorktree: retained,
		},
	}

	outcome, err := projectTaskLifecycleSetupOutcome(
		taskLifecycleOperationResume,
		taskLifecycleCommandContext{TaskRef: "KENT-453"},
		&worktreeSetupTerminalObservation{Event: event},
		nil,
	)
	if err != nil {
		t.Fatalf("projectTaskLifecycleSetupOutcome: %v", err)
	}
	if !outcome.Success || outcome.Presentation == nil ||
		outcome.Presentation.Kind != taskLifecyclePresentationRetainedWorktree ||
		outcome.Presentation.RetainedPreviousWorktree == nil ||
		outcome.Presentation.RetainedPreviousWorktree.Path != "/tmp/orphan" {
		t.Fatalf("outcome = %+v", outcome)
	}
	wantActions := retainedWorktreeInspectionActions(outcome.Presentation.RetainedPreviousWorktree)
	if !reflect.DeepEqual(outcome.Presentation.Actions, wantActions) {
		t.Fatalf("successful warning actions = %+v, want %+v", outcome.Presentation.Actions, wantActions)
	}
	if len(outcome.Presentation.Actions) != 1 ||
		outcome.Presentation.Actions[0].Kind != taskLifecycleActionListWorktrees {
		t.Fatalf("successful warning actions = %+v, want only Worktree inspection", outcome.Presentation.Actions)
	}
}

func TestTaskStartCompletedSetupPreservesJSONAppliedOutcome(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	remote := &lifecycleCommandTestRemote{
		startResponse: serverapi.WorkflowTaskStartResponse{
			Outcome: serverapi.WorkflowTaskActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskStartApplied{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
			},
		},
		setupEvent: func(id serverapi.WorktreeSetupOperationID) serverapi.WorktreeSetupEvent {
			return serverapi.WorktreeSetupEvent{
				SetupOperationID: id,
				Phase:            serverapi.WorktreeSetupPhaseCompleted,
				Completed:        &serverapi.WorktreeSetupCompleted{},
			}
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskStartSubcommand([]string{"task-1", "--json"}, &stdout, &stderr)
	if exitCode != 0 || stderr.Len() != 0 || len(remote.startRequests) != 1 {
		t.Fatalf("exit=%d stderr=%q requests=%+v", exitCode, stderr.String(), remote.startRequests)
	}
	var response serverapi.WorkflowTaskStartResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode JSON outcome: %v", err)
	}
	if response.Outcome != serverapi.WorkflowTaskActionOutcomeApplied || response.Applied == nil {
		t.Fatalf("JSON response = %+v", response)
	}
}

func TestTaskResumeFailedSetupReturnsNonzeroAfterAppliedMutation(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	retained := lifecycleSetupTestWorktree("/tmp/resume-retained")
	remote := &lifecycleCommandTestRemote{
		resumeResponse: serverapi.WorkflowTaskResumeResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskResumeApplied{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
			},
		},
		setupEvent: func(id serverapi.WorktreeSetupOperationID) serverapi.WorktreeSetupEvent {
			return serverapi.WorktreeSetupEvent{
				SetupOperationID: id,
				Phase:            serverapi.WorktreeSetupPhaseFailed,
				Failed: &serverapi.WorktreeSetupFailed{
					RetryReadiness: serverapi.WorktreeSetupRetryReady,
					Cause: serverapi.WorktreeSetupFailureCause{
						Kind: serverapi.WorktreeSetupFailureProcessExit,
						ProcessExit: &serverapi.WorktreeSetupProcessExit{
							ExitCode: 2,
						},
					},
					Diagnostic:       "setup exited with status 2",
					ScriptPath:       lifecycleStringPointer("/repo/setup.sh"),
					ExecutionTarget:  lifecycleSetupExecutionTarget(serverapi.WorkflowExecutionTargetModeHead),
					RetainedWorktree: &retained,
				},
			}
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskResumeSubcommand([]string{"task-1", "--execution-target", "head"}, &stdout, &stderr)
	if exitCode != 1 || stdout.Len() != 0 || len(remote.resumeRequests) != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.resumeRequests)
	}
	if remote.resumeRequests[0].ExecutionTarget == nil ||
		remote.resumeRequests[0].ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeHead {
		t.Fatalf("resume request = %+v", remote.resumeRequests[0])
	}
}

func TestTaskStartFailedSetupReturnsNonzeroAfterAppliedMutation(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	retained := lifecycleSetupTestWorktree("/tmp/start-retained")
	remote := &lifecycleCommandTestRemote{
		startResponse: serverapi.WorkflowTaskStartResponse{
			Outcome: serverapi.WorkflowTaskActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskStartApplied{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
			},
		},
		setupEvent: func(id serverapi.WorktreeSetupOperationID) serverapi.WorktreeSetupEvent {
			return serverapi.WorktreeSetupEvent{
				SetupOperationID: id,
				Phase:            serverapi.WorktreeSetupPhaseFailed,
				Failed: &serverapi.WorktreeSetupFailed{
					RetryReadiness: serverapi.WorktreeSetupRetryReady,
					Cause: serverapi.WorktreeSetupFailureCause{
						Kind:    serverapi.WorktreeSetupFailureTimeout,
						Timeout: &serverapi.WorktreeSetupTimeout{},
					},
					Diagnostic:       "setup timed out twice",
					ScriptPath:       lifecycleStringPointer("/repo/setup.sh"),
					ExecutionTarget:  lifecycleSetupExecutionTarget(serverapi.WorkflowExecutionTargetModeDefaultBranch),
					RetainedWorktree: &retained,
				},
			}
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskStartSubcommand([]string{"task-1", "--json"}, &stdout, &stderr)
	if exitCode != 1 || stdout.Len() != 0 || len(remote.startRequests) != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.startRequests)
	}
}

func TestClosedSetupStreamReturnsNonzeroInspectionOnlyOutcome(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	returned := make(chan struct{})
	remote := &lifecycleCommandTestRemote{
		startResponse: serverapi.WorkflowTaskStartResponse{
			Outcome: serverapi.WorkflowTaskActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskStartApplied{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
			},
		},
		subscription: eofWorktreeSetupSubscription{returned: returned},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskStartSubcommand([]string{"task-1"}, &stdout, &stderr)
	if exitCode != 1 || stdout.Len() != 0 || len(remote.startRequests) != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.startRequests)
	}
	<-returned
	presentation := taskLifecycleObservationPresentation(
		taskLifecycleOperationStart,
		taskLifecycleCommandContext{TaskRef: "task-1"},
		io.ErrUnexpectedEOF,
	)
	if presentation.Kind != taskLifecyclePresentationObservationFailed ||
		!reflect.DeepEqual(presentation.Actions, taskLifecycleInspectionActions("task-1", nil)) {
		t.Fatalf("presentation = %+v", presentation)
	}
}

func TestNonRetryableSetupFailureHasNoRecoveryActions(t *testing.T) {
	event := serverapi.WorktreeSetupEvent{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		Phase:            serverapi.WorktreeSetupPhaseFailed,
		Failed: &serverapi.WorktreeSetupFailed{
			RetryReadiness: serverapi.WorktreeSetupNonRetryable,
			Cause: serverapi.WorktreeSetupFailureCause{
				Kind:                    serverapi.WorktreeSetupFailureInterruptionPersistence,
				InterruptionPersistence: &serverapi.WorktreeSetupInterruptionPersistenceFailure{},
			},
			Diagnostic: "failed to persist interrupted Current Nodes",
		},
	}
	outcome, err := projectTaskLifecycleSetupOutcome(
		taskLifecycleOperationResume,
		taskLifecycleCommandContext{TaskRef: "KENT-453"},
		&worktreeSetupTerminalObservation{Event: event},
		nil,
	)
	if err != nil {
		t.Fatalf("projectTaskLifecycleSetupOutcome: %v", err)
	}
	if outcome.Success || outcome.Presentation == nil ||
		outcome.Presentation.Kind != taskLifecyclePresentationSetupFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
	for _, action := range outcome.Presentation.Actions {
		switch action.Kind {
		case taskLifecycleActionRetryCurrentTarget,
			taskLifecycleActionChooseNoWorktree,
			taskLifecycleActionChooseHead,
			taskLifecycleActionChooseDefaultBranch,
			taskLifecycleActionChooseCustomRef:
			t.Fatalf("non-retryable presentation contains recovery action %+v", action)
		}
	}
}

func TestAlreadyStartedPresentationUsesTypedResumeAndMoveGuidance(t *testing.T) {
	projectRef := "project-1"
	presentation := taskLifecycleAlreadyStartedPresentation("KENT-453", &projectRef)
	if presentation.Kind != taskLifecyclePresentationAlreadyStarted ||
		presentation.Operation != taskLifecycleOperationStart {
		t.Fatalf("presentation = %+v", presentation)
	}
	wantActions := []taskLifecycleAction{
		{
			Kind: taskLifecycleActionRetryCurrentTarget,
			Args: []string{"kent", "task", "resume", "KENT-453", "--project", "project-1"},
		},
	}
	if !reflect.DeepEqual(presentation.Actions, wantActions) {
		t.Fatalf("actions = %+v, want %+v", presentation.Actions, wantActions)
	}
	wantGuidance := []taskLifecycleGuidanceKind{taskLifecycleGuidanceMoveTask}
	if !reflect.DeepEqual(presentation.Guidance, wantGuidance) {
		t.Fatalf("guidance = %+v, want %+v", presentation.Guidance, wantGuidance)
	}
}

func TestTaskStartAlreadyStartedConflictReturnsTypedNonzeroPath(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	remote := &lifecycleCommandTestRemote{
		startError: &serverapi.WorkflowTaskStartConflictError{
			TaskID: "task-1",
			Reason: serverapi.WorkflowTaskStartConflictAlreadyStarted,
		},
		setupEvent: func(id serverapi.WorktreeSetupOperationID) serverapi.WorktreeSetupEvent {
			return serverapi.WorktreeSetupEvent{
				SetupOperationID: id,
				Phase:            serverapi.WorktreeSetupPhaseNotRequired,
				NotRequired: &serverapi.WorktreeSetupNotRequired{
					Reason: serverapi.WorktreeSetupNotRequiredNoTargetPreparation,
				},
			}
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskStartSubcommand([]string{"task-1", "--project", "project-1"}, &stdout, &stderr)
	if exitCode != 1 || stdout.Len() != 0 || len(remote.startRequests) != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.startRequests)
	}
}

func TestMoveSetupFailurePresentationReconstructsOriginalAndTargetOverrideActions(t *testing.T) {
	projectRef := "project-1"
	commentary := "operator note"
	transitionKey := "implement"
	valuesJSON := `{"plan":{"summary":"done"}}`
	currentTarget := "head"
	command := taskMoveRecoveryCommand{
		TaskRef:                    "KENT-453",
		TargetNodeID:               "implementation",
		ProjectRef:                 &projectRef,
		Commentary:                 &commentary,
		TransitionKey:              &transitionKey,
		ValuesJSON:                 &valuesJSON,
		ProceedDespiteDependencies: true,
		JSON:                       true,
		CurrentExecutionTarget:     &currentTarget,
	}
	retained := lifecycleSetupTestWorktree("/tmp/move-retained")
	preparationErr := &serverapi.WorkflowTaskMovePreparationError{
		Failure: serverapi.WorktreeSetupFailed{
			RetryReadiness: serverapi.WorktreeSetupRetryReady,
			Cause: serverapi.WorktreeSetupFailureCause{
				Kind:        serverapi.WorktreeSetupFailureOperational,
				Operational: &serverapi.WorktreeSetupOperationalFailure{},
			},
			Diagnostic:       "setup exited twice",
			ScriptPath:       lifecycleStringPointer("/repo/setup.sh"),
			RetainedWorktree: &retained,
		},
	}

	presentation, err := taskMovePreparationFailurePresentation(
		taskLifecycleCommandContext{TaskRef: "KENT-453", Move: &command},
		preparationErr,
	)
	if err != nil {
		t.Fatalf("taskMoveSetupFailurePresentation: %v", err)
	}
	if presentation.Kind != taskLifecyclePresentationSetupRecovery ||
		presentation.Operation != taskLifecycleOperationMove ||
		presentation.SetupScriptPath == nil ||
		*presentation.SetupScriptPath != "/repo/setup.sh" ||
		presentation.RetainedWorktree == nil ||
		presentation.RetainedWorktree.Path != "/tmp/move-retained" {
		t.Fatalf("presentation = %+v", presentation)
	}
	if len(presentation.Actions) != 5 {
		t.Fatalf("actions = %+v, want five Move recovery actions", presentation.Actions)
	}
	for index, action := range presentation.Actions {
		targetCount := 0
		for argIndex, arg := range action.Args {
			if arg == "--execution-target" {
				targetCount++
				if argIndex+1 >= len(action.Args) {
					t.Fatalf("action %d has incomplete execution target: %+v", index, action)
				}
			}
		}
		if targetCount != 1 {
			t.Fatalf("action %d execution target count = %d, action=%+v", index, targetCount, action)
		}
	}
	if got := presentation.Actions[0].Args[len(presentation.Actions[0].Args)-1]; got != "head" {
		t.Fatalf("current-target action = %+v", presentation.Actions[0])
	}
	if got := presentation.Actions[4].Args[len(presentation.Actions[4].Args)-1]; got != "ref:<revision>" {
		t.Fatalf("custom-ref action = %+v", presentation.Actions[4])
	}
}

func TestMovePreparationFailurePresentationRejectsBlankScriptPath(t *testing.T) {
	retained := lifecycleSetupTestWorktree("/tmp/move-retained")
	_, err := taskMovePreparationFailurePresentation(
		taskLifecycleCommandContext{
			TaskRef: "KENT-453",
			Move: &taskMoveRecoveryCommand{
				TaskRef:      "KENT-453",
				TargetNodeID: "implementation",
			},
		},
		&serverapi.WorkflowTaskMovePreparationError{
			Failure: serverapi.WorktreeSetupFailed{
				RetryReadiness: serverapi.WorktreeSetupRetryReady,
				Cause: serverapi.WorktreeSetupFailureCause{
					Kind:        serverapi.WorktreeSetupFailureOperational,
					Operational: &serverapi.WorktreeSetupOperationalFailure{},
				},
				Diagnostic:       "setup failed",
				ScriptPath:       lifecycleStringPointer(" "),
				RetainedWorktree: &retained,
			},
		},
	)
	if err == nil {
		t.Fatal("taskMoveSetupFailurePresentation accepted a blank setup script path")
	}
}

func TestMoveTargetPreparationFailurePresentsPreviousWorktreeWithoutPrimaryOrScript(t *testing.T) {
	previous := &serverapi.RetainedPreviousWorktree{
		Worktree: lifecycleSetupTestWorktree("/tmp/move-previous"),
	}
	presentation, err := taskMovePreparationFailurePresentation(
		taskLifecycleCommandContext{
			TaskRef: "KENT-453",
			Move: &taskMoveRecoveryCommand{
				TaskRef:                "KENT-453",
				TargetNodeID:           "implementation",
				CurrentExecutionTarget: lifecycleStringPointer("head"),
			},
		},
		&serverapi.WorkflowTaskMovePreparationError{
			Failure: serverapi.WorktreeSetupFailed{
				RetryReadiness: serverapi.WorktreeSetupRetryReady,
				Cause: serverapi.WorktreeSetupFailureCause{
					Kind:        serverapi.WorktreeSetupFailureTargetPreparation,
					Preparation: &serverapi.WorktreeSetupPreparationFailure{},
				},
				Diagnostic:               "replacement creation failed",
				RetainedPreviousWorktree: previous,
			},
		},
	)
	if err != nil {
		t.Fatalf("taskMovePreparationFailurePresentation: %v", err)
	}
	if presentation.Kind != taskLifecyclePresentationSetupRecovery ||
		presentation.SetupScriptPath != nil ||
		presentation.RetainedWorktree != nil ||
		presentation.RetainedPreviousWorktree == nil ||
		presentation.RetainedPreviousWorktree.Path != "/tmp/move-previous" {
		t.Fatalf("target-preparation presentation = %+v", presentation)
	}
	if len(presentation.Actions) <= len(taskMoveRecoveryActions(taskMoveRecoveryCommand{
		TaskRef:                "KENT-453",
		TargetNodeID:           "implementation",
		CurrentExecutionTarget: lifecycleStringPointer("head"),
	})) {
		t.Fatalf("target-preparation actions = %+v, want recovery and retained Worktree inspection", presentation.Actions)
	}
}

func TestTargetPreparationFailureDoesNotFabricateScriptOrWorktree(t *testing.T) {
	event := serverapi.WorktreeSetupEvent{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		Phase:            serverapi.WorktreeSetupPhaseFailed,
		Failed: &serverapi.WorktreeSetupFailed{
			RetryReadiness: serverapi.WorktreeSetupRetryReady,
			Cause: serverapi.WorktreeSetupFailureCause{
				Kind:        serverapi.WorktreeSetupFailureTargetPreparation,
				Preparation: &serverapi.WorktreeSetupPreparationFailure{},
			},
			Diagnostic:      "target revision could not be inspected",
			ExecutionTarget: lifecycleSetupExecutionTarget(serverapi.WorkflowExecutionTargetModeHead),
		},
	}
	outcome, err := projectTaskLifecycleSetupOutcome(
		taskLifecycleOperationResume,
		taskLifecycleCommandContext{
			TaskRef:    "KENT-453",
			ResumeArgs: []string{"kent", "task", "resume", "KENT-453"},
		},
		&worktreeSetupTerminalObservation{Event: event},
		nil,
	)
	if err != nil {
		t.Fatalf("projectTaskLifecycleSetupOutcome: %v", err)
	}
	if outcome.Success || outcome.Presentation == nil ||
		outcome.Presentation.Kind != taskLifecyclePresentationSetupRecovery ||
		outcome.Presentation.SetupScriptPath != nil ||
		outcome.Presentation.RetainedWorktree != nil {
		t.Fatalf("target-preparation outcome = %+v", outcome)
	}
}

func lifecycleSetupExecutionTarget(
	mode serverapi.WorkflowExecutionTargetMode,
) *serverapi.WorkflowExecutionTargetSelection {
	return &serverapi.WorkflowExecutionTargetSelection{Mode: mode}
}

func TestTaskMoveTypedSetupFailureReturnsNonzeroWithoutSetupSubscription(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	retained := lifecycleSetupTestWorktree("/tmp/move-primary")
	remote := &taskInterruptCommandRemote{
		moveError: &serverapi.WorkflowTaskMovePreparationError{
			Failure: serverapi.WorktreeSetupFailed{
				RetryReadiness: serverapi.WorktreeSetupRetryReady,
				Cause: serverapi.WorktreeSetupFailureCause{
					Kind:        serverapi.WorktreeSetupFailureOperational,
					Operational: &serverapi.WorktreeSetupOperationalFailure{},
				},
				Diagnostic:       "setup failed after retry",
				ScriptPath:       lifecycleStringPointer("/repo/setup.sh"),
				RetainedWorktree: &retained,
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskMoveSubcommand(
		[]string{
			"task-1",
			"done",
			"--execution-target",
			"head",
			"--commentary",
			"operator note",
			"--ignore-dependencies",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 1 || stdout.Len() != 0 || len(remote.moveRequests) != 1 || remote.setupSubscriptions != 0 {
		t.Fatalf(
			"exit=%d stdout=%q stderr=%q requests=%+v setup subscriptions=%d",
			exitCode,
			stdout.String(),
			stderr.String(),
			remote.moveRequests,
			remote.setupSubscriptions,
		)
	}
	request := remote.moveRequests[0]
	if request.ExecutionTarget == nil ||
		request.ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeHead ||
		request.Commentary != "operator note" ||
		!request.ProceedDespiteDependencies {
		t.Fatalf("Move request = %+v", request)
	}
}

func TestAppliedMoveRetainedPreviousWorktreeProjectsTypedInspectionWarning(t *testing.T) {
	retained := &serverapi.RetainedPreviousWorktree{
		Worktree: lifecycleSetupTestWorktree("/tmp/move-orphan"),
	}
	presentation, err := taskLifecycleRetainedWorktreePresentation(
		taskLifecycleOperationMove,
		"KENT-453",
		retained,
	)
	if err != nil {
		t.Fatalf("taskLifecycleRetainedWorktreePresentation: %v", err)
	}
	if presentation.RetainedPreviousWorktree.Path != "/tmp/move-orphan" {
		t.Fatalf("presentation = %+v", presentation)
	}
	wantActions := retainedWorktreeInspectionActions(presentation.RetainedPreviousWorktree)
	if !reflect.DeepEqual(presentation.Actions, wantActions) {
		t.Fatalf("actions = %+v, want %+v", presentation.Actions, wantActions)
	}
	if len(presentation.Actions) != 1 ||
		presentation.Actions[0].Kind != taskLifecycleActionListWorktrees {
		t.Fatalf("actions = %+v, want only Worktree inspection", presentation.Actions)
	}
}

func TestTaskMoveAppliedRetainedPreviousWorktreeKeepsJSONAndNoSetupSubscription(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	retained := &serverapi.RetainedPreviousWorktree{
		Worktree: lifecycleSetupTestWorktree("/tmp/move-orphan"),
	}
	remote := &taskInterruptCommandRemote{
		moveResponse: &serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskMoveApplied{
				CurrentNodes:             []serverapi.WorkflowTaskCurrentNode{{NodeID: "done"}},
				RetainedPreviousWorktree: retained,
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskMoveSubcommand([]string{"task-1", "done", "--json"}, &stdout, &stderr)
	if exitCode != 0 || len(remote.moveRequests) != 1 || remote.setupSubscriptions != 0 {
		t.Fatalf(
			"exit=%d stderr=%q requests=%+v setup subscriptions=%d",
			exitCode,
			stderr.String(),
			remote.moveRequests,
			remote.setupSubscriptions,
		)
	}
	var response serverapi.WorkflowTaskMoveResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode JSON Move response: %v", err)
	}
	if response.Applied == nil ||
		response.Applied.RetainedPreviousWorktree == nil ||
		response.Applied.RetainedPreviousWorktree.Worktree.Registered == nil ||
		response.Applied.RetainedPreviousWorktree.Worktree.Registered.Git.CanonicalRoot != "/tmp/move-orphan" {
		t.Fatalf("Move response = %+v", response)
	}
}

func lifecycleSetupTestWorktree(root string) serverapi.WorktreeTopologyEntry {
	return serverapi.WorktreeTopologyEntry{
		Variant: serverapi.WorktreeTopologyVariantRegistered,
		Registered: &serverapi.WorktreeRegisteredFacts{
			Git: serverapi.WorktreeGitFacts{
				CanonicalRoot: root,
				HeadObject:    "0123456789abcdef",
			},
			Kent: serverapi.WorktreeKentFacts{
				WorktreeID:    "worktree-1",
				CanonicalRoot: root,
				DisplayName:   "KENT-453",
				Managed:       true,
			},
		},
	}
}
