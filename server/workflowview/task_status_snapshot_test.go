package workflowview

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"core/server/workflowexecution"
)

func TestTaskStatusSnapshotCaptureRetriesLiveObservationInterleaving(t *testing.T) {
	var captures int
	var anchored int
	var closed int
	events := []string{}
	snapshot, err := taskStatusSnapshotCapture(
		t.Context(),
		workflowexecution.NewMutationPermit(),
		func() (taskStatusLiveSnapshot, error) {
			captures++
			events = append(events, fmt.Sprintf("capture-%d", captures))
			revision := uint64(3)
			if captures == 1 {
				revision = 1
			}
			if captures == 2 {
				revision = 2
			}
			return taskStatusLiveSnapshot{revision: revision}, nil
		},
		func(context.Context) (*TaskStatusSnapshot, error) {
			events = append(events, fmt.Sprintf("open-%d", anchored+1))
			return &TaskStatusSnapshot{
				anchor: func(context.Context) error {
					anchored++
					events = append(events, fmt.Sprintf("anchor-%d", anchored))
					return nil
				},
				close: func() error {
					closed++
					events = append(events, fmt.Sprintf("close-%d", closed))
					return nil
				},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("taskStatusSnapshotCapture: %v", err)
	}
	if captures != 4 || anchored != 2 || closed != 1 || snapshot.live.revision != 3 {
		t.Fatalf(
			"stable capture calls/candidates = captures:%d anchors:%d closed:%d revision:%d, want 4/2/1/3",
			captures,
			anchored,
			closed,
			snapshot.live.revision,
		)
	}
	wantEvents := []string{
		"capture-1", "open-1", "anchor-1", "capture-2", "close-1",
		"capture-3", "open-2", "anchor-2", "capture-4",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("stable capture event order = %v, want %v", events, wantEvents)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("close stable snapshot: %v", err)
	}
	if closed != 2 {
		t.Fatalf("closed snapshots = %d, want unstable and returned snapshots closed", closed)
	}
}

func TestTaskStatusSnapshotCaptureJoinsRollbackFailureWithCaptureAndAnchorFailures(t *testing.T) {
	captureErr := errors.New("live observation failed")
	anchorErr := errors.New("durable anchor failed")
	rollbackErr := errors.New("rollback failed")
	tests := []struct {
		name          string
		captureErrors []error
		anchor        func(context.Context) error
		want          error
	}{
		{
			name:          "after live observation",
			captureErrors: []error{nil, captureErr},
			anchor: func(context.Context) error {
				return nil
			},
			want: captureErr,
		},
		{
			name:          "durable anchor",
			captureErrors: []error{nil},
			anchor: func(context.Context) error {
				return anchorErr
			},
			want: anchorErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			captureLive := func() (taskStatusLiveSnapshot, error) {
				index := calls
				calls++
				if index >= len(test.captureErrors) {
					index = len(test.captureErrors) - 1
				}
				if err := test.captureErrors[index]; err != nil {
					return taskStatusLiveSnapshot{}, err
				}
				return taskStatusLiveSnapshot{revision: 1}, nil
			}
			_, err := taskStatusSnapshotCapture(
				t.Context(),
				workflowexecution.NewMutationPermit(),
				captureLive,
				func(context.Context) (*TaskStatusSnapshot, error) {
					return &TaskStatusSnapshot{
						anchor: test.anchor,
						close: func() error {
							return rollbackErr
						},
					}, nil
				},
			)
			if !errors.Is(err, test.want) || !errors.Is(err, rollbackErr) {
				t.Fatalf("capture error = %v, want both %v and %v", err, test.want, rollbackErr)
			}
		})
	}
}
