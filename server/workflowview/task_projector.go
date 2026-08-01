package workflowview

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/serverapi"
)

type TaskProjector struct{}

type TaskStatusInput struct {
	TaskID             string
	Kind               string
	NodeIDsJSON        string
	AttentionTypesJSON string
	Done               bool
}

type TaskFactsInput struct {
	Task           sqlitegen.TaskRecord
	Status         workflowTaskStatusFact
	CurrentNodes   []workflow.CurrentNode
	LiveExecutions []sessionruntime.TaskExecution
	Definition     definitionSnapshot
	CanDelete      bool
}

type TaskFacts struct {
	Summary serverapi.WorkflowTaskSummary
	Status  serverapi.WorkflowTaskStatus
	Actions serverapi.WorkflowTaskActions
	Done    bool
}

func NewTaskProjector() *TaskProjector {
	return &TaskProjector{}
}

func (*TaskProjector) DecodeStatus(input TaskStatusInput) (workflowTaskStatusFact, error) {
	kind := serverapi.WorkflowTaskStatusKind(input.Kind)
	nativeState, valid := kind.NativeState()
	if !valid {
		return workflowTaskStatusFact{}, fmt.Errorf("workflow task status record for task %q has invalid kind %q", input.TaskID, input.Kind)
	}
	nodeIDs, err := workflowTaskStatusIDs(input.TaskID, "node_ids_json", input.NodeIDsJSON)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	attentionTypes, err := workflowTaskStatusAttentionTypes(input.TaskID, input.AttentionTypesJSON)
	if err != nil {
		return workflowTaskStatusFact{}, err
	}
	return workflowTaskStatusFact{
		Status: serverapi.WorkflowTaskStatus{
			Kind:           kind,
			NativeState:    nativeState,
			NodeIDs:        nodeIDs,
			AttentionTypes: attentionTypes,
		},
		Done: input.Done,
	}, nil
}

func (*TaskProjector) ProjectTaskFacts(input TaskFactsInput) TaskFacts {
	done := input.Status.Done || currentNodesContainTerminal(input.CurrentNodes, input.Definition.nodeKinds)
	return TaskFacts{
		Summary: taskSummary(input.Task, input.Status.Status, done),
		Status:  input.Status.Status,
		Actions: taskActions(done, input.Status.Status, input.CurrentNodes, input.LiveExecutions, input.Definition.api, input.Definition.nodeKinds, input.CanDelete),
		Done:    done,
	}
}

func (*TaskProjector) ProjectComment(comment sqlitegen.TaskComment) serverapi.WorkflowTaskComment {
	return serverapi.WorkflowTaskComment{
		ID:              comment.ID,
		TaskID:          comment.TaskID,
		Body:            comment.Body,
		Author:          comment.AuthorKind,
		AuthorID:        comment.AuthorID,
		CreatedAtUnixMs: comment.CreatedAtUnixMs,
		UpdatedAt:       comment.UpdatedAtUnixMs,
	}
}

func workflowCurrentNodes(nodes []workflow.CurrentNode) []serverapi.WorkflowTaskCurrentNode {
	projected := make([]serverapi.WorkflowTaskCurrentNode, 0, len(nodes))
	for _, currentNode := range nodes {
		projected = append(projected, workflowCurrentNode(currentNode))
	}
	return projected
}

func workflowCurrentNode(currentNode workflow.CurrentNode) serverapi.WorkflowTaskCurrentNode {
	projected := workflowCurrentNodeReference(currentNode.Reference)
	if currentNode.SessionID != nil {
		value := currentNode.SessionID.String()
		projected.SessionID = &value
	}
	return projected
}

