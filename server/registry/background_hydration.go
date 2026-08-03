package registry

import (
	"core/server/runtimeview"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
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
	return runtimeview.TranscriptBackgroundActivitiesFromProcessSnapshots(sessionID, snapshots)
}
