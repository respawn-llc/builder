package workflowrunner

import (
	"context"
	"path/filepath"
	"testing"

	"core/internal/testharness/workflowfixture"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestCurrentNodeStartUsesMaterializedSelectedRoleAndForcesQuestions(t *testing.T) {
	f, input := newMaterializedRoleSelectionStart(t)
	if input.ExecutionRoot == nil {
		t.Fatal("current node start context omitted execution root")
	}
	plan, disposable, err := f.starter.planCurrentNodeSession(
		context.Background(),
		input,
		*input.ExecutionRoot,
		false,
	)
	if err != nil {
		t.Fatalf("planCurrentNodeSession: %v", err)
	}
	if disposable {
		t.Cleanup(func() {
			if err := f.starter.cleanupSession(context.Background(), plan.Descriptor); err != nil {
				t.Errorf("cleanup planned session: %v", err)
			}
		})
	}
	if plan.ActiveSettings.Model != "workflow-reviewer" {
		t.Fatalf("planned model = %q, want materialized reviewer model", plan.ActiveSettings.Model)
	}
	if !containsTool(plan.EnabledTools, toolspec.ToolAskQuestion) {
		t.Fatalf("planned tools = %+v, want forced ask_question", plan.EnabledTools)
	}
}

func TestCurrentNodeStartUsesMaterializedSelectedRoleAtCompactionBoundary(t *testing.T) {
	f, input := newMaterializedRoleSelectionStart(t)
	input.ContextMode = workflow.ContextModeCompactAndContinueSession
	input.EnteringEdge.ContextMode = workflow.ContextModeCompactAndContinueSession
	store, err := session.Create(
		filepath.Join(f.cfg.PersistenceRoot, "projects", input.Task.ProjectID, "sessions"),
		"sessions",
		f.workspace,
		sessioncontract.SessionCategoryMain,
		f.starter.storeOptions...,
	)
	if err != nil {
		t.Fatalf("create retained Session: %v", err)
	}
	if err := store.SetContinuationContext(session.ContinuationContext{
		AgentRole: textutil.Value("reviewer"),
	}); err != nil {
		t.Fatalf("set retained Session Agent: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist retained Session: %v", err)
	}
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		AutoCompaction: textutil.Value(false),
	}); err != nil {
		t.Fatalf("disable retained Session Auto-compaction: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse retained Session ID: %v", err)
	}
	input.CurrentNode.SessionID = &sessionID
	input.SourceSessionID = &sessionID
	if input.ExecutionRoot == nil {
		t.Fatal("current node start context omitted execution root")
	}
	plan, disposable, err := f.starter.planCurrentNodeSession(
		context.Background(),
		input,
		*input.ExecutionRoot,
		false,
	)
	if err != nil {
		t.Fatalf("planCurrentNodeSession compact: %v", err)
	}
	if disposable {
		t.Cleanup(func() {
			if err := f.starter.cleanupSession(context.Background(), plan.Descriptor); err != nil {
				t.Errorf("cleanup compact planned session: %v", err)
			}
		})
	}
	if plan.ActiveSettings.Model != "workflow-reviewer" {
		t.Fatalf("compact planned model = %q, want materialized reviewer model", plan.ActiveSettings.Model)
	}
	if !containsTool(plan.EnabledTools, toolspec.ToolAskQuestion) {
		t.Fatalf("compact planned tools = %+v, want forced ask_question", plan.EnabledTools)
	}
	if !plan.AutoCompactionEnabled {
		t.Fatal("compact planned Auto-compaction = false, want required true")
	}
	reopened, err := session.OpenByID(
		filepath.Join(f.cfg.PersistenceRoot, "projects", input.Task.ProjectID, "sessions"),
		plan.Descriptor.SessionID().String(),
		f.starter.storeOptions...,
	)
	if err != nil {
		t.Fatalf("reopen admitted Workflow Session: %v", err)
	}
	if got := reopened.Meta().ChatSettings; got == nil || got.AutoCompaction == nil || !*got.AutoCompaction {
		t.Fatalf("persisted Workflow Auto-compaction = %+v, want true", got)
	}
}

func TestCurrentNodeStartAppliesMaterializedWorkflowThinkingAfterRoleResolution(t *testing.T) {
	f, input := newMaterializedRoleSelectionStart(t)
	thinking, err := workflow.NewThinkingValue("max")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	input.CurrentNode.AgentExecutionSelection.Thinking = &thinking
	if input.ExecutionRoot == nil {
		t.Fatal("current node start context omitted execution root")
	}
	plan, disposable, err := f.starter.planCurrentNodeSession(
		context.Background(),
		input,
		*input.ExecutionRoot,
		false,
	)
	if err != nil {
		t.Fatalf("planCurrentNodeSession: %v", err)
	}
	if disposable {
		t.Cleanup(func() {
			if err := f.starter.cleanupSession(context.Background(), plan.Descriptor); err != nil {
				t.Errorf("cleanup planned session: %v", err)
			}
		})
	}
	if plan.ActiveSettings.ThinkingLevel != "max" {
		t.Fatalf("planned thinking level = %q, want max", plan.ActiveSettings.ThinkingLevel)
	}
}

