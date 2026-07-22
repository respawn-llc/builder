package workflowview

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/serverapi"
)

type TaskProjector struct{}

type TaskStatusInput struct {
	TaskID             string
	Kind               string
	NodeIDsJSON        string
	RunIDsJSON         string
	AttentionTypesJSON string
	Done               bool
}

type TaskFactsInput struct {
	Task       sqlitegen.TaskRecord
	Status     workflowTaskStatusFact
	Placements []sqlitegen.TaskNodePlacementRecord
	RunActions taskRunActionFacts
	Definition definitionSnapshot
}

type taskRunActionFacts struct {
	HasRunning         bool
	HasInterrupted     bool
	HasWaitingQuestion bool
}

type TaskFacts struct {
	Summary             serverapi.WorkflowTaskSummary
	Status              serverapi.WorkflowTaskStatus
	Actions             serverapi.WorkflowTaskActions
	Done                bool
	EffectivePlacements []sqlitegen.TaskNodePlacementRecord
}

type PlacementProjectionInput struct {
	Placement sqlitegen.TaskNodePlacementRecord
	Nodes     map[string]serverapi.WorkflowNode
}

type RunProjectionInput struct {
	Run          sqlitegen.TaskRunRecord
	Nodes        map[string]serverapi.WorkflowNode
	SessionNames map[string]string
}

type TransitionProjectionInput struct {
	Transition sqlitegen.TaskTransitionRecord
	Edges      []sqlitegen.TaskTransitionEdgeRecord
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
	runIDs, err := workflowTaskStatusIDs(input.TaskID, "run_ids_json", input.RunIDsJSON)
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
			RunIDs:         runIDs,
			AttentionTypes: attentionTypes,
		},
		Done: input.Done,
	}, nil
}

func (*TaskProjector) ProjectTaskFacts(input TaskFactsInput) TaskFacts {
	done := input.Status.Done || hasActiveTerminalPlacement(input.Placements, input.Definition.nodeKinds)
	return TaskFacts{
		Summary:             taskSummary(input.Task, input.Status.Status, done),
		Status:              input.Status.Status,
		Actions:             taskActions(input.Task, done, input.Placements, input.RunActions, input.Definition.api, input.Definition.nodeKinds),
		Done:                done,
		EffectivePlacements: effectiveBoardPlacementsForTask(input.Task, input.Placements, input.Definition.api, input.Definition.nodeKinds),
	}
}

func (*TaskProjector) ProjectPlacement(input PlacementProjectionInput) serverapi.WorkflowPlacement {
	placement := input.Placement
	nodeID, _ := taskNodePlacementNodeID(placement)
	dto := serverapi.WorkflowPlacement{
		ID:                        placement.ID,
		TaskID:                    placement.TaskID,
		NodeID:                    nodeID,
		State:                     placement.State,
		ParallelBatchTransitionID: strings.TrimSpace(placement.ParallelBatchTransitionID.String),
		ParallelBranchEdgeID:      strings.TrimSpace(placement.ParallelBranchEdgeID.String),
	}
	if node, ok := input.Nodes[nodeID]; ok {
		dto.NodeKey = node.Key
		dto.NodeDisplayName = node.DisplayName
		dto.NodeKind = node.Kind
	}
	return dto
}

func (*TaskProjector) ProjectRun(input RunProjectionInput) serverapi.WorkflowRun {
	run := input.Run
	nodeID := nullableWorkflowViewNodeID(run.NodeID)
	dto := serverapi.WorkflowRun{
		ID:                  run.ID,
		TaskID:              run.TaskID,
		PlacementID:         run.PlacementID,
		NodeID:              nodeID,
		SessionID:           run.SessionID.String,
		Generation:          run.RunGeneration,
		StartedAtUnixMs:     metadata.OptionalInt64(run.StartedAtUnixMs),
		CompletedAtUnixMs:   metadata.OptionalInt64(run.CompletedAtUnixMs),
		InterruptedAtUnixMs: metadata.OptionalInt64(run.InterruptedAtUnixMs),
		InterruptionReason:  metadata.OptionalString(run.InterruptionReason),
		InterruptionDetail:  run.InterruptionDetailJson,
		WaitingAskID:        metadata.OptionalString(run.WaitingAskID),
		Status:              runStatus(run),
	}
	if node, ok := input.Nodes[nodeID]; ok {
		dto.NodeKind = node.Kind
		if node.ScriptPath != nil {
			dto.ScriptPath = strings.TrimSpace(*node.ScriptPath)
		}
		dto.Role = node.SubagentRole
	}
	if name, ok := input.SessionNames[strings.TrimSpace(run.SessionID.String)]; ok {
		dto.SessionName = name
	}
	return dto
}

