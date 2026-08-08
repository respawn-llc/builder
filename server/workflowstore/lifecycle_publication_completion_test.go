package workflowstore

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestLifecyclePublicationCompletesSerialCurrentNodeWithSuccessorRunBeforeSourceFinalization(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		ctx,
		task.ID,
		testsetup.PreparedPublicationStage(NewTaskStartLifecycleDelta),
	)
	if err != nil {
		t.Fatalf("PublishTaskStart: %v", err)
	}
	source := started.Mutation.Created[0].Reference
	sourceScope := runtimeids.NewExecutionScopeID()
	if err := publication.PublishExactRegistration(ctx, LifecycleExactExecution{
		ProjectID:   binding.ProjectID,
		WorkflowID:  workflowID,
		CurrentNode: source,
		ScopeID:     sourceScope,
		Script:      &LifecycleScriptExecutionTarget{Path: "/test/script"},
		Phase:       LifecycleExactExecutionRunning,
	}, &lifecycleExactActivation{}); err != nil {
		t.Fatalf("PublishExactRegistration: %v", err)
	}

	completed, _, err := publication.PublishCurrentNodeCompletion(ctx, CurrentNodeCompletionRequest{
		Source:       source,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	}, func(result CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error) {
		if len(result.AutomaticIntents) != 1 {
			t.Fatalf("completion intents = %+v, want one successor", result.AutomaticIntents)
		}
		target := result.AutomaticIntents[0].CurrentNode
		delta, err := NewTaskLifecycleDelta(task.ID, []LifecycleRunDelta{
			{CurrentNode: source, Expect: LifecycleFieldPresent, Next: LifecycleFieldPresent},
			{CurrentNode: target, Expect: LifecycleFieldAbsent, Next: LifecycleFieldPresent},
		}, nil)
		return delta, nil, err
	})
	if err != nil {
		t.Fatalf("PublishCurrentNodeCompletion: %v", err)
	}
	if len(completed.Mutation.Created) != 1 {
		t.Fatalf("completion mutation = %+v, want one successor", completed.Mutation)
	}
	target := completed.Mutation.Created[0].Reference
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	currentNodes, err := capture.CurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("CurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(target) {
		t.Fatalf("captured Current Nodes = %+v, want successor %v", currentNodes, target)
	}
	queued := capture.QueuedCurrentNodes(task.ID)
	if len(queued) != 1 || !queued[0].Equal(target) {
		t.Fatalf("captured queued Current Nodes = %+v, want successor %v", queued, target)
	}
	if exact := capture.ExactExecutions(task.ID); len(exact) != 1 ||
		!exact[0].CurrentNode.Equal(source) ||
		exact[0].ScopeID != sourceScope {
		t.Fatalf("captured Exact executions = %+v, want source retained through finalization", exact)
	}
}

func TestLifecyclePublicationAppliesPendingApprovalWithSuccessorRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID).RequiresApproval = true
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		ctx,
		task.ID,
		testsetup.PreparedPublicationStage(NewTaskStartLifecycleDelta),
	)
	if err != nil {
		t.Fatalf("PublishTaskStart: %v", err)
	}
	source := started.Mutation.Created[0].Reference
	completed, _, err := publication.PublishCurrentNodeCompletion(ctx, CurrentNodeCompletionRequest{
		Source:       source,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "ready"},
	}, func(result CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error) {
		delta, err := NewTaskLifecycleDelta(task.ID, []LifecycleRunDelta{{
			CurrentNode: source,
			Expect:      LifecycleFieldPresent,
			Next:        LifecycleFieldPresent,
		}}, nil)
		return delta, nil, err
	})
	if err != nil {
		t.Fatalf("PublishCurrentNodeCompletion: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("completion did not create a pending Approval")
	}

	applied, err := publication.PublishPendingApproval(
		ctx,
		completed.PendingApproval.ID,
		func(result PendingApprovalApplyResult) (TaskLifecycleDelta, func(error), error) {
			target := result.Mutation.Created[0].Reference
			delta, err := NewTaskLifecycleDelta(task.ID, []LifecycleRunDelta{
				{CurrentNode: source, Expect: LifecycleFieldPresent, Next: LifecycleFieldPresent},
				{CurrentNode: target, Expect: LifecycleFieldAbsent, Next: LifecycleFieldPresent},
			}, nil)
			return delta, nil, err
		},
	)
	if err != nil {
		t.Fatalf("PublishPendingApproval: %v", err)
	}
	target := applied.Mutation.Created[0].Reference
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	currentNodes, err := capture.CurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("CurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(target) {
		t.Fatalf("captured Current Nodes = %+v, want approved target %v", currentNodes, target)
	}
	queued := capture.QueuedCurrentNodes(task.ID)
	hasQueued := func(reference workflow.CurrentNodeReference) bool {
		for _, candidate := range queued {
			if candidate.Equal(reference) {
				return true
			}
		}
		return false
	}
	if len(queued) != 2 || !hasQueued(source) || !hasQueued(target) {
		t.Fatalf("captured queued Current Nodes = %+v, want source and approved target", queued)
	}
}

