package runtimewirefixture

import (
	"path/filepath"

	shelltool "core/server/tools/shell"

	"github.com/google/uuid"
)

func BackgroundCompletionEvent(id string, ownerSessionID string, root string) shelltool.Event {
	exitCode := 0
	return shelltool.Event{
		Type: shelltool.EventCompleted,
		Snapshot: shelltool.Snapshot{
			ID:             id,
			ActivityID:     uuid.New(),
			OwnerSessionID: ownerSessionID,
			State:          "completed",
			Command:        "kent run",
			Workdir:        root,
			LogPath:        filepath.Join(root, id+".log"),
			ExitCode:       &exitCode,
		},
	}
}
