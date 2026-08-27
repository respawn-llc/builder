package app

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestSessionWorkspaceIdentityChangeReopensMovedSession(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{}, WithUISessionID(sessionID.String()))
	identity := func(sequence uint64, workspaceID, root string) {
		target := &clientui.SessionExecutionTarget{
			WorkspaceID:           workspaceID,
			WorkspaceName:         workspaceID,
			WorkspaceRoot:         root,
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
			CwdRelpath:            ".",
			EffectiveWorkdir:      root,
		}
		model.applyAdmittedTranscriptMessageState(
			clientui.NewTranscriptMessage(sequence, clientui.NewTranscriptEvent(clientui.TranscriptSessionIdentity{
				SessionID:             sessionID,
				ConversationFreshness: clientui.ConversationFreshnessEstablished,
				ExecutionTarget:       target,
			})),
			runtimeTupleMergeResult{},
		)
	}
	identity(1, "workspace-a", "/workspace-a")
	identity(2, "workspace-b", "/workspace-b")

	transition := model.Transition()
	if !transition.SessionRetargeted || transition.Action != UIActionOpenSession || transition.TargetSessionID != sessionID.String() {
		t.Fatalf("Session transition = %+v", transition)
	}
}
