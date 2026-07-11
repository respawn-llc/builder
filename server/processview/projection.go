package processview

import (
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/valuecopy"
)

func ProcessFromSnapshot(snapshot shelltool.Snapshot) clientui.BackgroundProcess {
	return clientui.BackgroundProcess{
		ID:                      snapshot.ID,
		OwnerSessionID:          snapshot.OwnerSessionID,
		OwnerRunID:              snapshot.OwnerRunID,
		OwnerStepID:             snapshot.OwnerStepID,
		State:                   snapshot.State,
		Command:                 snapshot.Command,
		Workdir:                 snapshot.Workdir,
		StartedAt:               snapshot.StartedAt,
		FinishedAt:              snapshot.FinishedAt,
		ExitCode:                valuecopy.Pointer(snapshot.ExitCode),
		LogPath:                 snapshot.LogPath,
		RecentOutput:            snapshot.RecentOutput,
		OutputAvailable:         snapshot.OutputAvailable,
		OutputRetainedFromBytes: snapshot.OutputRetainedFromBytes,
		OutputRetainedToBytes:   snapshot.OutputRetainedToBytes,
		Running:                 snapshot.Running,
		StdinOpen:               snapshot.StdinOpen,
		Backgrounded:            snapshot.Backgrounded,
		KillRequested:           snapshot.KillRequested,
		LastUpdatedAt:           snapshot.LastUpdatedAt,
	}
}
