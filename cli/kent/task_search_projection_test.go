package main

import (
	"testing"

	"core/shared/serverapi"
)

func TestTaskSearchPlainProjectionPreservesHitKindsAndRemainingCount(t *testing.T) {
	commentID := "comment-1"
	response := serverapi.TaskSearchResponse{
		Mode: serverapi.TaskSearchModeLiteral,
		Groups: []serverapi.TaskSearchGroup{{
			ProjectID:     "project-1",
			ProjectKey:    "KNT",
			TaskID:        "task-1",
			ShortID:       "KNT-1",
			WorkflowID:    "workflow-1",
			Title:         "Task",
			Status:        serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindBacklog, NativeState: serverapi.WorkflowTaskNativeStateActive},
			TotalHitCount: 4,
			Hits: []serverapi.TaskSearchHit{
				{
					Ordinal: 1,
					Source:  serverapi.TaskSearchSource{Kind: serverapi.TaskSearchSourceKindTitle},
					Literal: &serverapi.TaskSearchLiteralHit{Before: "left\t", Match: "needle", After: "\nright", LeftTruncated: true},
				},
				{
					Ordinal: 3,
					Source:  serverapi.TaskSearchSource{Kind: serverapi.TaskSearchSourceKindComment, CommentID: &commentID},
					Literal: &serverapi.TaskSearchLiteralHit{Match: "needle", RightTruncated: true},
				},
			},
		}},
	}

	projection, err := taskSearchPlainProjectionFromResponse(response)
	if err != nil {
		t.Fatalf("taskSearchPlainProjectionFromResponse: %v", err)
	}
	if len(projection.Groups) != 1 {
		t.Fatalf("group count = %d, want 1", len(projection.Groups))
	}
	group := projection.Groups[0]
	if group.RemainingHitCount != 1 {
		t.Fatalf("remaining hit count = %d, want 1", group.RemainingHitCount)
	}
	if len(group.Lines) != 3 {
		t.Fatalf("line count = %d, want 3", len(group.Lines))
	}
	if group.Lines[0].Kind != taskSearchPlainLineKindHit ||
		group.Lines[0].Literal == nil ||
		!group.Lines[0].Literal.LeftTruncated ||
		group.Lines[0].Literal.RightTruncated {
		t.Fatalf("first line = %#v, want literal title/body hit with left omission", group.Lines[0])
	}
	if group.Lines[1].Kind != taskSearchPlainLineKindCommentHeading {
		t.Fatalf("second line kind = %v, want comment heading", group.Lines[1].Kind)
	}
	if group.Lines[2].Kind != taskSearchPlainLineKindHit ||
		group.Lines[2].Literal == nil ||
		!group.Lines[2].Literal.RightTruncated {
		t.Fatalf("third line = %#v, want literal comment hit with right omission", group.Lines[2])
	}
}

func TestTaskSearchPlainRendererWritesOneRecordPerProjectedLine(t *testing.T) {
	projection := taskSearchPlainProjection{
		Groups: []taskSearchPlainGroup{{
			ShortID: "KNT-1",
			Title:   "Task",
			Lines: []taskSearchPlainLine{
				{Kind: taskSearchPlainLineKindHit, FTS5Snippet: "one"},
				{Kind: taskSearchPlainLineKindCommentHeading},
				{Kind: taskSearchPlainLineKindHit, FTS5Snippet: "two"},
			},
			RemainingHitCount: 1,
		}},
	}
	writer := &newlineCountingWriter{}

	if err := writeTaskSearchPlainProjection(writer, projection); err != nil {
		t.Fatalf("writeTaskSearchPlainProjection: %v", err)
	}
	if writer.lines != 5 {
		t.Fatalf("written line count = %d, want 5", writer.lines)
	}
}

type newlineCountingWriter struct {
	lines int
}

func (w *newlineCountingWriter) Write(data []byte) (int, error) {
	for _, value := range data {
		if value == '\n' {
			w.lines++
		}
	}
	return len(data), nil
}
