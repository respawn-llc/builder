package workflowview

import (
	"testing"

	"core/shared/serverapi"
)

func TestActivityProjectsOnlyCommentsAndRetainedSessionCreation(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Activity task")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	comment, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "Current Node comment", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	activity, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  intPointer(20),
	})
	if err != nil {
		t.Fatalf("Activity.List: %v", err)
	}
	if len(activity.Items) != 2 {
		t.Fatalf("activity items = %+v, want comment and retained session creation", activity.Items)
	}
	items := map[string]serverapi.WorkflowTaskActivityItem{}
	for _, item := range activity.Items {
		items[item.Type] = item
	}
	if commentItem, ok := items["comment"]; !ok ||
		commentItem.Comment == nil ||
		commentItem.Comment.ID != comment.ID ||
		commentItem.SessionStarted != nil {
		t.Fatalf("comment activity = %+v, want comment only", commentItem)
	}
	if sessionItem, ok := items["session_started"]; !ok ||
		sessionItem.SessionStarted == nil ||
		sessionItem.SessionStarted.SessionID != sessionID.String() ||
		sessionItem.Comment != nil {
		t.Fatalf("session activity = %+v, want retained session creation only", sessionItem)
	}
}

func TestActivityOrdersAndPaginatesCanonicalSources(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Activity pagination")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	older, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "Older", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment older: %v", err)
	}
	newer, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "Newer", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment newer: %v", err)
	}
	fixture.setSessionCreatedAt(t, sessionID, 1_000)
	fixture.setCommentUpdatedAt(t, older.ID, 2_000)
	fixture.setCommentUpdatedAt(t, newer.ID, 3_000)

	limit := 2
	first, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("Activity.List first: %v", err)
	}
	if len(first.Items) != 2 ||
		first.Items[0].Comment == nil ||
		first.Items[0].Comment.ID != newer.ID ||
		first.Items[1].Comment == nil ||
		first.Items[1].Comment.ID != older.ID ||
		first.NextOffset == nil || *first.NextOffset != 2 {
		t.Fatalf("first activity page = %+v, next offset %v", first.Items, first.NextOffset)
	}
	second, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Offset: first.NextOffset,
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("Activity.List second: %v", err)
	}
	if len(second.Items) != 1 ||
		second.Items[0].SessionStarted == nil ||
		second.Items[0].SessionStarted.SessionID != sessionID.String() ||
		second.NextOffset != nil {
		t.Fatalf("second activity page = %+v, next offset %v", second.Items, second.NextOffset)
	}
	beyondEnd := 100
	empty, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Offset: &beyondEnd,
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("Activity.List beyond end: %v", err)
	}
	if len(empty.Items) != 0 || empty.NextOffset != nil {
		t.Fatalf("beyond-end activity page = %+v", empty)
	}
}

func TestActivityUsesActivityIDAsEqualTimeTieBreakerAndReflectsCommentEdits(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Activity ordering")
	first, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "First", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment first: %v", err)
	}
	second, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "Second", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment second: %v", err)
	}
	fixture.setCommentUpdatedAt(t, first.ID, 5_000)
	fixture.setCommentUpdatedAt(t, second.ID, 5_000)

	limit := 10
	page, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("Activity.List equal-time: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ActivityID <= page.Items[1].ActivityID {
		t.Fatalf("equal-time activity page = %+v", page.Items)
	}

	if err := fixture.store.ReplaceComment(fixture.ctx, first.ID, "Edited"); err != nil {
		t.Fatalf("ReplaceComment: %v", err)
	}
	editedAt := int64(6_000)
	fixture.setCommentUpdatedAt(t, first.ID, editedAt)
	page, err = fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("Activity.List after edit: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].Comment == nil || page.Items[0].Comment.ID != first.ID {
		t.Fatalf("edited activity page = %+v", page.Items)
	}
}

func TestActivityPaginatesLargeTaskHistoryByOffset(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Large Activity history")
	for index := 0; index < 123; index++ {
		if _, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "Comment", "user", "user-1"); err != nil {
			t.Fatalf("AddComment %d: %v", index, err)
		}
	}

	limit := 50
	var offset *int
	total := 0
	for pageNumber := 0; pageNumber < 4; pageNumber++ {
		page, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
			TaskID: string(started.task.ID),
			Offset: offset,
			Limit:  &limit,
		})
		if err != nil {
			t.Fatalf("Activity.List page %d: %v", pageNumber, err)
		}
		if len(page.Items) > limit {
			t.Fatalf("activity page %d has %d items, want at most %d", pageNumber, len(page.Items), limit)
		}
		total += len(page.Items)
		if pageNumber < 2 {
			if page.NextOffset == nil {
				t.Fatalf("activity page %d ended before history", pageNumber)
			}
			offset = page.NextOffset
			continue
		}
		if page.NextOffset != nil {
			t.Fatalf("activity page %d continued after history: %v", pageNumber, page.NextOffset)
		}
		break
	}
	if total != 123 {
		t.Fatalf("activity history returned %d items, want 123", total)
	}
}
