package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestWorkflowTaskRunWaitReportsProjectedInterruptedAttention(t *testing.T) {
	message := "target resolution failed"
	detail := `{"code":"workflow_execution_target_resolution_failed"}`
	target := serverapi.WorkflowTaskCurrentNode{
		NodeID:              "node-1",
		TransitionBranchKey: nil,
	}
	item := serverapi.WorkflowAttentionItem{
		Kind:        serverapi.WorkflowAttentionItemKindInterruptedCurrentNode,
		CurrentNode: &target,
		Message:     &message,
		DetailJSON:  &detail,
	}
	attention, found := interruptedWorkflowTaskRunTarget(
		[]serverapi.WorkflowAttentionItem{item},
		[]serverapi.WorkflowTaskCurrentNode{target},
	)
	if !found || attention.Message == nil || *attention.Message != message {
		t.Fatalf("interrupted attention = %+v, found = %t", attention, found)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	writeWorkflowTaskRunWaitError(&stdout, &stderr, false, &workflowTaskRunWaitInterruption{attention: attention})
	if got := stdout.String(); got != message+"\n"+detail+"\n" {
		t.Fatalf("stdout = %q, want existing interruption fields", got)
	}
}

func TestWorkflowTaskRunWaitErrorKeepsJSONOnStdout(t *testing.T) {
	message := "target resolution failed"
	attention := serverapi.WorkflowAttentionItem{Message: &message, WorkflowID: runtimeids.NewWorkflowID()}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	writeWorkflowTaskRunWaitError(&stdout, &stderr, true, &workflowTaskRunWaitInterruption{attention: attention})
	if stdout.String() == "" || stderr.String() != "" {
		t.Fatalf("stdout = %q, stderr = %q, want JSON stdout and empty stderr", stdout.String(), stderr.String())
	}
}

func TestWorkflowTaskRunWaitErrorFallsBackWhenAttentionHasNoDetails(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := &workflowTaskRunWaitInterruption{}
	writeWorkflowTaskRunWaitError(&stdout, &stderr, false, err)
	if stdout.String() != "" || stderr.String() != err.Error()+"\n" {
		t.Fatalf("stdout = %q, stderr = %q, want fallback error on stderr", stdout.String(), stderr.String())
	}
}

func TestWorkflowMutationSetupProgressSurvivesFastMutationResponse(t *testing.T) {
	events := make(chan serverapi.WorktreeSetupEvent, 1)
	delivered := make(chan struct{})
	remote := &delayedSetupProgressRemote{
		events:    events,
		delivered: delivered,
	}
	var stderr bytes.Buffer
	_, stop, err := runWorkflowMutationWithSetupProgress(
		context.Background(),
		remote,
		&stderr,
		func(_ context.Context, _ serverapi.WorktreeSetupOperationID) (struct{}, error) {
			return struct{}{}, nil
		},
	)
	if err != nil {
		t.Fatalf("runWorkflowMutationWithSetupProgress: %v", err)
	}
	events <- serverapi.WorktreeSetupEvent{
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SourceWorkspaceRoot: "/source",
		WorktreeRoot:        "/worktree",
		ScriptPath:          "setup.sh",
		Phase:               serverapi.WorktreeSetupPhaseStarted,
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("setup subscription ended before deferred setup started")
	}
	if err := stop(); err != nil {
		t.Fatalf("stop setup progress: %v", err)
	}
}

func TestWorkflowTaskRunWaitProbesAttentionAfterAcknowledgementTimeout(t *testing.T) {
	target := serverapi.WorkflowTaskCurrentNode{NodeID: "node-1"}
	message := "target resolution failed"
	remote := &interruptionProbeRemote{
		target: target,
		item: serverapi.WorkflowAttentionItem{
			Kind:        serverapi.WorkflowAttentionItemKindInterruptedCurrentNode,
			CurrentNode: &target,
			Message:     &message,
		},
	}
	_, err := waitForWorkflowTaskRunSession(
		context.Background(),
		remote,
		"task-1",
		[]serverapi.WorkflowTaskCurrentNode{target},
		10*time.Millisecond,
		time.Millisecond,
	)
	var interruption *workflowTaskRunWaitInterruption
	if !errors.As(err, &interruption) {
		t.Fatalf("wait error = %v, want projected interruption", err)
	}
	if interruption.attention.Message == nil || *interruption.attention.Message != message {
		t.Fatalf("interruption attention = %+v", interruption.attention)
	}
}

type delayedSetupProgressRemote struct {
	apicontract.WorkflowService
	events    <-chan serverapi.WorktreeSetupEvent
	delivered chan<- struct{}
}

type interruptionProbeRemote struct {
	apicontract.WorkflowService
	target serverapi.WorkflowTaskCurrentNode
	item   serverapi.WorkflowAttentionItem
	calls  int
}

func (r *interruptionProbeRemote) ResolveProjectPath(
	context.Context,
	serverapi.ProjectResolvePathRequest,
) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, errors.New("unused")
}

func (r *interruptionProbeRemote) Close() error {
	return nil
}

func (r *interruptionProbeRemote) GetWorkflowTask(
	_ context.Context,
	req serverapi.WorkflowTaskGetRequest,
) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{
		Task: serverapi.WorkflowTaskDetail{
			Summary:      serverapi.WorkflowTaskSummary{ID: req.TaskID, ShortID: "KENT-1"},
			CurrentNodes: []serverapi.WorkflowTaskCurrentNode{r.target},
		},
	}, nil
}

func (r *interruptionProbeRemote) ListWorkflowTaskAttention(
	_ context.Context,
	_ serverapi.WorkflowTaskAttentionListRequest,
) (serverapi.WorkflowTaskAttentionListResponse, error) {
	r.calls++
	if r.calls < 2 {
		return serverapi.WorkflowTaskAttentionListResponse{}, nil
	}
	return serverapi.WorkflowTaskAttentionListResponse{
		Items: []serverapi.WorkflowAttentionItem{r.item},
	}, nil
}

func (r *delayedSetupProgressRemote) ResolveProjectPath(
	context.Context,
	serverapi.ProjectResolvePathRequest,
) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, errors.New("unused")
}

func (r *delayedSetupProgressRemote) Close() error {
	return nil
}

func (r *delayedSetupProgressRemote) SubscribeWorktreeSetup(
	context.Context,
	serverapi.WorktreeSetupSubscribeRequest,
) (serverapi.WorktreeSetupSubscription, error) {
	return delayedSetupProgressSubscription{events: r.events, delivered: r.delivered}, nil
}

type delayedSetupProgressSubscription struct {
	events    <-chan serverapi.WorktreeSetupEvent
	delivered chan<- struct{}
}

func (s delayedSetupProgressSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	select {
	case event := <-s.events:
		close(s.delivered)
		return event, nil
	case <-ctx.Done():
		return serverapi.WorktreeSetupEvent{}, context.Cause(ctx)
	}
}

func (delayedSetupProgressSubscription) Close() error {
	return nil
}
