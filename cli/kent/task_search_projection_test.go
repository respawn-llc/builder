package main

import (
	"bytes"
	"reflect"
	"strings"
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

func TestTaskSearchPlainRendererWritesCompleteHierarchyWithoutBlankMetadataRows(t *testing.T) {
	projection := taskSearchPlainProjection{
		Groups: []taskSearchPlainGroup{{
			ShortID: "KNT-1",
			Title:   "Task",
			Lines: []taskSearchPlainLine{
				{Kind: taskSearchPlainLineKindHit, FTS5: &serverapi.TaskSearchFTS5Hit{Snippet: "one"}},
				{Kind: taskSearchPlainLineKindCommentHeading},
				{Kind: taskSearchPlainLineKindHit, FTS5: &serverapi.TaskSearchFTS5Hit{Snippet: "two"}},
			},
			RemainingHitCount: 1,
		}},
	}
	var output bytes.Buffer

	if err := writeTaskSearchPlainProjection(&output, projection); err != nil {
		t.Fatalf("writeTaskSearchPlainProjection: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	want := []string{
		taskSearchPlainTaskHeader("KNT-1", "Task"),
		"one",
		taskSearchPlainCommentHeading,
		"two",
		taskSearchPlainRemainingHitsLine(1),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plain output lines = %#v, want %#v", got, want)
	}
}

func TestTaskSearchPlainFragmentUsesOnlyStructuredLiteralEllipsesAndFoldsWhitespace(t *testing.T) {
	literal, err := taskSearchPlainFragment(taskSearchPlainLine{
		Kind: taskSearchPlainLineKindHit,
		Literal: &serverapi.TaskSearchLiteralHit{
			Before:         "  before\t",
			Match:          "needle",
			After:          "\nafter  ",
			LeftTruncated:  true,
			RightTruncated: true,
		},
	})
	if err != nil {
		t.Fatalf("taskSearchPlainFragment literal: %v", err)
	}
	if literal != taskSearchPlainLiteralOmissionMarker+" before needle after "+taskSearchPlainLiteralOmissionMarker {
		t.Fatalf("literal fragment = %q", literal)
	}
	raw, err := taskSearchPlainFragment(taskSearchPlainLine{
		Kind: taskSearchPlainLineKindHit,
		FTS5: &serverapi.TaskSearchFTS5Hit{Snippet: "  raw\tfragment  "},
	})
	if err != nil {
		t.Fatalf("taskSearchPlainFragment FTS5: %v", err)
	}
	if raw != "raw fragment" {
		t.Fatalf("raw fragment = %q", raw)
	}
}

func TestTaskSearchPlainProjectionOmitsConditionalCommentAndRemainingLines(t *testing.T) {
	response := serverapi.TaskSearchResponse{
		Mode: serverapi.TaskSearchModeFTS5,
		Groups: []serverapi.TaskSearchGroup{{
			ProjectID:     "project-1",
			ProjectKey:    "KNT",
			TaskID:        "task-1",
			ShortID:       "KNT-1",
			WorkflowID:    "workflow-1",
			Title:         "Task",
			Status:        serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindBacklog, NativeState: serverapi.WorkflowTaskNativeStateActive},
			TotalHitCount: 2,
			Hits: []serverapi.TaskSearchHit{
				{Ordinal: 1, Source: serverapi.TaskSearchSource{Kind: serverapi.TaskSearchSourceKindTitle}, FTS5: &serverapi.TaskSearchFTS5Hit{Snippet: "one"}},
				{Ordinal: 2, Source: serverapi.TaskSearchSource{Kind: serverapi.TaskSearchSourceKindBody}, FTS5: &serverapi.TaskSearchFTS5Hit{Snippet: "two"}},
			},
		}},
	}
	projection, err := taskSearchPlainProjectionFromResponse(response)
	if err != nil {
		t.Fatalf("taskSearchPlainProjectionFromResponse: %v", err)
	}
	group := projection.Groups[0]
	if len(group.Lines) != 2 ||
		group.Lines[0].Kind != taskSearchPlainLineKindHit ||
		group.Lines[1].Kind != taskSearchPlainLineKindHit ||
		group.RemainingHitCount != 0 {
		t.Fatalf("projection without Comment/remaining lines = %#v", group)
	}
}

func TestTaskSearchPlainRendererPrintsEmptyResponseWithoutMetadataRows(t *testing.T) {
	var output bytes.Buffer
	if err := writeTaskSearchPlainProjection(&output, taskSearchPlainProjection{}); err != nil {
		t.Fatalf("writeTaskSearchPlainProjection: %v", err)
	}
	if output.String() != taskSearchPlainNoMatchesLine+"\n" {
		t.Fatalf("empty plain output = %q", output.String())
	}
}

func TestTaskSearchPlainRendererRejectsInvalidHitPayloads(t *testing.T) {
	t.Parallel()
	literal := &serverapi.TaskSearchLiteralHit{Match: "literal"}
	fts5 := &serverapi.TaskSearchFTS5Hit{Snippet: "raw"}
	for _, test := range []struct {
		name string
		line taskSearchPlainLine
	}{
		{name: "missing", line: taskSearchPlainLine{Kind: taskSearchPlainLineKindHit}},
		{name: "mixed", line: taskSearchPlainLine{Kind: taskSearchPlainLineKindHit, Literal: literal, FTS5: fts5}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projection := taskSearchPlainProjection{Groups: []taskSearchPlainGroup{{
				ShortID: "KNT-1",
				Title:   "Task",
				Lines:   []taskSearchPlainLine{test.line},
			}}}
			if err := writeTaskSearchPlainProjection(&bytes.Buffer{}, projection); err == nil {
				t.Fatal("writeTaskSearchPlainProjection accepted an invalid hit payload")
			}
		})
	}
}
