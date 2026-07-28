package workflowview

import (
	"errors"
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

	activity, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:   string(started.task.ID),
		PageSize: 20,
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

	first, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:   string(started.task.ID),
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("Activity.List first: %v", err)
	}
	if len(first.Items) != 2 ||
		first.Items[0].Comment == nil ||
		first.Items[0].Comment.ID != newer.ID ||
		first.Items[1].Comment == nil ||
		first.Items[1].Comment.ID != older.ID ||
		first.NextPageToken == "" {
		t.Fatalf("first activity page = %+v, token %q", first.Items, first.NextPageToken)
	}
	second, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:    string(started.task.ID),
		PageSize:  2,
		PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatalf("Activity.List second: %v", err)
	}
	if len(second.Items) != 1 ||
		second.Items[0].SessionStarted == nil ||
		second.Items[0].SessionStarted.SessionID != sessionID.String() ||
		second.NextPageToken != "" {
		t.Fatalf("second activity page = %+v, token %q", second.Items, second.NextPageToken)
	}
	if _, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:    string(started.task.ID),
		PageSize:  2,
		PageToken: "invalid",
	}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("invalid activity page token error = %v, want invalid page token", err)
	}
}
