package workflowview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type TaskSessions struct {
	queries    *sqlitegen.Queries
	activities ActiveTaskSessionActivitySource
}

type ActiveTaskSessionActivitySource interface {
	ActiveRuntimeActivitySnapshots(context.Context) ([]runtimeactivity.ActiveSessionSnapshot, error)
}

type taskSessionProjection struct {
	item            serverapi.WorkflowTaskSessionItem
	createdAtUnixMs int64
}

func NewTaskSessions(metadataStore *metadata.Store, activities ActiveTaskSessionActivitySource) (*TaskSessions, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if activities == nil {
		return nil, errors.New("active Task Session activity source is required")
	}
	return &TaskSessions{queries: metadataStore.Queries(), activities: activities}, nil
}

func (s *TaskSessions) List(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskSessionListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskSessionListResponse{}, err
	}
	window, err := serverapi.ResolveWorkflowOffsetWindow(req.Offset, req.Limit)
	if err != nil {
		return serverapi.WorkflowTaskSessionListResponse{}, err
	}
	return s.ReadSessions(ctx, req.TaskID, window)
}

func (s *TaskSessions) ReadSessions(ctx context.Context, taskID string, window serverapi.WorkflowOffsetWindow) (serverapi.WorkflowTaskSessionListResponse, error) {
	if s == nil || s.queries == nil {
		return serverapi.WorkflowTaskSessionListResponse{}, errors.New("Task Sessions read model is required")
	}
	if _, err := s.queries.GetTask(ctx, taskID); err != nil {
		return serverapi.WorkflowTaskSessionListResponse{}, err
	}
	active, activeSessionIDs, err := s.activeTaskSessions(ctx, taskID)
	if err != nil {
		return serverapi.WorkflowTaskSessionListResponse{}, err
	}
	capacity := window.Limit + 1
	items := make([]serverapi.WorkflowTaskSessionItem, 0, capacity)
	if window.Offset < len(active) {
		activeEnd := min(len(active), window.Offset+capacity)
		for _, projection := range active[window.Offset:activeEnd] {
			items = append(items, projection.item)
		}
	}
	remaining := capacity - len(items)
	if remaining > 0 {
		excludedSessionIDsJSON, err := json.Marshal(activeSessionIDs)
		if err != nil {
			return serverapi.WorkflowTaskSessionListResponse{}, err
		}
		idleOffset := max(window.Offset-len(active), 0)
		rows, err := s.queries.ListIdleWorkflowTaskSessions(ctx, sqlitegen.ListIdleWorkflowTaskSessionsParams{
			TaskID:                 sql.NullString{String: taskID, Valid: true},
			ExcludedSessionIdsJson: string(excludedSessionIDsJSON),
			PageOffset:             int64(idleOffset),
			PageLimit:              int64(remaining),
		})
		if err != nil {
			return serverapi.WorkflowTaskSessionListResponse{}, err
		}
		for _, row := range rows {
			projection, err := taskSessionProjectionFromFields(
				row.SessionID,
				row.SessionName,
				row.NodeName,
				row.ContinuationJson,
				row.CreatedAtUnixMs,
				serverapi.WorkflowTaskSessionStatusIdle,
			)
			if err != nil {
				return serverapi.WorkflowTaskSessionListResponse{}, err
			}
			items = append(items, projection.item)
		}
	}
	page := serverapi.FinalizeWorkflowOffsetPage(window, items)
	return serverapi.WorkflowTaskSessionListResponse{
		TaskID:             taskID,
		WorkflowOffsetPage: page,
	}, nil
}

