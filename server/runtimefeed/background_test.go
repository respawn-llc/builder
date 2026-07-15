package runtimefeed

import (
	"testing"

	"core/shared/runtimeids"
)

func TestTranscriptBackgroundActivityUsesTypedLifecycleInsteadOfRemovedFlag(t *testing.T) {
	activityID, err := runtimeids.ParseBackgroundActivityID("66666666-6666-4666-8666-666666666666")
	if err != nil {
		t.Fatalf("parse background activity id: %v", err)
	}
	activity := TranscriptBackgroundActivity{
		ActivityID:  activityID,
		ProcessID:   ProcessID("process-1"),
		OwnerRunID:  runtimefeedTestRunID(t),
		OwnerStepID: runtimefeedTestStepID(t),
		Lifecycle:   BackgroundLifecycleBackgrounded,
		Command:     "go test ./...",
		Workdir:     "/repo",
	}
	if err := activity.Validate(); err != nil {
		t.Fatalf("validate background activity: %v", err)
	}
}

func TestTranscriptBackgroundLifecycleRejectsTerminalSentinelCombinations(t *testing.T) {
	activityID, err := runtimeids.ParseBackgroundActivityID("66666666-6666-4666-8666-666666666666")
	if err != nil {
		t.Fatalf("parse background activity id: %v", err)
	}
	exitCode := 0
	base := TranscriptBackgroundActivity{
		ActivityID:  activityID,
		ProcessID:   ProcessID("process-1"),
		OwnerRunID:  runtimefeedTestRunID(t),
		OwnerStepID: runtimefeedTestStepID(t),
		Lifecycle:   BackgroundLifecycleBackgrounded,
		Command:     "go test ./...",
		Workdir:     "/repo",
	}
	tests := []TranscriptBackgroundActivity{
		func() TranscriptBackgroundActivity {
			activity := base
			activity.ExitCode = &exitCode
			return activity
		}(),
		func() TranscriptBackgroundActivity {
			activity := base
			activity.UserRequestedKill = true
			return activity
		}(),
		func() TranscriptBackgroundActivity {
			activity := base
			activity.Lifecycle = BackgroundLifecycleCompleted
			activity.UserRequestedKill = true
			return activity
		}(),
		func() TranscriptBackgroundActivity {
			activity := base
			activity.Lifecycle = BackgroundLifecycle("unknown")
			return activity
		}(),
	}
	for _, activity := range tests {
		if err := activity.Validate(); err == nil {
			t.Fatalf("accepted invalid background lifecycle: %+v", activity)
		}
	}
}