func workflowCurrentNodeReference(reference workflow.CurrentNodeReference) serverapi.WorkflowTaskCurrentNode {
	projected := serverapi.WorkflowTaskCurrentNode{NodeID: string(reference.NodeID)}
	if value, present := reference.TransitionBranchKey(); present {
		branch := string(value)
		projected.TransitionBranchKey = &branch
	}
	return projected
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

func taskSummary(task sqlitegen.TaskRecord, status serverapi.WorkflowTaskStatus, done bool) serverapi.WorkflowTaskSummary {
	return serverapi.WorkflowTaskSummary{
		ID:                task.ID,
		ProjectID:         task.ProjectID,
		WorkflowID:        task.WorkflowID,
		ShortID:           task.ShortID,
		Title:             task.Title,
		BodyPreview:       bodyPreview(task.Body),
		SourceWorkspaceID: strings.TrimSpace(task.SourceWorkspaceID.String),
		CreatedAtUnixMs:   task.CreatedAtUnixMs,
		UpdatedAtUnixMs:   task.UpdatedAtUnixMs,
		Done:              done,
		ActiveNodeIDs:     append([]string(nil), status.NodeIDs...),
	}
}

func currentNodesContainTerminal(nodes []workflow.CurrentNode, nodeKinds map[string]workflow.NodeKind) bool {
	for _, currentNode := range nodes {
		if nodeKinds[string(currentNode.Reference.NodeID)] == workflow.NodeKindTerminal {
			return true
		}
	}
	return false
}

func taskActions(
	done bool,
	status serverapi.WorkflowTaskStatus,
	currentNodes []workflow.CurrentNode,
	live []sessionruntime.TaskExecution,
	def serverapi.WorkflowDefinition,
	nodeKinds map[string]workflow.NodeKind,
	canDelete bool,
) serverapi.WorkflowTaskActions {
	hasLiveExecution := len(live) != 0
	hasInterruptibleExecution := false
	for _, execution := range live {
		hasInterruptibleExecution = hasInterruptibleExecution ||
			(!execution.Queued && !execution.HasPendingPrompts())
	}
	actions := serverapi.WorkflowTaskActions{
		CanStart:     !done && !hasLiveExecution && status.Kind == serverapi.WorkflowTaskStatusKindBacklog,
		CanInterrupt: !done && hasInterruptibleExecution,
		CanResume:    !done && !hasLiveExecution && status.Kind == serverapi.WorkflowTaskStatusKindInterrupted,
		CanDelete:    canDelete,
	}
	if !done && !hasLiveExecution {
		actions.ManualMoveTargetNodeIDs = manualMoveTargetNodeIDs(def, currentNodes, nodeKinds)
	}
	return actions
}

func manualMoveTargetNodeIDs(def serverapi.WorkflowDefinition, currentNodes []workflow.CurrentNode, nodeKinds map[string]workflow.NodeKind) []string {
	if len(currentNodes) != 1 {
		return []string{}
	}
	sourceNodeID := string(currentNodes[0].Reference.NodeID)
	if nodeKinds[sourceNodeID] == workflow.NodeKindTerminal {
		return []string{}
	}
	groupIDs := map[string]bool{}
	for _, group := range def.TransitionGroups {
		if group.SourceNodeID == sourceNodeID {
			groupIDs[group.ID] = true
		}
	}
	derivedEdges := workflowDerivedEdgeWiringByID(def.DerivedWiring)
	targets := []string{}
	seen := map[string]bool{}
	for _, node := range def.Nodes {
		if workflow.NodeKind(node.Kind) == workflow.NodeKindTerminal && node.ID != sourceNodeID {
			seen[node.ID] = true
			targets = append(targets, node.ID)
		}
	}
	for _, edge := range def.Edges {
		contextSource := workflow.CanonicalContextSource(workflow.ContextSource{
			Kind:    workflow.ContextSourceKind(edge.ContextSource.Kind),
			NodeKey: workflow.ModelKey(edge.ContextSource.NodeKey),
		})
		if !groupIDs[edge.TransitionGroupID] ||
			edge.RequiresApproval ||
			len(derivedEdges[edge.ID].RequiredProvisionFields) > 0 ||
			contextSource.Kind == workflow.ContextSourceSelectedNode ||
			contextSource.Kind == workflow.ContextSourcePreviousTarget {
			continue
		}
		if !seen[edge.TargetNodeID] {
			seen[edge.TargetNodeID] = true
			targets = append(targets, edge.TargetNodeID)
		}
	}
	sort.Strings(targets)
	return targets
}

func workflowDerivedEdgeWiringByID(derived serverapi.WorkflowDerivedWiring) map[string]serverapi.WorkflowDerivedEdgeWiring {
	byID := make(map[string]serverapi.WorkflowDerivedEdgeWiring, len(derived.Edges))
	for _, edge := range derived.Edges {
		byID[edge.EdgeID] = edge
	}
	return byID
}
