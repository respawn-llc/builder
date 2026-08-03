package registry

import (
	"fmt"
	"strings"

	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

func (r *RuntimeRegistry) backgroundActivitiesForSession(sessionID string) ([]clientui.TranscriptBackgroundActivity, error) {
	if r == nil || r.backgroundProcessSnapshots == nil {
		return nil, nil
	}
	return transcriptBackgroundActivitiesFromProcessSnapshots(sessionID, r.backgroundProcessSnapshots())
}

func transcriptBackgroundActivitiesFromProcessSnapshots(
	sessionID string,
	snapshots []shelltool.Snapshot,
) ([]clientui.TranscriptBackgroundActivity, error) {
	sessionID = strings.TrimSpace(sessionID)
	out := make([]clientui.TranscriptBackgroundActivity, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !snapshot.Running || !snapshot.Backgrounded || strings.TrimSpace(snapshot.OwnerSessionID) != sessionID {
			continue
		}
		activityID, err := runtimeids.ParseBackgroundActivityID(snapshot.ActivityID.String())
		if err != nil {
			return nil, fmt.Errorf("background process %q activity id: %w", snapshot.ID, err)
		}
		runID, err := runtimeids.ParseRunID(snapshot.OwnerRunID)
		if err != nil {
			return nil, fmt.Errorf("background process %q owner run id: %w", snapshot.ID, err)
		}
		stepID, err := runtimeids.ParseStepID(snapshot.OwnerStepID)
		if err != nil {
			return nil, fmt.Errorf("background process %q owner step id: %w", snapshot.ID, err)
		}
		out = append(out, clientui.TranscriptBackgroundActivity{
			ActivityID:        activityID,
			ProcessID:         clientui.ProcessID(strings.TrimSpace(snapshot.ID)),
			OwnerRunID:        runID,
			OwnerStepID:       stepID,
			Lifecycle:         clientui.BackgroundLifecycleBackgrounded,
			Command:           snapshot.Command,
			Workdir:           snapshot.Workdir,
			LogPath:           textutil.OptionalTrimmedString(snapshot.LogPath),
			Preview:           nil,
			ExitCode:          nil,
			UserRequestedKill: false,
			NoticeSuppressed:  false,
		})
	}
	return out, nil
}