func TestLifecyclePublicationAppliesManualMoveWithTargetRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0].Reference
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	targetNode := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(targetNode),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	moved, err := publication.PublishManualMove(
		ctx,
		prepared,
		&ExecutionTargetCandidate{
			Snapshot: ExecutionTargetSnapshot{
				Mode:       workflow.ExecutionTargetModeNone,
				Provenance: ExecutionTargetProvenanceResolved,
			},
			Root: ExecutionRoot{
				SourceWorkspaceID:   binding.WorkspaceID,
				SourceWorkspaceRoot: binding.CanonicalRoot,
			},
		},
		func(result ManualMoveResult) (TaskLifecycleDelta, func(error), error) {
			target := result.Mutation.Created[0].Reference
			delta, err := NewTaskLifecycleDelta(task.ID, []LifecycleRunDelta{
				{CurrentNode: source, Expect: LifecycleFieldAbsent, Next: LifecycleFieldAbsent},
				{CurrentNode: target, Expect: LifecycleFieldAbsent, Next: LifecycleFieldPresent},
			}, nil)
			return delta, nil, err
		},
	)
	if err != nil {
		t.Fatalf("PublishManualMove: %v", err)
	}
	target := moved.Mutation.Created[0].Reference
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	currentNodes, err := capture.CurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("CurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(target) {
		t.Fatalf("captured Current Nodes = %+v, want manual-move target %v", currentNodes, target)
	}
	queued := capture.QueuedCurrentNodes(task.ID)
	if len(queued) != 1 || !queued[0].Equal(target) {
		t.Fatalf("captured queued Current Nodes = %+v, want target %v", queued, target)
	}
}

func TestLifecyclePublicationPublishesEverySameKindFanoutRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		ctx,
		task.ID,
		testsetup.PreparedPublicationStage(NewTaskStartLifecycleDelta),
	)
	if err != nil {
		t.Fatalf("PublishTaskStart: %v", err)
	}
	source := started.Mutation.Created[0].Reference
	completed, _, err := publication.PublishCurrentNodeCompletion(ctx, CurrentNodeCompletionRequest{
		Source:       source,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "ready"},
	}, func(result CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error) {
		changes := []LifecycleRunDelta{{
			CurrentNode: source,
			Expect:      LifecycleFieldPresent,
			Next:        LifecycleFieldPresent,
		}}
		for _, intent := range result.AutomaticIntents {
			changes = append(changes, LifecycleRunDelta{
				CurrentNode: intent.CurrentNode,
				Expect:      LifecycleFieldAbsent,
				Next:        LifecycleFieldPresent,
			})
		}
		delta, err := NewTaskLifecycleDelta(task.ID, changes, nil)
		return delta, nil, err
	})
	if err != nil {
		t.Fatalf("PublishCurrentNodeCompletion: %v", err)
	}
	if len(completed.AutomaticIntents) != 2 {
		t.Fatalf("fanout intents = %+v, want two successors", completed.AutomaticIntents)
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	queued := capture.QueuedCurrentNodes(task.ID)
	if len(queued) != 3 {
		t.Fatalf("captured queued Current Nodes = %+v, want source plus two fanout successors", queued)
	}
	for _, intent := range completed.AutomaticIntents {
		found := false
		for _, reference := range queued {
			found = found || reference.Equal(intent.CurrentNode)
		}
		if !found {
			t.Fatalf("captured queued Current Nodes = %+v, missing fanout successor %v", queued, intent.CurrentNode)
		}
	}
}

