package runtime

import (
	"errors"
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
	kind, ok := selectWorkflowTaskPromptForTest(
		t,
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
	kind, ok := selectWorkflowTaskPromptForTest(
		t,
		workflowPromptItems("run-previous"),
		"run-current",
		workflowTaskPromptTriggerTaskDelivery,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func TestSelectWorkflowTaskPromptForSameNodeReentryForcesReassignment(t *testing.T) {
	t.Parallel()
	kind, ok := selectWorkflowTaskPromptForTest(
		t,
		workflowPromptItems("assignment-current"),
		"assignment-current",
		workflowTaskPromptTriggerAssignmentDelivery,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func TestSelectWorkflowTaskPromptForResumedAssignmentOmitsDuplicate(t *testing.T) {
	t.Parallel()
	kind, ok := selectWorkflowTaskPromptForTest(
		t,
		workflowPromptItems("assignment-current"),
		"assignment-current",
		workflowTaskPromptTriggerResumeDelivery,
	)
	if ok {
		t.Fatalf("selected workflow prompt = %d, want no duplicate prompt", kind)
	}
}

func TestWorkflowPromptIdentityUsesNaturalCurrentNodeAcrossExecutionScopes(t *testing.T) {
	t.Parallel()
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
	expectedIdentity := workflowruntime.CurrentNodePromptIdentity(currentNode)
	if prompt.Identity != expectedIdentity {
		t.Fatalf("workflow prompt identity = %q, want natural Current Node identity %q", prompt.Identity, expectedIdentity)
	}
	kind, inject := selectWorkflowTaskPromptForTest(
		t,
		workflowPromptItems(expectedIdentity),
		prompt.Identity,
		workflowTaskPromptTriggerAssignmentDelivery,
	)
	if !inject || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("same-node reentry prompt = %d, inject=%t, want reassignment", kind, inject)
	}
}

func TestSelectWorkflowTaskPromptOmitsDuplicateForCurrentTaskRequest(t *testing.T) {
	t.Parallel()
	kind, ok := selectWorkflowTaskPromptForTest(
		t,
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
	kind, ok := selectWorkflowTaskPromptForTest(
		t,
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
	kind, ok := selectWorkflowTaskPromptForTest(
		t,
		workflowPromptItems("run-previous"),
		"run-current",
		workflowTaskPromptTriggerCompaction,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func TestSelectWorkflowTaskPromptResumeRequiresMatchingAssignment(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		items   []llm.ResponseItem
		wantErr bool
	}{
		{items: nil, wantErr: true},
		{items: workflowPromptItems("run-previous"), wantErr: true},
		{items: workflowPromptItems("run-current")},
	} {
		kind, inject, err := selectWorkflowTaskPrompt(
			test.items,
			"run-current",
			workflowTaskPromptTriggerResumeDelivery,
		)
		if inject {
			t.Fatalf(
				"resume selected workflow assignment kind %d for items %+v; want no assignment",
				kind,
				test.items,
			)
		}
		gotErr := errors.Is(err, errWorkflowResumeAssignmentUnavailable)
		if gotErr != test.wantErr {
			t.Fatalf(
				"resume selection error = %v for items %+v, want error=%t",
				err,
				test.items,
				test.wantErr,
			)
		}
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

	prompt, configured := engine.workflowPrompt()
	if !configured {
		t.Fatal("workflow prompt is not configured")
	}
	kind, ok := selectWorkflowTaskPromptForTest(
		t,
		engine.transcriptRuntimeState().SnapshotItems(),
		prompt.Identity,
		workflowTaskPromptTriggerAssignmentDelivery,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func selectWorkflowTaskPromptForTest(
	t *testing.T,
	items []llm.ResponseItem,
	currentNodeIdentity string,
	trigger workflowTaskPromptTrigger,
) (prompts.WorkflowTaskPromptKind, bool) {
	t.Helper()
	kind, inject, err := selectWorkflowTaskPrompt(items, currentNodeIdentity, trigger)
	if err != nil {
		t.Fatalf("selectWorkflowTaskPrompt: %v", err)
	}
	return kind, inject
}

func workflowPromptItems(identity string) []llm.ResponseItem {
	return llm.ItemsFromMessages([]llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
		SourcePath:  textutil.Value(identity),
		Content:     textutil.Value("workflow instructions"),
	}})
}
