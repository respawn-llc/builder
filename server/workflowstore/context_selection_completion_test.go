package workflowstore

import (
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/session"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/runtimeids"
)

func TestAutomaticCompletionPreservesRetainedTargetSessionRole(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	audit := nodeByKey(t, definition, "audit")
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	targetReference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(audit), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference target: %v", err)
	}
	targetSessionID := associateTaskSessionForTest(t, ctx, store, binding, cfg, targetReference, time.UnixMilli(2))
	setPersistedSessionRoleForTest(t, cfg, binding, store.metadata, targetSessionID, "reviewer")

	review, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("complete review with retained target session: %v", err)
	}
	if len(completed.Mutation.Created) != 1 {
		t.Fatalf("completion mutation = %+v, want one target", completed.Mutation)
	}
	target := completed.Mutation.Created[0]
	if target.SessionID == nil || *target.SessionID != targetSessionID {
		t.Fatalf("target session = %v, want retained %q", target.SessionID, targetSessionID)
	}
	if target.AgentExecutionSelection == nil ||
		target.AgentExecutionSelection.Assignee != "reviewer" ||
		target.AgentExecutionSelection.Origin != workflow.AssigneeOriginRetainedSession {
		t.Fatalf("target execution selection = %+v, want retained reviewer", target.AgentExecutionSelection)
	}
}

func TestConvergingIncomingEdgesKeepIndependentSelections(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	alternateID := workflow.NodeID("node-alternate-" + workflowID.String())
	alternateGroupID := workflow.TransitionGroupID("group-alternate-" + workflowID.String())
	alternateDoneGroupID := workflow.TransitionGroupID("group-alternate-done-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		plan := nodeByKey(t, def, "plan")
		audit := nodeByKey(t, def, "audit")
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:             alternateID,
			WorkflowID:     workflowID,
			Key:            "alternate",
			Kind:           workflow.NodeKindAgent,
			DisplayName:    "Alternate",
			SubagentRole:   "coder",
			PromptTemplate: "Alternate.",
		})
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{
				ID:           alternateGroupID,
				WorkflowID:   workflowID,
				SourceNodeID: workflow.NodeIDOf(plan),
				TransitionID: "alternate",
				DisplayName:  "Alternate",
			},
			TransitionGroupRecord{
				ID:           alternateDoneGroupID,
				WorkflowID:   workflowID,
				SourceNodeID: alternateID,
				TransitionID: "alternate_audit",
				DisplayName:  "Audit",
			},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{
				ID:                workflow.EdgeID("edge-plan-alternate-" + workflowID.String()),
				WorkflowID:        workflowID,
				TransitionGroupID: alternateGroupID,
				Key:               "alternate",
				TargetNodeID:      alternateID,
				ContextMode:       workflow.ContextModeNewSession,
				PromptTemplate:    "Alternate.",
			},
			EdgeRecord{
				ID:                workflow.EdgeID("edge-alternate-audit-" + workflowID.String()),
				WorkflowID:        workflowID,
				TransitionGroupID: alternateDoneGroupID,
				Key:               "audit",
				TargetNodeID:      workflow.NodeIDOf(audit),
				ContextMode:       workflow.ContextModeNewSession,
				PromptTemplate:    "Audit.",
			},
		)
		override := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		override.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		override.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)

	selectedTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	selectedPlan := startTask(t, ctx, store, selectedTask.ID).Mutation.Created[0]
	selectedReview, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       selectedPlan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "selected path"},
	})
	if err != nil {
		t.Fatalf("complete selected plan: %v", err)
	}
	selectedAudit, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       selectedReview.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"role": "reviewer"},
	})
	if err != nil {
		t.Fatalf("complete selected review: %v", err)
	}

	fallbackTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	fallbackPlan := startTask(t, ctx, store, fallbackTask.ID).Mutation.Created[0]
	fallbackAlternate, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       fallbackPlan.Reference,
		TransitionID: "alternate",
	})
	if err != nil {
		t.Fatalf("complete fallback plan: %v", err)
	}
	fallbackAudit, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       fallbackAlternate.Mutation.Created[0].Reference,
		TransitionID: "alternate_audit",
	})
	if err != nil {
		t.Fatalf("complete fallback alternate: %v", err)
	}

	selectedSelection := selectedAudit.Mutation.Created[0].AgentExecutionSelection
	fallbackSelection := fallbackAudit.Mutation.Created[0].AgentExecutionSelection
	if selectedSelection == nil || selectedSelection.Assignee != "reviewer" ||
		selectedSelection.Origin != workflow.AssigneeOriginTransitionSelected {
		t.Fatalf("selected converging target = %+v, want reviewer transition selection", selectedSelection)
	}
	if fallbackSelection == nil || fallbackSelection.Assignee != "coder" ||
		fallbackSelection.Origin != workflow.AssigneeOriginConfiguredFallback {
		t.Fatalf("fallback converging target = %+v, want coder fallback", fallbackSelection)
	}
}