func TestLifecyclePublicationKeepsNonFinalJoinBlockedAndPublishesFinalJoinRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		ctx,
		task.ID,
		testsetup.PreparedPublicationStage(NewTaskStartLifecycleDelta),
	)
	if err != nil {
		t.Fatalf("PublishTaskStart: %v", err)
	}
	source := started.Mutation.Created[0].Reference
	split, _, err := publication.PublishCurrentNodeCompletion(ctx, CurrentNodeCompletionRequest{
		Source:       source,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "ready"},
	}, func(result CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error) {
		changes := []LifecycleRunDelta{{
			CurrentNode: source,
			Expect:      LifecycleFieldPresent,
			Next:        LifecycleFieldPresent,
		}}
		for _, intent := range result.AutomaticIntents {
			changes = append(changes, LifecycleRunDelta{
				CurrentNode: intent.CurrentNode,
				Expect:      LifecycleFieldAbsent,
				Next:        LifecycleFieldPresent,
			})
		}
		delta, err := NewTaskLifecycleDelta(task.ID, changes, nil)
		return delta, nil, err
	})
	if err != nil {
		t.Fatalf("PublishCurrentNodeCompletion split: %v", err)
	}
	if err := publication.Publish(ctx, mustLifecycleDelta(t, task.ID, []LifecycleRunDelta{{
		CurrentNode: source,
		Expect:      LifecycleFieldPresent,
		Next:        LifecycleFieldAbsent,
	}})); err != nil {
		t.Fatalf("retire split source Run: %v", err)
	}
	first, second := split.Mutation.Created[0], split.Mutation.Created[1]
	firstTransition := "join_a"
	secondTransition := "join_b"
	if branch, _ := first.Reference.TransitionBranchKey(); branch == "split_b" {
		firstTransition, secondTransition = secondTransition, firstTransition
	}
	firstArrival, _, err := publication.PublishCurrentNodeCompletion(ctx, CurrentNodeCompletionRequest{
		Source:       first.Reference,
		TransitionID: firstTransition,
		OutputValues: map[string]string{"joined": "branch complete"},
	}, func(result CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error) {
		if len(result.AutomaticIntents) != 0 {
			t.Fatalf("non-final Join intents = %+v, want none", result.AutomaticIntents)
		}
		delta, err := NewTaskLifecycleDelta(task.ID, []LifecycleRunDelta{{
			CurrentNode: first.Reference,
			Expect:      LifecycleFieldPresent,
			Next:        LifecycleFieldPresent,
		}}, nil)
		return delta, nil, err
	})
	if err != nil {
		t.Fatalf("PublishCurrentNodeCompletion first Join arrival: %v", err)
	}
	if len(firstArrival.AutomaticIntents) != 0 {
		t.Fatalf("non-final Join result = %+v, want blocked without successor", firstArrival)
	}
	if err := publication.Publish(ctx, mustLifecycleDelta(t, task.ID, []LifecycleRunDelta{{
		CurrentNode: first.Reference,
		Expect:      LifecycleFieldPresent,
		Next:        LifecycleFieldAbsent,
	}})); err != nil {
		t.Fatalf("retire first Join branch Run: %v", err)
	}
	finalArrival, _, err := publication.PublishCurrentNodeCompletion(ctx, CurrentNodeCompletionRequest{
		Source:       second.Reference,
		TransitionID: secondTransition,
	}, func(result CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error) {
		if len(result.AutomaticIntents) != 1 {
			t.Fatalf("final Join intents = %+v, want one successor", result.AutomaticIntents)
		}
		delta, err := NewTaskLifecycleDelta(task.ID, []LifecycleRunDelta{
			{CurrentNode: second.Reference, Expect: LifecycleFieldPresent, Next: LifecycleFieldPresent},
			{CurrentNode: result.AutomaticIntents[0].CurrentNode, Expect: LifecycleFieldAbsent, Next: LifecycleFieldPresent},
		}, nil)
		return delta, nil, err
	})
	if err != nil {
		t.Fatalf("PublishCurrentNodeCompletion final Join arrival: %v", err)
	}
	target := finalArrival.AutomaticIntents[0].CurrentNode
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	currentNodes, err := capture.CurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("CurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(target) {
		t.Fatalf("captured Current Nodes = %+v, want final Join successor %v", currentNodes, target)
	}
	queued := capture.QueuedCurrentNodes(task.ID)
	if len(queued) != 2 {
		t.Fatalf("captured queued Current Nodes = %+v, want final branch and successor until finalization", queued)
	}
}

func mustLifecycleDelta(
	t *testing.T,
	taskID workflow.TaskID,
	runs []LifecycleRunDelta,
) TaskLifecycleDelta {
	t.Helper()
	delta, err := NewTaskLifecycleDelta(taskID, runs, nil)
	if err != nil {
		t.Fatalf("NewTaskLifecycleDelta: %v", err)
	}
	return delta
}