func (*TaskProjector) ProjectTransition(input TransitionProjectionInput) (serverapi.WorkflowTaskTransition, error) {
	transition := input.Transition
	outputs := map[string]string{}
	if err := workflow.UnmarshalString(transition.OutputValuesJson, &outputs); err != nil {
		return serverapi.WorkflowTaskTransition{}, err
	}
	dto := serverapi.WorkflowTaskTransition{
		ID:                    transition.ID,
		TaskID:                transition.TaskID,
		SourceRunID:           strings.TrimSpace(transition.SourceRunID.String),
		SourcePlacementID:     strings.TrimSpace(transition.SourcePlacementID.String),
		SourceNodeID:          nullableWorkflowViewNodeID(transition.SourceNodeID),
		SourceNodeKey:         transition.SourceNodeKey,
		SourceNodeDisplayName: transition.SourceNodeDisplayName,
		TransitionGroupID:     strings.TrimSpace(transition.TransitionGroupID.String),
		TransitionID:          transition.TransitionID,
		TransitionDisplayName: transition.TransitionDisplayName,
		WorkflowRevisionSeen:  transition.WorkflowRevisionSeen,
		Actor:                 transition.Actor,
		State:                 transition.State,
		Commentary:            transition.Commentary,
		OutputValues:          outputs,
		CreatedAt:             transition.CreatedAtUnixMs,
		AppliedAtUnixMs:       metadata.OptionalInt64(transition.AppliedAtUnixMs),
	}
	for _, edge := range input.Edges {
		inputs := []serverapi.WorkflowInputBinding{}
		if err := workflow.UnmarshalString(edge.InputBindingsJson, &inputs); err != nil {
			return serverapi.WorkflowTaskTransition{}, err
		}
		requirements := []serverapi.WorkflowOutputRequirement{}
		if err := workflow.UnmarshalString(edge.OutputRequirementsJson, &requirements); err != nil {
			return serverapi.WorkflowTaskTransition{}, err
		}
		dto.Edges = append(dto.Edges, serverapi.WorkflowTransitionEdge{
			ID:                    edge.ID,
			TaskTransitionID:      edge.TaskTransitionID,
			WorkflowEdgeID:        strings.TrimSpace(edge.WorkflowEdgeID.String),
			EdgeKey:               edge.EdgeKey,
			TargetNodeID:          strings.TrimSpace(edge.TargetNodeID.String),
			TargetNodeKey:         edge.TargetNodeKey,
			TargetNodeDisplayName: edge.TargetNodeDisplayName,
			TargetNodeKind:        edge.TargetNodeKind,
			TargetPlacementID:     strings.TrimSpace(edge.TargetPlacementID.String),
			State:                 edge.State,
			ContextMode:           edge.ContextMode,
			RequiresApproval:      edge.RequiresApproval != 0,
			InputBindings:         inputs,
			OutputRequirements:    requirements,
			WorkflowRevisionSeen:  edge.WorkflowRevisionSeen,
		})
	}
	return dto, nil
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
		CanceledAt:        metadata.OptionalInt64(task.CanceledAtUnixMs),
		CancelReason:      metadata.OptionalString(task.CancellationReason),
		CreatedAtUnixMs:   task.CreatedAtUnixMs,
		UpdatedAtUnixMs:   task.UpdatedAtUnixMs,
		Done:              done,
		ActiveNodeIDs:     append([]string(nil), status.NodeIDs...),
	}
}

func workflowNodeByID(def serverapi.WorkflowDefinition) map[string]serverapi.WorkflowNode {
	out := make(map[string]serverapi.WorkflowNode, len(def.Nodes))
	for _, node := range def.Nodes {
		out[node.ID] = node
	}
	return out
}

func runStatus(run sqlitegen.TaskRunRecord) string {
	switch {
	case run.CompletedAtUnixMs.Valid:
		return "completed"
	case run.InterruptedAtUnixMs.Valid:
		return "interrupted"
	case run.WaitingAskID.Valid:
		return "waiting_question"
	case run.StartedAtUnixMs.Valid:
		return "running"
	default:
		return "pending"
	}
}

func hasActiveTerminalPlacement(placements []sqlitegen.TaskNodePlacementRecord, nodeKinds map[string]workflow.NodeKind) bool {
	for _, placement := range placements {
		nodeID, ok := taskNodePlacementNodeID(placement)
		if ok && placement.State == "active" && nodeKinds[nodeID] == workflow.NodeKindTerminal {
			return true
		}
	}
	return false
}

func taskActions(task sqlitegen.TaskRecord, done bool, placements []sqlitegen.TaskNodePlacementRecord, runActions taskRunActionFacts, def serverapi.WorkflowDefinition, nodeKinds map[string]workflow.NodeKind) serverapi.WorkflowTaskActions {
	actions := serverapi.WorkflowTaskActions{CanCancel: !task.CanceledAtUnixMs.Valid && !done}
	waitingApproval := false
	backlog := false
	for _, placement := range placements {
		if placement.State == "waiting_approval" {
			waitingApproval = true
		}
		nodeID, ok := taskNodePlacementNodeID(placement)
		if ok && placement.State == "active" && nodeKinds[nodeID] == workflow.NodeKindStart {
			backlog = true
		}
	}
	actions.CanStart = !task.CanceledAtUnixMs.Valid && backlog && !waitingApproval && !runActions.HasRunning && !runActions.HasWaitingQuestion
	taskActive := !task.CanceledAtUnixMs.Valid
	if taskActive && !runActions.HasRunning {
		actions.ManualMoveTargetNodeIDs = manualMoveTargetNodeIDs(def, placements, nodeKinds)
	}
	actions.CanInterrupt = taskActive && runActions.HasRunning
	actions.CanResume = taskActive && runActions.HasInterrupted
	return actions
}

