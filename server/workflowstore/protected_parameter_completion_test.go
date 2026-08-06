package workflowstore

import (
	"errors"
	"testing"

	"core/server/workflow"
)

func TestCompletionContractsApplyProtectedParameterConsumptionPolicies(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.PromptTemplate = "Audit {{.Params.role}}."
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	_, err = completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
	})
	var validationErr CompletionValidationError
	if !errors.As(err, &validationErr) || !completionHasCode(err, CompletionCodeUnavailableTargetAgentRole) {
		t.Fatalf("missing protected role error = %v, want unavailable-role validation", err)
	}
	completed, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"role": "reviewer"},
	})
	if err != nil {
		t.Fatalf("complete audit with protected role: %v", err)
	}
	if len(completed.Mutation.Created) != 1 ||
		completed.Mutation.Created[0].AgentExecutionSelection == nil ||
		completed.Mutation.Created[0].AgentExecutionSelection.Assignee != "reviewer" ||
		completed.Mutation.Created[0].AgentExecutionSelection.Origin != workflow.AssigneeOriginTransitionSelected {
		t.Fatalf("materialized target = %+v, want selected reviewer", completed.Mutation.Created)
	}
	if completed.Mutation.Created[0].CurrentInputValues["role"] != "reviewer" {
		t.Fatalf("materialized target current inputs = %+v, want exposed selector role", completed.Mutation.Created[0].CurrentInputValues)
	}
}

func TestAutomaticCompletionMaterializesSoleRoleWithoutProtectedValue(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {Identity: "coder", QuestionsEnabled: true},
		},
		selectable: []workflow.TargetAgentRole{
			{
				Identity:              "reviewer",
				ExplicitAgentCallable: true,
				Thinking: workflow.ThinkingCapability{
					ReasoningCapable: true,
					Finite:           true,
					Levels:           []string{"high"},
				},
			},
		},
	}
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{
			{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee},
			{Key: "effort", Purpose: workflow.ParameterPurposeTargetThinking},
		}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	completed, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("complete audit: %v", err)
	}
	target := completed.Mutation.Created[0]
	if err := store.InterruptCurrentNode(
		ctx,
		target.Reference,
		workflow.CurrentNodeInterruptionReasonUserInterrupt,
		workflow.CurrentNodeInterruptionDetail{Code: string(workflow.CurrentNodeInterruptionReasonUserInterrupt)},
	); err != nil {
		t.Fatalf("interrupt target with hidden selectors: %v", err)
	}
	classifications, err := store.PreflightTaskResume(ctx, task.ID)
	if err != nil {
		t.Fatalf("preflight resume with hidden selectors: %v", err)
	}
	if len(classifications) != 1 || len(classifications[0].Diagnostics) != 0 {
		t.Fatalf("hidden selector resume classifications = %+v, want one resumable Current Node", classifications)
	}
	selection := target.AgentExecutionSelection
	if selection == nil ||
		selection.Assignee != "reviewer" ||
		selection.Origin != workflow.AssigneeOriginTransitionSelected ||
		selection.Thinking == nil ||
		string(*selection.Thinking) != "high" {
		t.Fatalf("materialized target selection = %+v, want sole explicit role and finite thinking", selection)
	}
}

func TestAutomaticCompletionMaterializesFiniteThinkingSelection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {
				Identity:           "coder",
				QuestionsEnabled:   true,
				ConfiguredThinking: "low",
				Thinking: workflow.ThinkingCapability{
					ReasoningCapable: true,
					Finite:           true,
					Levels:           []string{"low", "high"},
				},
			},
		},
		selectable: []workflow.TargetAgentRole{{Identity: "reviewer", ExplicitAgentCallable: true}},
	}
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:         "effort",
			Description: "Choose an effort level.",
			Purpose:     workflow.ParameterPurposeTargetThinking,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	completed, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"effort": "high"},
	})
	if err != nil {
		t.Fatalf("complete audit: %v", err)
	}
	selection := completed.Mutation.Created[0].AgentExecutionSelection
	if selection == nil || selection.Thinking == nil || string(*selection.Thinking) != "high" {
		t.Fatalf("materialized target selection = %+v, want high thinking", selection)
	}
	if completed.Mutation.Created[0].CurrentInputValues["effort"] != "high" {
		t.Fatalf("materialized target current inputs = %+v, want exposed thinking selector", completed.Mutation.Created[0].CurrentInputValues)
	}
}

func TestAutomaticCompletionMaterializesOpenThinkingSelection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {
				Identity:         "coder",
				QuestionsEnabled: true,
				Thinking: workflow.ThinkingCapability{
					ReasoningCapable: true,
					Finite:           false,
				},
			},
		},
	}
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:         "effort",
			Description: "Use a provider-specific effort.",
			Purpose:     workflow.ParameterPurposeTargetThinking,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	completed, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"effort": "provider-custom"},
	})
	if err != nil {
		t.Fatalf("complete audit: %v", err)
	}
	selection := completed.Mutation.Created[0].AgentExecutionSelection
	if selection == nil || selection.Thinking == nil || string(*selection.Thinking) != "provider-custom" {
		t.Fatalf("materialized target selection = %+v, want provider-custom thinking", selection)
	}
}

func TestAutomaticCompletionRejectsInvalidSelectionBeforeMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.RequiresApproval = true
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	_, err = completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"role": "not-configured"},
	})
	if err == nil {
		t.Fatal("invalid role completion succeeded")
	}
	if !completionHasCode(err, string(workflow.TargetAgentSelectionErrorUnavailableRole)) {
		t.Fatalf("invalid role error = %v, want unavailable-role issue", err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after rejected completion: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(review.Mutation.Created[0].Reference) {
		t.Fatalf("current nodes after rejected completion = %+v, want unchanged source", currentNodes)
	}
	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after rejected completion: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending approvals after rejected completion = %+v, want none", approvals)
	}
}

func TestAutomaticCompletionMaterializesSelectionFromScriptSource(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		review := workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(nodeByKey(t, def, "review")))
		review.Kind = workflow.NodeKindScript
		review.SubagentRole = ""
		review.ScriptPath = "scripts/review"
		workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-review-"+workflowID.String())).PromptTemplate = ""
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	script, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "script input"},
	})
	if err != nil {
		t.Fatalf("complete Agent source: %v", err)
	}
	completed, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       script.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"role": "reviewer"},
	})
	if err != nil {
		t.Fatalf("complete Script source: %v", err)
	}
	selection := completed.Mutation.Created[0].AgentExecutionSelection
	if selection == nil || selection.Assignee != "reviewer" || selection.Origin != workflow.AssigneeOriginTransitionSelected {
		t.Fatalf("Script completion target selection = %+v, want selected reviewer", selection)
	}
}

type completionTargetCatalog struct {
	roles      map[string]workflow.TargetAgentRole
	selectable []workflow.TargetAgentRole
}

func (c completionTargetCatalog) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	value, ok := c.roles[role]
	return value, ok
}

func (c completionTargetCatalog) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return append([]workflow.TargetAgentRole(nil), c.selectable...)
}

func TestCompletionContractsRejectDormantProtectedValuesAsUnknownOutputs(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	_, err = completeCurrentNodeForStoreTest(store, ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"role": "reviewer"},
	})
	if !completionHasCode(err, CompletionCodeUnknownOutputField) {
		t.Fatalf("dormant protected output error = %v, want unknown output", err)
	}
}
