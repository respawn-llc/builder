package runtime

import (
	"testing"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func TestSelectWorkflowTaskPromptForFirstNodeAssignmentInSession(t *testing.T) {
	t.Parallel()
	kind, ok := selectWorkflowTaskPrompt(
		nil,
		"run-current",
		workflowTaskPromptTriggerTaskDelivery,
	)
	if !ok || kind != prompts.WorkflowTaskPromptInitialAssignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want initial assignment", kind, ok)
	}
}

func TestSelectWorkflowTaskPromptForAnotherNodeAssignmentInSameSession(t *testing.T) {
	t.Parallel()
	kind, ok := selectWorkflowTaskPrompt(
		workflowPromptItems("run-previous"),
		"run-current",
		workflowTaskPromptTriggerTaskDelivery,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func TestWorkflowPromptIdentityChangesWhenTheSameNodeIsReentered(t *testing.T) {
	t.Parallel()
	previousScopeID := runtimeids.NewExecutionScopeID()
	currentScopeID := runtimeids.NewExecutionScopeID()
	currentNode, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        currentScopeID,
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
			Instructions:   workflowruntime.TaskInstructions{CurrentNode: currentNode},
		},
		Config{},
	)
	prompt, configured := engine.workflowPrompt()
	if !configured {
		t.Fatal("workflow prompt is not configured")
	}
	if prompt.Identity != currentScopeID.String() {
		t.Fatalf("workflow prompt identity = %q, want exact scope %q", prompt.Identity, currentScopeID)
	}
	kind, inject := selectWorkflowTaskPrompt(
		workflowPromptItems(previousScopeID.String()),
		prompt.Identity,
		workflowTaskPromptTriggerTaskDelivery,
	)
	if !inject || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("same-node reentry prompt = %d, inject=%t, want reassignment", kind, inject)
	}
}

func TestSelectWorkflowTaskPromptOmitsDuplicateForCurrentTaskRequest(t *testing.T) {
	t.Parallel()
	kind, ok := selectWorkflowTaskPrompt(
		workflowPromptItems("run-current"),
		"run-current",
		workflowTaskPromptTriggerTaskDelivery,
	)
	if ok {
		t.Fatalf("selected workflow prompt = %d, want no duplicate prompt", kind)
	}
}

func TestSelectWorkflowTaskPromptAfterSameNodeAssignmentCompaction(t *testing.T) {
	t.Parallel()
	kind, ok := selectWorkflowTaskPrompt(
		workflowPromptItems("run-current"),
		"run-current",
		workflowTaskPromptTriggerCompaction,
	)
	if !ok || kind != prompts.WorkflowTaskPromptCompactionReminder {
		t.Fatalf("selected workflow prompt = %d, present=%t, want compaction reminder", kind, ok)
	}
}

func TestSelectWorkflowTaskPromptAfterCompactionForAnotherNodeAssignment(t *testing.T) {
	t.Parallel()
	kind, ok := selectWorkflowTaskPrompt(
		workflowPromptItems("run-previous"),
		"run-current",
		workflowTaskPromptTriggerCompaction,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func TestSelectWorkflowTaskPromptForFanoutCloneWithInheritedAssignment(t *testing.T) {
	t.Parallel()
	source := mustCreateTestSession(t)
	mustAppendTestEvent(t, source, "previous-assignment", llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
		SourcePath:  textutil.Value("run-previous"),
		Content:     textutil.Value("workflow instructions"),
	})
	sourceLog, err := source.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize source event log: %v", err)
	}
	clone, err := session.CloneSession(
		sourceLog,
		"fanout-clone",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("clone source Session: %v", err)
	}
	if clone.Meta().SessionID == source.Meta().SessionID {
		t.Fatalf("fan-out clone reused source Session ID %q", source.Meta().SessionID)
	}
	scopeID := runtimeids.NewExecutionScopeID()
	engine := mustNewWorkflowTestEngine(
		t,
		clone,
		&fakeClient{},
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        scopeID,
			Contract:       workflowruntime.CompletionContract{},
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
		},
		Config{},
	)

	kind, ok := selectWorkflowTaskPrompt(
		engine.transcriptRuntimeState().SnapshotItems(),
		scopeID.String(),
		workflowTaskPromptTriggerTaskDelivery,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func workflowPromptItems(scopeID string) []llm.ResponseItem {
	return llm.ItemsFromMessages([]llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
		SourcePath:  textutil.Value(scopeID),
		Content:     textutil.Value("workflow instructions"),
	}})
}
