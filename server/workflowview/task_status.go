package workflowview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type workflowTaskStatusFact struct {
	Status serverapi.WorkflowTaskStatus
	Done   bool
}

func (s *Service) taskStatus(ctx context.Context, taskID string) (serverapi.WorkflowTaskStatus, error) {
	fact, err := s.taskStatusFact(ctx, taskID)
	if err != nil {
		return serverapi.WorkflowTaskStatus{}, err
	}
	return fact.Status, nil
}

func (s *Service) taskStatusFact(ctx context.Context, taskID string) (workflowTaskStatusFact, error) {
	row, err := s.queries.GetWorkflowTaskStatusRecord(ctx, taskID)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	return workflowTaskStatusFactFromRecord(row)
}

func (s *Service) taskStatusFacts(ctx context.Context, taskIDs []string) (map[string]workflowTaskStatusFact, error) {
	if len(taskIDs) == 0 {
		return map[string]workflowTaskStatusFact{}, nil
	}
	rows, err := s.queries.ListWorkflowTaskStatusRecordsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]workflowTaskStatusFact, len(taskIDs))
	for _, row := range rows {
		if _, exists := statuses[row.TaskID]; exists {
			return nil, fmt.Errorf("workflow task status projection returned duplicate task %q", row.TaskID)
		}
		status, err := workflowTaskStatusFactFromRecord(row)
		if err != nil {
			return nil, err
		}
		statuses[row.TaskID] = status
	}
	for _, taskID := range taskIDs {
		if _, exists := statuses[taskID]; !exists {
			return nil, fmt.Errorf("workflow task status projection omitted task %q", taskID)
		}
	}
	return statuses, nil
}

func workflowTaskStatusFactFromRecord(row sqlitegen.WorkflowTaskStatusRecord) (workflowTaskStatusFact, error) {
	status, err := workflowTaskStatusFromFields(row.TaskID, row.Kind, row.NativeState, row.NodeIdsJson, row.RunIdsJson, row.AttentionTypesJson)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	return workflowTaskStatusFact{Status: status, Done: row.IsDone != 0}, nil
}

func workflowTaskStatusFromFields(taskID string, kindValue string, nativeState string, nodeIDsValue any, runIDsValue any, attentionTypesValue any) (serverapi.WorkflowTaskStatus, error) {
	kind := serverapi.WorkflowTaskStatusKind(kindValue)
	if !validWorkflowTaskStatusNativeState(kind, nativeState) {
		return serverapi.WorkflowTaskStatus{}, fmt.Errorf("workflow task status record for task %q has invalid kind/native state %q/%q", taskID, kindValue, nativeState)
	}
	nodeIDsJSON, err := workflowTaskStatusProjectionJSON(taskID, "node_ids_json", nodeIDsValue)
	if err != nil {
		return serverapi.WorkflowTaskStatus{}, err
	}
	nodeIDs, err := workflowTaskStatusIDs(taskID, "node_ids_json", nodeIDsJSON)
	if err != nil {
		return serverapi.WorkflowTaskStatus{}, err
	}
	runIDsJSON, err := workflowTaskStatusProjectionJSON(taskID, "run_ids_json", runIDsValue)
	if err != nil {
		return serverapi.WorkflowTaskStatus{}, err
	}
	runIDs, err := workflowTaskStatusIDs(taskID, "run_ids_json", runIDsJSON)
	if err != nil {
		return serverapi.WorkflowTaskStatus{}, err
	}
	attentionTypesJSON, err := workflowTaskStatusProjectionJSON(taskID, "attention_types_json", attentionTypesValue)
	if err != nil {
		return serverapi.WorkflowTaskStatus{}, err
	}
	attentionTypes, err := workflowTaskStatusAttentionTypes(taskID, attentionTypesJSON)
	if err != nil {
		return serverapi.WorkflowTaskStatus{}, err
	}
	return serverapi.WorkflowTaskStatus{
		Kind:           kind,
		NativeState:    nativeState,
		NodeIDs:        nodeIDs,
		RunIDs:         runIDs,
		AttentionTypes: attentionTypes,
	}, nil
}

func workflowTaskStatusProjectionJSON(taskID string, field string, value any) (string, error) {
	encoded, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("workflow task status record for task %q has non-text %s", taskID, field)
	}
	return encoded, nil
}

func validWorkflowTaskStatusNativeState(kind serverapi.WorkflowTaskStatusKind, nativeState string) bool {
	switch kind {
	case serverapi.WorkflowTaskStatusKindCanceled:
		return nativeState == "canceled"
	case serverapi.WorkflowTaskStatusKindDone:
		return nativeState == "terminal"
	case serverapi.WorkflowTaskStatusKindWaitingQuestion:
		return nativeState == "waiting_ask"
	case serverapi.WorkflowTaskStatusKindWaitingApproval:
		return nativeState == "waiting_approval"
	case serverapi.WorkflowTaskStatusKindInterrupted:
		return nativeState == "interrupted"
	case serverapi.WorkflowTaskStatusKindRunning:
		return nativeState == "running"
	case serverapi.WorkflowTaskStatusKindQueued:
		return nativeState == "queued"
	case serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindActive:
		return nativeState == "active"
	default:
		return false
	}
}

func workflowTaskStatusIDs(taskID string, field string, encoded string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, fmt.Errorf("workflow task status record for task %q has malformed %s: %w", taskID, field, err)
	}
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("workflow task status record for task %q has blank %s[%d]", taskID, field, index)
		}
		if index > 0 && values[index-1] >= value {
			return nil, fmt.Errorf("workflow task status record for task %q has non-deterministic %s", taskID, field)
		}
	}
	return values, nil
}

func workflowTaskStatusAttentionTypes(taskID string, encoded string) ([]serverapi.WorkflowTaskAttentionKind, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, fmt.Errorf("workflow task status record for task %q has malformed attention_types_json: %w", taskID, err)
	}
	out := make([]serverapi.WorkflowTaskAttentionKind, 0, len(values))
	for index, value := range values {
		kind := serverapi.WorkflowTaskAttentionKind(value)
		switch kind {
		case serverapi.WorkflowTaskAttentionKindApproval, serverapi.WorkflowTaskAttentionKindInterrupted, serverapi.WorkflowTaskAttentionKindQuestion:
		default:
			return nil, fmt.Errorf("workflow task status record for task %q has unknown attention_types_json[%d] %q", taskID, index, value)
		}
		if index > 0 && values[index-1] >= value {
			return nil, fmt.Errorf("workflow task status record for task %q has non-deterministic attention_types_json", taskID)
		}
		out = append(out, kind)
	}
	return out, nil
}