func manualMoveTargetNodeIDs(def serverapi.WorkflowDefinition, placements []sqlitegen.TaskNodePlacementRecord, nodeKinds map[string]workflow.NodeKind) []string {
	sourceNodeID := ""
	for _, placement := range placements {
		nodeID, ok := taskNodePlacementNodeID(placement)
		if !ok {
			continue
		}
		nodeKind := nodeKinds[nodeID]
		if placement.State != "active" && placement.State != "waiting_approval" {
			continue
		}
		if sourceNodeID != "" {
			return []string{}
		}
		if nodeKind == workflow.NodeKindTerminal && placement.State != "active" {
			return []string{}
		}
		sourceNodeID = nodeID
	}
	if sourceNodeID == "" {
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
		contextSource := workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSource.Kind), NodeKey: workflow.ModelKey(edge.ContextSource.NodeKey)})
		if !groupIDs[edge.TransitionGroupID] || edge.RequiresApproval || len(derivedEdges[edge.ID].RequiredProvisionFields) > 0 || contextSource.Kind == workflow.ContextSourceSelectedNode || contextSource.Kind == workflow.ContextSourcePreviousTarget {
			continue
		}
		if !seen[edge.TargetNodeID] {
			seen[edge.TargetNodeID] = true
			targets = append(targets, edge.TargetNodeID)
		}
	}
	return targets
}

func workflowDerivedEdgeWiringByID(derived serverapi.WorkflowDerivedWiring) map[string]serverapi.WorkflowDerivedEdgeWiring {
	byID := make(map[string]serverapi.WorkflowDerivedEdgeWiring, len(derived.Edges))
	for _, edge := range derived.Edges {
		byID[edge.EdgeID] = edge
	}
	return byID
}

func effectiveBoardPlacements(placements []sqlitegen.TaskNodePlacementRecord, nodeKinds map[string]workflow.NodeKind) []sqlitegen.TaskNodePlacementRecord {
	effective := make([]sqlitegen.TaskNodePlacementRecord, 0, len(placements))
	for _, placement := range placements {
		nodeID, ok := taskNodePlacementNodeID(placement)
		if !ok {
			continue
		}
		if nodeKinds[nodeID] == workflow.NodeKindTerminal {
			if placement.State == "active" {
				effective = append(effective, placement)
			}
			continue
		}
		if placement.State == "active" || placement.State == "waiting_approval" {
			effective = append(effective, placement)
		}
	}
	return effective
}

func effectiveBoardPlacementsForTask(task sqlitegen.TaskRecord, placements []sqlitegen.TaskNodePlacementRecord, def serverapi.WorkflowDefinition, nodeKinds map[string]workflow.NodeKind) []sqlitegen.TaskNodePlacementRecord {
	active := effectiveBoardPlacements(placements, nodeKinds)
	if !task.CanceledAtUnixMs.Valid {
		return active
	}
	terminalNodeID := canceledBoardTerminalNodeID(def)
	if terminalNodeID == nil {
		return active
	}
	terminalPlacements := make([]sqlitegen.TaskNodePlacementRecord, 0, len(active))
	for _, placement := range active {
		nodeID, ok := taskNodePlacementNodeID(placement)
		if ok && nodeKinds[nodeID] == workflow.NodeKindTerminal {
			terminalPlacements = append(terminalPlacements, placement)
		}
	}
	if len(terminalPlacements) > 0 {
		return terminalPlacements
	}
	// User-authorized BUI-84 exception; KENT-306 tracks replacing this sentinel.
	return []sqlitegen.TaskNodePlacementRecord{{
		ID:              "",
		TaskID:          task.ID,
		NodeID:          sql.NullString{String: *terminalNodeID, Valid: true},
		State:           "active",
		CreatedAtUnixMs: task.UpdatedAtUnixMs,
		UpdatedAtUnixMs: task.UpdatedAtUnixMs,
	}}
}

func canceledBoardTerminalNodeID(def serverapi.WorkflowDefinition) *string {
	var fallback *string
	for _, node := range def.Nodes {
		if workflow.NodeKind(node.Kind) != workflow.NodeKindTerminal {
			continue
		}
		if fallback == nil {
			value := node.ID
			fallback = &value
		}
		if node.Key == "done" {
			value := node.ID
			return &value
		}
	}
	return fallback
}

func taskNodePlacementNodeID(placement sqlitegen.TaskNodePlacementRecord) (string, bool) {
	nodeID := nullableWorkflowViewNodeID(placement.NodeID)
	return nodeID, nodeID != ""
}

func nullableWorkflowViewNodeID(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}
