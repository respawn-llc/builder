package registry

import (
	"testing"

	shelltool "core/server/tools/shell"
	"github.com/google/uuid"
)

func TestBackgroundHydrationFiltersRunningSessionProcessesWithoutPreview(t *testing.T) {
	snapshots, err := transcriptBackgroundActivitiesFromProcessSnapshots("session-1", []shelltool.Snapshot{
		{
			ID:             "process-1",
			ActivityID:     uuid.MustParse("66666666-6666-4666-8666-666666666666"),
			OwnerSessionID: "session-1",
			OwnerRunID:     "11111111-1111-4111-8111-111111111111",
			OwnerStepID:    "22222222-2222-4222-8222-222222222222",
			Command:        "go test ./...",
			Workdir:        "/workspace",
			LogPath:        "/tmp/process.log",
			RecentOutput:   "\x1b[31mraw output\x1b[0m",
			Running:        true,
			Backgrounded:   true,
		},
		{
			ID:             "process-terminal",
			ActivityID:     uuid.MustParse("77777777-7777-4777-8777-777777777777"),
			OwnerSessionID: "session-1",
			Running:        false,
			Backgrounded:   true,
		},
		{
			ID:             "process-other-session",
			OwnerSessionID: "session-2",
			Running:        true,
			Backgrounded:   true,
		},
	})
	if err != nil {
		t.Fatalf("project background hydration: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("background hydration count = %d, want one", len(snapshots))
	}
	activity := snapshots[0]
	if activity.Preview != nil {
		t.Fatalf("background hydration preview = %q, want nil", *activity.Preview)
	}
	if activity.ProcessID != "process-1" ||
		activity.Command != "go test ./..." ||
		activity.Workdir != "/workspace" ||
		activity.LogPath == nil ||
		*activity.LogPath != "/tmp/process.log" {
		t.Fatalf("background hydration = %+v", activity)
	}
}
