package app

import (
	"testing"

	"core/shared/clientui"
)

func TestSuccessfulSessionRetargetOutcomeRequestsSameSessionHandoff(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{}, WithUISessionID("11111111-1111-4111-8111-111111111111"))
	success := clientui.TranscriptSessionRetargetSuccess{
		ProjectID:     "project-b",
		ProjectName:   "Project B",
		WorkspaceID:   "workspace-b",
		CanonicalRoot: "/workspace-b",
	}
	command := model.applyAdmittedTranscriptMessageState(
		clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptSessionRetargetOutcome{
			OperationID: clientui.NewWorktreeTransitionID(),
			Kind:        clientui.TranscriptSessionRetargetSucceeded,
			Success:     &success,
		})),
		runtimeTupleMergeResult{},
	)
	transition := model.Transition()
	if command == nil || transition.Action != UIActionOpenSession ||
		transition.TargetSessionID != model.sessionID ||
		transition.SessionRetargetSuccess == nil ||
		transition.SessionRetargetSuccess.ProjectID != success.ProjectID {
		t.Fatalf("Session retarget transition = %+v", transition)
	}
}

func TestFailedSessionRetargetOutcomeKeepsOpenUI(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{}, WithUISessionID("11111111-1111-4111-8111-111111111111"))
	command := model.applyAdmittedTranscriptMessageState(
		clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptSessionRetargetOutcome{
			OperationID: clientui.NewWorktreeTransitionID(),
			Kind:        clientui.TranscriptSessionRetargetFailed,
			Failure: &clientui.TranscriptSessionRetargetFailure{
				Diagnostic:                "commit failed",
				UnchangedProjectID:        "project-a",
				UnchangedProjectName:      "Project A",
				UnchangedWorkingDirectory: "/workspace-a",
			},
		})),
		runtimeTupleMergeResult{},
	)
	if command != nil || model.Transition().Action != UIActionNone {
		t.Fatalf("failed Session retarget changed UI lifecycle: command=%v transition=%+v", command, model.Transition())
	}
}
