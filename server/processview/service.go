package processview

import (
	"context"
	"fmt"

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

type ProjectSessionMembership interface {
	ListProjectSessionIDs(ctx context.Context, projectID string) ([]string, error)
}

type ProcessViewService struct {
	processes  ProcessSource
	membership ProjectSessionMembership
}

func NewProcessViewService(processes ProcessSource, membership ProjectSessionMembership) *ProcessViewService {
	return &ProcessViewService{processes: processes, membership: membership}
}

func (s *ProcessViewService) ListProcesses(ctx context.Context, req serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
	if s == nil || s.processes == nil {
		return serverapi.ProcessListResponse{}, fmt.Errorf("process source is required")
	}
	if s.membership == nil {
		return serverapi.ProcessListResponse{}, fmt.Errorf("project session membership is required")
	}
	projectSessionIDs, err := s.membership.ListProjectSessionIDs(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProcessListResponse{}, err
	}
	projectSessions := make(map[string]struct{}, len(projectSessionIDs))
	for _, sessionID := range projectSessionIDs {
		projectSessions[sessionID] = struct{}{}
	}
	snapshots := s.processes.List()
	processes := make([]clientui.BackgroundProcess, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if _, matchesProject := projectSessions[snapshot.OwnerSessionID]; !matchesProject {
			continue
		}
		if req.OwnerSessionID != nil && snapshot.OwnerSessionID != *req.OwnerSessionID {
			continue
		}
		if req.OwnerRunID != nil && snapshot.OwnerRunID != *req.OwnerRunID {
			continue
		}
		processes = append(processes, ProcessFromSnapshot(snapshot))
	}
	return serverapi.ProcessListResponse{Processes: processes}, nil
}

func (s *ProcessViewService) GetProcess(_ context.Context, req serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error) {
	if s == nil || s.processes == nil {
		return serverapi.ProcessGetResponse{}, fmt.Errorf("process source is required")
	}
	snapshot, err := s.processes.Snapshot(req.ProcessID)
	if err != nil {
		return serverapi.ProcessGetResponse{}, err
	}
	process := ProcessFromSnapshot(snapshot)
	return serverapi.ProcessGetResponse{Process: &process}, nil
}

func (s *ProcessViewService) KillProcess(ctx context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error) {
	if s == nil || s.processes == nil {
		return serverapi.ProcessKillResponse{}, fmt.Errorf("process source is required")
	}
	if err := ctx.Err(); err != nil {
		return serverapi.ProcessKillResponse{}, err
	}
	return serverapi.ProcessKillResponse{}, s.processes.Kill(req.ProcessID)
}

func (s *ProcessViewService) GetInlineOutput(_ context.Context, req serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error) {
	if s == nil || s.processes == nil {
		return serverapi.ProcessInlineOutputResponse{}, fmt.Errorf("process source is required")
	}
	output, logPath, err := s.processes.InlineOutput(req.ProcessID, req.MaxChars)
	if err != nil {
		return serverapi.ProcessInlineOutputResponse{}, err
	}
	return serverapi.ProcessInlineOutputResponse{Output: output, LogPath: logPath}, nil
}