func (s *TaskSessions) activeTaskSessions(ctx context.Context, taskID string) ([]taskSessionProjection, []string, error) {
	snapshots, err := s.activities.ActiveRuntimeActivitySnapshots(ctx)
	if err != nil {
		return nil, nil, err
	}
	statuses := make(map[string]serverapi.WorkflowTaskSessionStatus, len(snapshots))
	candidateIDs := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		status, err := taskSessionStatus(snapshot.Activity)
		if err != nil {
			return nil, nil, fmt.Errorf("Session %q runtime activity: %w", snapshot.SessionID, err)
		}
		if status == serverapi.WorkflowTaskSessionStatusIdle {
			continue
		}
		statuses[snapshot.SessionID] = status
		candidateIDs = append(candidateIDs, snapshot.SessionID)
	}
	if len(candidateIDs) == 0 {
		return []taskSessionProjection{}, []string{}, nil
	}
	sessionIDsJSON, err := json.Marshal(candidateIDs)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.queries.ListActiveWorkflowTaskSessions(ctx, sqlitegen.ListActiveWorkflowTaskSessionsParams{
		TaskID:         sql.NullString{String: taskID, Valid: true},
		SessionIdsJson: string(sessionIDsJSON),
	})
	if err != nil {
		return nil, nil, err
	}
	active := make([]taskSessionProjection, 0, len(rows))
	activeSessionIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		status, exists := statuses[row.SessionID]
		if !exists {
			return nil, nil, fmt.Errorf("active Task Session query returned uncaptured Session %q", row.SessionID)
		}
		projection, err := taskSessionProjectionFromFields(
			row.SessionID,
			row.SessionName,
			row.NodeName,
			row.ContinuationJson,
			row.CreatedAtUnixMs,
			status,
		)
		if err != nil {
			return nil, nil, err
		}
		active = append(active, projection)
		activeSessionIDs = append(activeSessionIDs, row.SessionID)
	}
	sort.Slice(active, func(left int, right int) bool {
		leftRank := taskSessionStatusRank(active[left].item.Status)
		rightRank := taskSessionStatusRank(active[right].item.Status)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if active[left].createdAtUnixMs != active[right].createdAtUnixMs {
			return active[left].createdAtUnixMs > active[right].createdAtUnixMs
		}
		return active[left].item.SessionID > active[right].item.SessionID
	})
	return active, activeSessionIDs, nil
}

func taskSessionStatus(activity clientui.RuntimeActivity) (serverapi.WorkflowTaskSessionStatus, error) {
	if err := activity.Validate(); err != nil {
		return "", err
	}
	switch activity.State {
	case clientui.RuntimeActivityAwaitingPrompt:
		return serverapi.WorkflowTaskSessionStatusQuestion, nil
	case clientui.RuntimeActivityStarting,
		clientui.RuntimeActivityRunning,
		clientui.RuntimeActivityDraining,
		clientui.RuntimeActivityClosing:
		return serverapi.WorkflowTaskSessionStatusRunning, nil
	case clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityUnavailable:
		return serverapi.WorkflowTaskSessionStatusIdle, nil
	default:
		return "", fmt.Errorf("unsupported runtime activity state %q", activity.State)
	}
}

func taskSessionStatusRank(status serverapi.WorkflowTaskSessionStatus) int {
	switch status {
	case serverapi.WorkflowTaskSessionStatusRunning:
		return 0
	case serverapi.WorkflowTaskSessionStatusQuestion:
		return 1
	case serverapi.WorkflowTaskSessionStatusIdle:
		return 2
	default:
		panic(fmt.Sprintf("unknown Task Session status %q", status))
	}
}

func taskSessionProjectionFromFields(
	sessionID string,
	sessionName string,
	nodeName sql.NullString,
	continuationJSON string,
	createdAtUnixMs int64,
	status serverapi.WorkflowTaskSessionStatus,
) (taskSessionProjection, error) {
	agentRole, err := taskSessionAgentRole(continuationJSON)
	if err != nil {
		return taskSessionProjection{}, fmt.Errorf("Session %q Agent role: %w", sessionID, err)
	}
	return taskSessionProjection{
		item: serverapi.WorkflowTaskSessionItem{
			SessionID:   sessionID,
			SessionName: optionalTaskSessionString(sessionName),
			NodeName:    metadata.OptionalString(nodeName),
			AgentRole:   agentRole,
			Status:      status,
		},
		createdAtUnixMs: createdAtUnixMs,
	}, nil
}

func taskSessionAgentRole(continuationJSON string) (string, error) {
	var continuation session.ContinuationContext
	if err := json.Unmarshal([]byte(continuationJSON), &continuation); err != nil {
		return "", fmt.Errorf("decode continuation: %w", err)
	}
	normalized, err := session.NormalizeContinuationContext(continuation)
	if err != nil {
		return "", err
	}
	if normalized == nil || normalized.AgentRole == nil {
		return workflow.DefaultAgentRole, nil
	}
	return *normalized.AgentRole, nil
}

func optionalTaskSessionString(value string) *string {
	if value == "" {
		return nil
	}
	copied := value
	return &copied
}
