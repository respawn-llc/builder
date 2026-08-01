package workflowrunner

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
)

type taskCommentCountProbe struct {
	count int64
	err   error
	calls int
}

func (p *taskCommentCountProbe) CountTaskComments(context.Context, workflow.TaskID) (int64, error) {
	p.calls++
	return p.count, p.err
}

type taskDependencyCountProbe struct {
	count int
	err   error
	calls int
}

func (p *taskDependencyCountProbe) CountUnsatisfiedBlockers(context.Context, string) (int, error) {
	p.calls++
	return p.count, p.err
}

func TestTaskAwarenessSourceComposesCommentsAndFocusedDependencies(t *testing.T) {
	comments := &taskCommentCountProbe{count: 4}
	dependencies := &taskDependencyCountProbe{count: 2}
	source, err := NewTaskAwarenessSource(comments, dependencies)
	if err != nil {
		t.Fatalf("NewTaskAwarenessSource: %v", err)
	}

	awareness, err := source.TaskAwareness(context.Background(), workflow.TaskID("task-1"))
	if err != nil {
		t.Fatalf("TaskAwareness: %v", err)
	}
	if awareness.CommentCount != 4 || awareness.UnsatisfiedDependencyCount != 2 {
		t.Fatalf("Task awareness = %+v, want comment/dependency counts 4/2", awareness)
	}
	if comments.calls != 1 || dependencies.calls != 1 {
		t.Fatalf("awareness source calls = comments:%d dependencies:%d, want one each", comments.calls, dependencies.calls)
	}
}

func TestTaskAwarenessSourceStopsBeforeDependenciesWhenCommentsFail(t *testing.T) {
	countErr := errors.New("comment count unavailable")
	comments := &taskCommentCountProbe{err: countErr}
	dependencies := &taskDependencyCountProbe{count: 2}
	source, err := NewTaskAwarenessSource(comments, dependencies)
	if err != nil {
		t.Fatalf("NewTaskAwarenessSource: %v", err)
	}

	if _, err := source.TaskAwareness(context.Background(), workflow.TaskID("task-1")); !errors.Is(err, countErr) {
		t.Fatalf("TaskAwareness error = %v, want comment count error", err)
	}
	if dependencies.calls != 0 {
		t.Fatalf("dependency count calls = %d, want zero after comment failure", dependencies.calls)
	}
}
