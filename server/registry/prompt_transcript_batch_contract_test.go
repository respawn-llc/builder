package registry

import (
	"reflect"
	"testing"
	"time"

	askquestion "core/server/tools"
	"core/shared/clientui"
)

func TestPromptProjectionPreservesOrderedFileAccessAliases(t *testing.T) {
	request := askquestion.AskQuestionRequest{
		ToolCallID: "approval-1",
		StepID:     registryTestStepID,
		Question:   "Approve?",
		Approval:   true,
		ApprovalOptions: []askquestion.AskQuestionApprovalOption{
			{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
		AccessTargets: []askquestion.FileAccessTarget{
			{RequestedPath: "alias/first", ResolvedPath: "/outside/target"},
			{RequestedPath: "alias/second", ResolvedPath: "/outside/target"},
		},
	}
	want := []clientui.FileAccessTarget{
		{RequestedPath: "alias/first", ResolvedPath: "/outside/target"},
		{RequestedPath: "alias/second", ResolvedPath: "/outside/target"},
	}
	for _, eventType := range []pendingPromptEventType{pendingPromptEventPending, pendingPromptEventResolved} {
		prompt := transcriptPendingPromptFromSnapshot("session-1", PendingPromptSnapshot{
			Request:   request,
			CreatedAt: time.Unix(100, 0).UTC(),
		}, eventType)
		if !reflect.DeepEqual(prompt.AccessTargets, want) {
			t.Fatalf("%v prompt access targets = %+v, want ordered aliases %+v", eventType, prompt.AccessTargets, want)
		}
	}
}
