package workflowstore

import (
	"context"
	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/runtimeids"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKENT345StaleReviewIsNeverRevived(t *testing.T) {
	for _, test := range []struct {
		name           string
		contextSource  workflow.ContextSourceKind
		requiresManual bool
	}{
		{name: "previous target or new", contextSource: workflow.ContextSourcePreviousTargetOrNew},
		{name: "manual watcher re-entry", contextSource: workflow.ContextSourcePreviousTargetOrNew, requiresManual: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newKENT345Fixture(t, test.contextSource)
			review, implementationBSessionID := fixture.supersedeImplementation(t)
			var target workflow.CurrentNode
			if test.requiresManual {
				moved := applyManualMoveFixture(t, fixture.ctx, fixture.store, fixture.binding, ManualMoveRequest{
					TaskID:       fixture.taskID,
					TargetNodeID: fixture.watcherNodeID,
				})
				target = moved.Mutation.Created[0]
			} else {
				result, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
					Source:       fixture.implementation.Reference,
					TransitionID: "watch",
				})
				if err != nil {
					t.Fatalf("CompleteCurrentNode Implementation B: %v", err)
				}
				target = result.Mutation.Created[0]
			}
			reentry, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
				Source: target.Reference, TransitionID: "watcher_review",
			})
			if err != nil {
				t.Fatalf("CompleteCurrentNode watcher: %v", err)
			}
			freshReview := reentry.Mutation.Created[0]
			if freshReview.SessionID != nil && *freshReview.SessionID == review {
				t.Fatalf("watcher revived stale Review Session %q", review)
			}
			sourceID, exact := freshReview.ContinuationSource.ExactSessionID()
			if !exact || sourceID != implementationBSessionID {
				t.Fatalf("watcher source = %q, %v; want Implementation B %q", sourceID, exact, implementationBSessionID)
			}
		})
	}
	t.Run("interruption and resume preserve fresh target", func(t *testing.T) {
		fixture := newKENT345Fixture(t, workflow.ContextSourcePreviousTargetOrNew)
		staleReviewSessionID, implementationBSessionID := fixture.supersedeImplementation(t)
		watcher, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
			Source: fixture.implementation.Reference, TransitionID: "watch",
		})
		if err != nil {
			t.Fatalf("CompleteCurrentNode Implementation B: %v", err)
		}
		reentry, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
			Source: watcher.Mutation.Created[0].Reference, TransitionID: "watcher_review",
		})
		if err != nil {
			t.Fatalf("CompleteCurrentNode watcher: %v", err)
		}
		fresh := reentry.Mutation.Created[0]
		freshSessionID := associateAndBindCurrentNodeSessionForTest(
			t, fixture.ctx, fixture.store, fixture.binding, fixture.cfg, fresh.Reference,
		)
		if freshSessionID == staleReviewSessionID {
			t.Fatalf("fresh Review Session = stale Session %q", staleReviewSessionID)
		}
		if err := fixture.store.InterruptCurrentNode(
			fixture.ctx,
			fresh.Reference,
			workflow.CurrentNodeInterruptionReasonUserInterrupt,
			workflow.CurrentNodeInterruptionDetail{Code: string(workflow.CurrentNodeInterruptionReasonUserInterrupt)},
		); err != nil {
			t.Fatalf("InterruptCurrentNode: %v", err)
		}
		if _, _, err := fixture.store.ResumeCurrentNode(fixture.ctx, fresh.Reference); err != nil {
			t.Fatalf("ResumeCurrentNode: %v", err)
		}
		current, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.taskID)
		if err != nil {
			t.Fatalf("ListCurrentNodes: %v", err)
		}
		sourceID, exact := current[0].ContinuationSource.ExactSessionID()
		if len(current) != 1 || current[0].SessionID == nil || *current[0].SessionID != freshSessionID ||
			!exact || sourceID != implementationBSessionID {
			t.Fatalf("resumed Review = %+v", current)
		}
	})
	t.Run("pending Approval freezes fresh target", func(t *testing.T) {
		fixture := newKENT345Fixture(t, workflow.ContextSourcePreviousTargetOrNew)
		staleReviewSessionID, implementationBSessionID := fixture.supersedeImplementation(t)
		definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		watcherReviewEdgeID := edgeByKey(t, definition, "watcher_review").ID
		saveWorkflowGraphFixture(t, fixture.ctx, fixture.store, fixture.workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
			workflowGraphSaveEdgeRecord(t, req.Edges, watcherReviewEdgeID).RequiresApproval = true
		})
		watcher, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
			Source: fixture.implementation.Reference, TransitionID: "watch",
		})
		if err != nil {
			t.Fatalf("CompleteCurrentNode Implementation B: %v", err)
		}
		reentry, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
			Source: watcher.Mutation.Created[0].Reference, TransitionID: "watcher_review",
		})
		if err != nil {
			t.Fatalf("CompleteCurrentNode watcher: %v", err)
		}
		if reentry.PendingApproval == nil {
			t.Fatal("watcher re-entry did not create pending Approval")
		}
		frozen := reentry.PendingApproval.Branches[0].Target.CurrentNode
		sourceID, exact := frozen.ContinuationSource.ExactSessionID()
		if frozen.SessionID != nil && *frozen.SessionID == staleReviewSessionID {
			t.Fatalf("pending Approval froze stale Review Session %q", staleReviewSessionID)
		}
		if !exact || sourceID != implementationBSessionID {
			t.Fatalf("pending Approval source = %q, %v; want Implementation B %q", sourceID, exact, implementationBSessionID)
		}
		applied, err := fixture.store.ApplyPendingApproval(fixture.ctx, reentry.PendingApproval.ID)
		if err != nil {
			t.Fatalf("ApplyPendingApproval: %v", err)
		}
		appliedSourceID, exact := applied.Mutation.Created[0].ContinuationSource.ExactSessionID()
		if !exact || appliedSourceID != implementationBSessionID {
			t.Fatalf("applied Approval source = %q, %v; want Implementation B %q", appliedSourceID, exact, implementationBSessionID)
		}
	})
}

