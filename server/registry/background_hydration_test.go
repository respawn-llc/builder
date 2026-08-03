package registry
import (
	shelltool "core/server/tools/shell"
	"github.com/google/uuid"
	"testing"
)
func TestBackgroundHydrationFiltersSessionProcessesAndOmitsPreview(t *testing.T) {
	activities, err := transcriptBackgroundActivitiesFromProcessSnapshots("session-1", []shelltool.Snapshot{
		{
			ID: "process-1", ActivityID: uuid.MustParse("66666666-6666-4666-8666-666666666666"),
			OwnerSessionID: "session-1", OwnerRunID: "11111111-1111-4111-8111-111111111111",
			OwnerStepID: "22222222-2222-4222-8222-222222222222", Command: "go test ./...",
			Workdir: "/workspace", LogPath: "/tmp/process.log", RecentOutput: "\x1b[31mraw\x1b[0m",
			Running: true, Backgrounded: true,
		},
		{ID: "terminal", OwnerSessionID: "session-1", Running: false, Backgrounded: true},
		{ID: "other", OwnerSessionID: "session-2", Running: true, Backgrounded: true},
	})
	if err != nil {
		t.Fatalf("project background hydration: %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("background activities = %d, want one", len(activities))
	}
	activity := activities[0]
	if activity.Preview != nil || activity.ProcessID != "process-1" ||
		activity.Command != "go test ./..." || activity.Workdir != "/workspace" ||
		activity.LogPath == nil || *activity.LogPath != "/tmp/process.log" {
		t.Fatalf("background activity = %+v", activity)
	}
}