func TestAutomaticCompletionChangesThinkingOnRetainedTargetSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {
				Identity:         "coder",
				QuestionsEnabled: true,
				Thinking: workflow.ThinkingCapability{
					ReasoningCapable: true,
					Finite:           true,
					Levels:           []string{"low", "high"},
				},
			},
			"reviewer": {
				Identity:         "reviewer",
				QuestionsEnabled: true,
				Thinking: workflow.ThinkingCapability{
					ReasoningCapable: true,
					Finite:           true,
					Levels:           []string{"low", "high"},
				},
			},
		},
		selectable: []workflow.TargetAgentRole{
			{Identity: "coder", ExplicitAgentCallable: true, QuestionsEnabled: true, Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}}},
			{Identity: "reviewer", ExplicitAgentCallable: true, QuestionsEnabled: true, Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}}},
		},
	}
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	audit := nodeByKey(t, definition, "audit")
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{
			{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee},
			{Key: "effort", Description: "Choose an effort.", Purpose: workflow.ParameterPurposeTargetThinking},
		}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	targetReference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(audit), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference target: %v", err)
	}
	targetSessionID := associateTaskSessionForTest(t, ctx, store, binding, cfg, targetReference, time.UnixMilli(2))
	setPersistedSessionRoleForTest(t, cfg, binding, store.metadata, targetSessionID, "reviewer")
	review, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"effort": "high"},
	})
	if err != nil {
		t.Fatalf("complete review with retained thinking change: %v", err)
	}
	selection := completed.Mutation.Created[0].AgentExecutionSelection
	if selection == nil || selection.Assignee != "reviewer" ||
		selection.Thinking == nil || string(*selection.Thinking) != "high" ||
		selection.Origin != workflow.AssigneeOriginRetainedSession {
		t.Fatalf("retained target selection = %+v, want reviewer/high retained session", selection)
	}
}

func TestAutomaticCompletionHonorsSelectedRoleAtFreshAndCompactedBoundaries(t *testing.T) {
	for _, contextMode := range []workflow.ContextMode{
		workflow.ContextModeNewSession,
		workflow.ContextModeCompactAndContinueSession,
	} {
		t.Run(string(contextMode), func(t *testing.T) {
			ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
			workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
			definition, _, err := store.GetDefinition(ctx, workflowID)
			if err != nil {
				t.Fatalf("GetDefinition: %v", err)
			}
			saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
				edge := workflowGraphSaveEdgeRecord(t, req.Edges, edgeByKey(t, definition, "audit").ID)
				edge.ContextMode = contextMode
				edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}
				edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
				edge.Parameters = []workflow.Parameter{{
					Key:     "role",
					Purpose: workflow.ParameterPurposeTargetAssignee,
				}}
			})
			linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
			task := createDefaultTask(t, ctx, store, binding.ProjectID)
			plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
			review, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
				Source:       plan.Reference,
				TransitionID: "review",
				OutputValues: map[string]string{"summary": "plan complete"},
			})
			if err != nil {
				t.Fatalf("complete plan: %v", err)
			}
			var sourceSessionID *runtimeids.SessionID
			if contextMode == workflow.ContextModeCompactAndContinueSession {
				sessionID := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, review.Mutation.Created[0].Reference)
				sourceSessionID = &sessionID
			}
			completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
				Source:       review.Mutation.Created[0].Reference,
				TransitionID: "audit",
				OutputValues: map[string]string{"role": "reviewer"},
			})
			if err != nil {
				t.Fatalf("complete review: %v", err)
			}
			target := completed.Mutation.Created[0]
			if target.AgentExecutionSelection == nil ||
				target.AgentExecutionSelection.Assignee != "reviewer" ||
				target.AgentExecutionSelection.Origin != workflow.AssigneeOriginTransitionSelected {
				t.Fatalf("target selection = %+v, want selected reviewer", target.AgentExecutionSelection)
			}
			if sourceSessionID == nil && target.SessionID != nil {
				t.Fatalf("fresh target session = %q, want absent", target.SessionID)
			}
			if sourceSessionID != nil && (target.SessionID == nil || *target.SessionID != *sourceSessionID) {
				t.Fatalf("compacted target session = %v, want %q", target.SessionID, *sourceSessionID)
			}
		})
	}
}

func setPersistedSessionRoleForTest(t *testing.T, cfg config.App, binding metadata.Binding, metadataStore *metadata.Store, sessionID runtimeids.SessionID, role string) {
	t.Helper()
	sessionRoot := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	store, err := session.Open(filepath.Join(sessionRoot, sessionID.String()), metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("Open session: %v", err)
	}
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
}
