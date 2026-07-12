package worktree

import (
	"testing"

	"core/shared/serverapi"
)

func TestWorktreeStatusInspectsOnlyTheRecordedTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	before, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget before: %v", err)
	}
	status, err := env.service.GetWorktreeStatus(env.ctx, serverapi.WorktreeStatusRequest{SessionID: env.session.Meta().SessionID})
	if err != nil {
		t.Fatalf("GetWorktreeStatus: %v", err)
	}
	if len(status.Problems) != 0 {
		t.Fatalf("status problems = %+v", status.Problems)
	}
	if status.Worktree.RecordedRoot != env.workspaceRoot || status.Worktree.ObservedRoot == nil || *status.Worktree.ObservedRoot != env.workspaceRoot {
		t.Fatalf("status worktree = %+v", status.Worktree)
	}
	after, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget after: %v", err)
	}
	if after != before {
		t.Fatalf("status mutated target: before=%+v after=%+v", before, after)
	}
}
