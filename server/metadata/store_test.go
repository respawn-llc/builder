package metadata

import (
	"context"
	"core/server/session"
	"core/shared/serverapi"
	"testing"
	"time"
)

func appendMetadataMessage(t *testing.T, store *session.Store, stepID string, role session.MessageRole, content string) session.EventRecord {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	step := stepID
	text := content
	record, receipt, err := eventLog.AppendRecord(&step, session.MessageRecord{
		Role:    role,
		Content: &text,
	})
	if err != nil || !receipt.Committed {
		t.Fatalf("append typed message: receipt=%+v error=%v", receipt, err)
	}
	return record
}

func metadataStringPointer(value string) *string {
	return &value
}

func stringPointerForStoreTest(value string) *string {
	return &value
}

func assertWorkspaceUnlinkBlocker(t *testing.T, blockers []serverapi.ProjectWorkspaceUnlinkBlocker, code string) {
	t.Helper()
	for _, blocker := range blockers {
		if blocker.Code == code {
			return
		}
	}
	t.Fatalf("blockers = %+v, want code %q", blockers, code)
}

func planAndCommitSessionWorkspaceRetarget(t *testing.T, ctx context.Context, store *Store, sessionID string, workspaceRoot string) Binding {
	t.Helper()
	plan, err := store.PlanSessionWorkspaceRetarget(ctx, SessionWorkspaceRetargetRequest{
		SessionID:     sessionID,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	result, err := store.CommitSessionWorkspaceRetarget(ctx, plan, time.Now().UTC())
	if err != nil {
		t.Fatalf("CommitSessionWorkspaceRetarget: %v", err)
	}
	return result.Binding
}
