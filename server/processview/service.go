package processview

import (
	"context"
	"fmt"
	"strings"

	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type ProcessSource interface {
	List() []shelltool.Snapshot
	Snapshot(id string) (shelltool.Snapshot, error)
	Kill(id string) error
	InlineOutput(id string, maxChars int) (string, string, error)
}

type ProcessViewService struct {
	processes ProcessSource
}

func NewProcessViewService(processes ProcessSource) *ProcessViewService {
	return &ProcessViewService{processes: processes}
}

func (s *ProcessViewService) ListProcesses(_ context.Context, req serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
	if s == nil || s.processes == nil {
		return serverapi.ProcessListResponse{}, fmt.Errorf("process source is required")
	}
	ownerSessionID := strings.TrimSpace(req.OwnerSessionID)
	ownerRunID := strings.TrimSpace(req.OwnerRunID)
	snapshots := s.processes.List()
	processes := make([]clientui.BackgroundProcess, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if ownerSessionID != "" && strings.TrimSpace(snapshot.OwnerSessionID) != ownerSessionID {
			continue
		}
		if ownerRunID != "" && strings.TrimSpace(snapshot.OwnerRunID) != ownerRunID {
			continue
		}
		processes = append(processes, ProcessFromSnapshot(snapshot))
	}
	return serverapi.ProcessListResponse{Processes: processes}, nil
}

func (s *ProcessViewService) GetProcess(_ context.Context, req serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProcessGetResponse{}, err
	}
	if s == nil || s.processes == nil {
		return serverapi.ProcessGetResponse{}, fmt.Errorf("process source is required")
	}
	snapshot, err := s.processes.Snapshot(strings.TrimSpace(req.ProcessID))
	if err != nil {
		return serverapi.ProcessGetResponse{}, err
	}
	process := ProcessFromSnapshot(snapshot)
	return serverapi.ProcessGetResponse{Process: &process}, nil
}

func (s *ProcessViewService) KillProcess(ctx context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProcessKillResponse{}, err
	}
	if s == nil || s.processes == nil {
		return serverapi.ProcessKillResponse{}, fmt.Errorf("process source is required")
	}
	if err := ctx.Err(); err != nil {
		return serverapi.ProcessKillResponse{}, err
	}
	return serverapi.ProcessKillResponse{}, s.processes.Kill(strings.TrimSpace(req.ProcessID))
}

func (s *ProcessViewService) GetInlineOutput(_ context.Context, req serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProcessInlineOutputResponse{}, err
	}
	if s == nil || s.processes == nil {
		return serverapi.ProcessInlineOutputResponse{}, fmt.Errorf("process source is required")
	}
	output, logPath, err := s.processes.InlineOutput(strings.TrimSpace(req.ProcessID), req.MaxChars)
	if err != nil {
		return serverapi.ProcessInlineOutputResponse{}, err
	}
	return serverapi.ProcessInlineOutputResponse{Output: output, LogPath: logPath}, nil
}
