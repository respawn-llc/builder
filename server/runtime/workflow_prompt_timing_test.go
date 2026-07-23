package runtime

import (
	"testing"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func TestSelectWorkflowTaskPromptForFirstNodeAssignmentInSession(t *testing.T) {
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
	kind, ok := selectWorkflowTaskPrompt(
		workflowPromptItems("run-previous"),
		"run-current",
		workflowTaskPromptTriggerTaskDelivery,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func TestSelectWorkflowTaskPromptOmitsDuplicateForCurrentTaskRequest(t *testing.T) {
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
	runID := workflow.RunID("run-current")
	engine := mustNewWorkflowTestEngine(
		t,
		clone,
		&fakeClient{},
		&workflowruntime.Config{
			RunID:          runID,
			Contract:       workflowruntime.CompletionContract{RunID: runID},
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
		},
		Config{},
	)

	kind, ok := selectWorkflowTaskPrompt(
		engine.transcriptRuntimeState().SnapshotItems(),
		string(runID),
		workflowTaskPromptTriggerTaskDelivery,
	)
	if !ok || kind != prompts.WorkflowTaskPromptReassignment {
		t.Fatalf("selected workflow prompt = %d, present=%t, want reassignment", kind, ok)
	}
}

func workflowPromptItems(runID string) []llm.ResponseItem {
	return llm.ItemsFromMessages([]llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
		SourcePath:  textutil.Value(runID),
		Content:     textutil.Value("workflow instructions"),
	}})
}