type kent345Fixture struct {
	ctx             context.Context
	store           *Store
	binding         metadata.Binding
	cfg             config.App
	taskID          workflow.TaskID
	workflowID      runtimeids.WorkflowID
	implementation  workflow.CurrentNode
	watcherNodeID   workflow.NodeID
	firstReview     workflow.CurrentNode
	implementationA runtimeids.SessionID
}

func newKENT345Fixture(t *testing.T, contextSource workflow.ContextSourceKind) kent345Fixture {
	t.Helper()
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	scriptPath := filepath.Join(binding.CanonicalRoot, "watch-pr")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write watcher fixture: %v", err)
	}
	workflowID := createKENT345Workflow(t, ctx, store, contextSource)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	implementation := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	implementationA := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, implementation.Reference)
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source: implementation.Reference, TransitionID: "initial_review",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode Implementation A: %v", err)
	}
	firstReview := reviewResult.Mutation.Created[0]
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: implementationA, CurrentNode: firstReview.Reference,
			AssociatedAt: time.UnixMilli(1_700_000_000_001).UTC(),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode Review A: %v", err)
	}
	return kent345Fixture{
		ctx: ctx, store: store, binding: binding, cfg: cfg, taskID: task.ID, workflowID: workflowID,
		implementation: implementation, watcherNodeID: workflow.NodeID("node-pr-watcher-" + workflowID.String()),
		firstReview: firstReview, implementationA: implementationA,
	}
}
func (f *kent345Fixture) supersedeImplementation(t *testing.T) (runtimeids.SessionID, runtimeids.SessionID) {
	t.Helper()
	reimplementation, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source: f.firstReview.Reference, TransitionID: "reimplement",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode Review A: %v", err)
	}
	f.implementation = reimplementation.Mutation.Created[0]
	implementationB := associateAndBindCurrentNodeSessionForTest(
		t, f.ctx, f.store, f.binding, f.cfg, f.implementation.Reference,
	)
	if _, err := f.store.CurrentTaskSessionForNode(f.ctx, f.firstReview.Reference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale Review remains current: %v", err)
	}
	return f.implementationA, implementationB
}
func createKENT345Workflow(
	t *testing.T,
	ctx context.Context,
	store *Store,
	retainedTargetSource workflow.ContextSourceKind,
) runtimeids.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "KENT-345 regression"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var startID, doneID workflow.NodeID
	for _, node := range definition.Nodes {
		switch node.Kind() {
		case workflow.NodeKindStart:
			startID = workflow.NodeIDOf(node)
		case workflow.NodeKindTerminal:
			doneID = workflow.NodeIDOf(node)
		}
	}
	suffix := created.ID.String()
	implementationID := workflow.NodeID("node-implementation-" + suffix)
	reviewID := workflow.NodeID("node-pr-autoreview-" + suffix)
	watcherID := workflow.NodeID("node-pr-watcher-" + suffix)
	group := func(name string) workflow.TransitionGroupID {
		return workflow.TransitionGroupID("group-" + name + "-" + suffix)
	}
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: implementationID, WorkflowID: created.ID, Key: "implementation", Kind: workflow.NodeKindAgent, DisplayName: "Implementation", SubagentRole: "coder"},
			NodeRecord{ID: reviewID, WorkflowID: created.ID, Key: "pr_autoreview", Kind: workflow.NodeKindAgent, DisplayName: "PR Autoreview", SubagentRole: "coder"},
			NodeRecord{ID: watcherID, WorkflowID: created.ID, Key: "pr_watcher", Kind: workflow.NodeKindScript, DisplayName: "PR Watcher", ScriptPath: "watch-pr"},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: group("start"), WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: group("review"), WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "initial_review", DisplayName: "Review"},
			TransitionGroupRecord{ID: group("reimplement"), WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "reimplement", DisplayName: "Reimplement"},
			TransitionGroupRecord{ID: group("watch"), WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "watch", DisplayName: "Watch"},
			TransitionGroupRecord{ID: group("watcher-review"), WorkflowID: created.ID, SourceNodeID: watcherID, TransitionID: "watcher_review", DisplayName: "Review"},
			TransitionGroupRecord{ID: group("done"), WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			kent345Edge(created.ID, workflow.EdgeID("edge-start-"+suffix), group("start"), "start", implementationID, workflow.ContextModeNewSession, workflow.ContextSource{}, "Implement."),
			kent345Edge(created.ID, workflow.EdgeID("edge-review-"+suffix), group("review"), "initial_review", reviewID, workflow.ContextModeContinueSession, workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}, "Review."),
			kent345Edge(created.ID, workflow.EdgeID("edge-reimplement-"+suffix), group("reimplement"), "reimplement", implementationID, workflow.ContextModeNewSession, workflow.ContextSource{}, "Reimplement."),
			kent345Edge(created.ID, workflow.EdgeID("edge-watch-"+suffix), group("watch"), "watch", watcherID, workflow.ContextModeNewSession, workflow.ContextSource{}, ""),
			kent345Edge(created.ID, workflow.EdgeID("edge-watcher-review-"+suffix), group("watcher-review"), "watcher_review", reviewID, workflow.ContextModeContinueSession, workflow.ContextSource{Kind: retainedTargetSource}, "Review updated implementation."),
			kent345Edge(created.ID, workflow.EdgeID("edge-done-"+suffix), group("done"), "done", doneID, workflow.ContextModeNewSession, workflow.ContextSource{}, ""),
		)
	})
	return created.ID
}
func kent345Edge(
	workflowID runtimeids.WorkflowID,
	id workflow.EdgeID,
	groupID workflow.TransitionGroupID,
	key workflow.ModelKey,
	target workflow.NodeID,
	mode workflow.ContextMode,
	source workflow.ContextSource,
	prompt string,
) EdgeRecord {
	return EdgeRecord{
		ID: id, WorkflowID: workflowID, TransitionGroupID: groupID, Key: key,
		TargetNodeID: target, ContextMode: mode, ContextSource: source, PromptTemplate: prompt,
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
	}
}
