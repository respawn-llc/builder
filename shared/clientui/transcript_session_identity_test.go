package clientui

import (
	"testing"

	"core/shared/runtimeids"
)

func TestTranscriptSessionIdentityAcceptsConfiguredAndUnconfiguredExecutionTargets(t *testing.T) {
	sessionName := "Session"
	tests := []TranscriptSessionIdentity{
		{
			SessionID:             transcriptTestSessionID(t),
			ConversationFreshness: ConversationFreshnessFresh,
		},
		{
			SessionID:             transcriptTestSessionID(t),
			SessionName:           &sessionName,
			ConversationFreshness: ConversationFreshnessEstablished,
			ExecutionTarget: &SessionExecutionTarget{
				WorkspaceID:           "workspace-1",
				WorkspaceName:         "Workspace",
				WorkspaceRoot:         "/repo",
				WorkspaceAvailability: ProjectAvailabilityAvailable,
				CwdRelpath:            ".",
				EffectiveWorkdir:      "/repo",
			},
		},
	}
	for _, identity := range tests {
		if err := identity.Validate(); err != nil {
			t.Fatalf("validate session identity %+v: %v", identity, err)
		}
	}
}

func TestTranscriptSessionIdentityRejectsPartialOrInconsistentExecutionTargets(t *testing.T) {
	base := TranscriptSessionIdentity{
		SessionID:             transcriptTestSessionID(t),
		ConversationFreshness: ConversationFreshnessEstablished,
		ExecutionTarget: &SessionExecutionTarget{
			WorkspaceID:           "workspace-1",
			WorkspaceRoot:         "/repo",
			WorkspaceAvailability: ProjectAvailabilityAvailable,
			CwdRelpath:            ".",
			EffectiveWorkdir:      "/repo",
		},
	}
	tests := []TranscriptSessionIdentity{
		func() TranscriptSessionIdentity {
			identity := base
			identity.SessionID = runtimeids.SessionID{}
			return identity
		}(),
		func() TranscriptSessionIdentity {
			identity := base
			identity.ConversationFreshness = ConversationFreshness(2)
			return identity
		}(),
		func() TranscriptSessionIdentity {
			identity := base
			identity.ExecutionTarget = &SessionExecutionTarget{EffectiveWorkdir: "/repo"}
			return identity
		}(),
		func() TranscriptSessionIdentity {
			identity := base
			target := *base.ExecutionTarget
			target.CwdRelpath = "../other"
			identity.ExecutionTarget = &target
			return identity
		}(),
		func() TranscriptSessionIdentity {
			identity := base
			target := *base.ExecutionTarget
			target.EffectiveWorkdir = "/other"
			identity.ExecutionTarget = &target
			return identity
		}(),
		func() TranscriptSessionIdentity {
			identity := base
			target := *base.ExecutionTarget
			target.Worktree = &SessionExecutionWorktreeTarget{
				ID:           "worktree-1",
				Root:         "/repo/.kent-worktrees/task",
				Availability: string(ProjectAvailabilityUnlinked),
			}
			target.EffectiveWorkdir = "/repo/.kent-worktrees/task"
			identity.ExecutionTarget = &target
			return identity
		}(),
	}
	for _, identity := range tests {
		if err := identity.Validate(); err == nil {
			t.Fatalf("accepted invalid session identity: %+v", identity)
		}
	}
}
