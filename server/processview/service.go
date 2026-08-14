package processview

import (
	"context"
	"fmt"
	"strings"

	"core/server/requestmemo"
	shelltool "core/server/tools/shell"
	"core/shared/apicontract"
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
	kills     *requestmemo.Memo[killRequestMemoRequest, serverapi.ProcessKillResponse]
}

type killRequestMemoRequest struct {
	ProcessID string
}

func NewProcessViewService(processes ProcessSource) *ProcessViewService {
	return &ProcessViewService{processes: processes, kills: requestmemo.New[killRequestMemoRequest, serverapi.ProcessKillResponse]()}
}

func (s *ProcessViewService) ResolveProcessAuthorization(_ context.Context, processID string) (apicontract.ProcessAuthorizationCandidate, error) {
	if s == nil || s.processes == nil {
		return apicontract.ProcessAuthorizationCandidate{}, fmt.Errorf("process source is required")
	}
	snapshot, err := s.processes.Snapshot(strings.TrimSpace(processID))
	if err != nil {
		return apicontract.ProcessAuthorizationCandidate{}, err
	}
	process := ProcessFromSnapshot(snapshot)
	return apicontract.ProcessAuthorizationCandidate{
		ProcessID:      process.ID,
		OwnerSessionID: process.OwnerSessionID,
		Process:        process,
	}, nil
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
	return apicontract.WithValidated(req, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.ProcessGetRequest]) (serverapi.ProcessGetResponse, error) {
		return s.getProcess(validated.Value().ProcessID)
	})
}

func (s *ProcessViewService) getProcess(processID string) (serverapi.ProcessGetResponse, error) {
	if s == nil || s.processes == nil {
		return serverapi.ProcessGetResponse{}, fmt.Errorf("process source is required")
	}
	snapshot, err := s.processes.Snapshot(strings.TrimSpace(processID))
	if err != nil {
		return serverapi.ProcessGetResponse{}, err
	}
	process := ProcessFromSnapshot(snapshot)
	return serverapi.ProcessGetResponse{Process: &process}, nil
}

func (s *ProcessViewService) GetProcessValidated(_ context.Context, _ apicontract.Validated[serverapi.ProcessGetRequest], authorization apicontract.AuthorizedProcessInActiveProject) (serverapi.ProcessGetResponse, error) {
	process := authorization.Process
	return serverapi.ProcessGetResponse{Process: &process}, nil
}

func (s *ProcessViewService) KillProcess(ctx context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error) {
	return apicontract.WithValidated(req, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.ProcessKillRequest]) (serverapi.ProcessKillResponse, error) {
		request := validated.Value()
		return s.killProcess(ctx, request.ClientRequestID, request.ProcessID)
	})
}

func (s *ProcessViewService) KillProcessValidated(ctx context.Context, req apicontract.Validated[serverapi.ProcessKillRequest], authorization apicontract.AuthorizedProcessInActiveProject) (serverapi.ProcessKillResponse, error) {
	return s.killProcess(ctx, req.Value().ClientRequestID, authorization.ProcessID)
}

func (s *ProcessViewService) killProcess(ctx context.Context, clientRequestID string, processID string) (serverapi.ProcessKillResponse, error) {
	if s == nil || s.processes == nil {
		return serverapi.ProcessKillResponse{}, fmt.Errorf("process source is required")
	}
	memoReq := killRequestMemoRequest{ProcessID: strings.TrimSpace(processID)}
	return s.kills.Do(ctx, strings.TrimSpace(clientRequestID), memoReq, func(a killRequestMemoRequest, b killRequestMemoRequest) bool { return a.ProcessID == b.ProcessID }, func(ctx context.Context) (serverapi.ProcessKillResponse, error) {
		if err := ctx.Err(); err != nil {
			return serverapi.ProcessKillResponse{}, err
		}
		return serverapi.ProcessKillResponse{}, s.processes.Kill(memoReq.ProcessID)
	})
}

func (s *ProcessViewService) GetInlineOutput(_ context.Context, req serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error) {
	return apicontract.WithValidated(req, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.ProcessInlineOutputRequest]) (serverapi.ProcessInlineOutputResponse, error) {
		request := validated.Value()
		return s.getInlineOutput(request.ProcessID, request.MaxChars)
	})
}

func (s *ProcessViewService) GetInlineOutputValidated(_ context.Context, req apicontract.Validated[serverapi.ProcessInlineOutputRequest], authorization apicontract.AuthorizedProcessInActiveProject) (serverapi.ProcessInlineOutputResponse, error) {
	return s.getInlineOutput(authorization.ProcessID, req.Value().MaxChars)
}

func (s *ProcessViewService) getInlineOutput(processID string, maxChars int) (serverapi.ProcessInlineOutputResponse, error) {
	if s == nil || s.processes == nil {
		return serverapi.ProcessInlineOutputResponse{}, fmt.Errorf("process source is required")
	}
	output, logPath, err := s.processes.InlineOutput(strings.TrimSpace(processID), maxChars)
	if err != nil {
		return serverapi.ProcessInlineOutputResponse{}, err
	}
	return serverapi.ProcessInlineOutputResponse{Output: output, LogPath: logPath}, nil
}