func TestCurrentNodeStartUsesConfiguredFallbackThinking(t *testing.T) {
	f, input := newMaterializedRoleSelectionStart(t)
	thinking, err := workflow.NewThinkingValue("high")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	fallback, err := workflow.NewAgentExecutionSelection(
		"coder",
		&thinking,
		workflow.AssigneeOriginConfiguredFallback,
	)
	if err != nil {
		t.Fatalf("NewAgentExecutionSelection: %v", err)
	}
	input.CurrentNode.AgentExecutionSelection = &fallback
	if input.ExecutionRoot == nil {
		t.Fatal("current node start context omitted execution root")
	}
	plan, disposable, err := f.starter.planCurrentNodeSession(
		context.Background(),
		input,
		*input.ExecutionRoot,
		false,
	)
	if err != nil {
		t.Fatalf("planCurrentNodeSession: %v", err)
	}
	if disposable {
		t.Cleanup(func() {
			if err := f.starter.cleanupSession(context.Background(), plan.Descriptor); err != nil {
				t.Errorf("cleanup planned session: %v", err)
			}
		})
	}
	if plan.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("planned thinking level = %q, want configured fallback high", plan.ActiveSettings.ThinkingLevel)
	}
}

func TestCurrentNodeStartFailsWhenMaterializedRoleIsRemovedFromConfig(t *testing.T) {
	f, input := newMaterializedRoleSelectionStart(t)
	delete(f.starter.cfg.Settings.Subagents, "reviewer")
	if input.ExecutionRoot == nil {
		t.Fatal("current node start context omitted execution root")
	}
	if _, _, err := f.starter.planCurrentNodeSession(context.Background(), input, *input.ExecutionRoot, false); err == nil {
		t.Fatal("planCurrentNodeSession succeeded after removing materialized role")
	}
}

func newMaterializedRoleSelectionStart(t *testing.T) (*currentNodeRunnerFixture, workflowstore.CurrentNodeStartContext) {
	t.Helper()
	f := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeRoleSelectionWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	if err := f.store.LockTaskExecutionTarget(context.Background(), task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   f.workspaceID,
			SourceWorkspaceRoot: f.workspace,
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	started, err := f.store.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	first := started.Mutation.Created[0]
	completed, err := f.store.CompleteCurrentNode(context.Background(), workflowstore.CurrentNodeCompletionRequest{
		Source:       first.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"role": "reviewer"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	target := completed.Mutation.Created[0]
	if target.AgentExecutionSelection == nil || target.AgentExecutionSelection.Assignee != "reviewer" {
		t.Fatalf("materialized selection = %+v, want reviewer", target.AgentExecutionSelection)
	}
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), target.Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext: %v", err)
	}
	return f, input
}

func containsTool(tools []toolspec.ID, want toolspec.ID) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

func createCurrentNodeRoleSelectionWorkflow(t *testing.T, store *workflowstore.Store) runtimeids.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Current Node materialized role"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	firstID := workflow.NodeID("node-first-" + created.ID.String())
	secondID := workflow.NodeID("node-second-" + created.ID.String())
	startGroup := workflow.TransitionGroupID("group-start-" + created.ID.String())
	nextGroup := workflow.TransitionGroupID("group-next-" + created.ID.String())
	doneGroup := workflow.TransitionGroupID("group-done-" + created.ID.String())
	workflowfixture.SaveStoreGraph(t, ctx, store, created.ID, func(definition workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		start := nodeByKindRunnerTest(t, definition, workflow.NodeKindStart)
		done := nodeByKindRunnerTest(t, definition, workflow.NodeKindTerminal)
		request.Nodes = append(request.Nodes,
			workflowstore.NodeRecord{ID: firstID, WorkflowID: created.ID, Key: "first", Kind: workflow.NodeKindAgent, DisplayName: "First", SubagentRole: "coder"},
			workflowstore.NodeRecord{ID: secondID, WorkflowID: created.ID, Key: "second", Kind: workflow.NodeKindAgent, DisplayName: "Second", SubagentRole: "coder"},
		)
		request.TransitionGroups = append(request.TransitionGroups,
			workflowstore.TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			workflowstore.TransitionGroupRecord{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: firstID, TransitionID: "next", DisplayName: "Next"},
			workflowstore.TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: secondID, TransitionID: "done", DisplayName: "Done"},
		)
		request.Edges = append(request.Edges,
			workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: firstID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "First."},
			workflowstore.EdgeRecord{
				ID: workflow.EdgeID("edge-next-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: secondID,
				AssigneeSelection: workflow.AssigneeSelectionPreviousNode, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession,
				PromptTemplate: "Second.", Parameters: []workflow.Parameter{{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee}},
			},
			workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}

func nodeByKindRunnerTest(t *testing.T, definition workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("node kind %q not found", kind)
	return nil
}
