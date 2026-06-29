package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
)

func completionHasCode(err error, code CompletionCode) bool {
	var cve CompletionValidationError
	return errors.As(err, &cve) && cve.HasCode(code)
}

func setCommentCreatedAt(t *testing.T, ctx context.Context, store *Store, commentID string, createdAtUnixMs int64) {
	t.Helper()
	// Intentional direct timestamp fixture: comment pagination needs stable
	// created_at rows without sleeping between comment writes.
	if _, err := store.db.ExecContext(ctx, `UPDATE task_comments SET created_at_unix_ms = ? WHERE id = ?`, createdAtUnixMs, commentID); err != nil {
		t.Fatalf("force comment timestamp: %v", err)
	}
}

func mutateSnapshotTransition(t *testing.T, snapshot *runStartSnapshot, transitionID string, mutate func(*transitionContractSnapshot)) {
	t.Helper()
	for index := range snapshot.TransitionGroups {
		if snapshot.TransitionGroups[index].TransitionID == transitionID {
			mutate(&snapshot.TransitionGroups[index])
			return
		}
	}
	t.Fatalf("snapshot transition %q missing from %+v", transitionID, snapshot.TransitionGroups)
}

func mutateRunStartSnapshot(t *testing.T, ctx context.Context, store *Store, runID workflow.RunID, mutate func(*testing.T, *runStartSnapshot)) {
	t.Helper()
	row, err := store.queries.GetTaskRun(ctx, string(runID))
	if err != nil {
		t.Fatalf("GetTaskRun: %v", err)
	}
	snapshot := runStartSnapshot{}
	if err := workflow.UnmarshalString(row.RunStartSnapshotJson, &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	mutate(t, &snapshot)
	updateRunStartSnapshot(t, ctx, store, runID, snapshot)
}

func nodeSnapshotByID(t *testing.T, snapshot runStartSnapshot, nodeID workflow.NodeID) nodeContractSnapshot {
	t.Helper()
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID {
			return node
		}
	}
	t.Fatalf("snapshot node %q missing from %+v", nodeID, snapshot.Nodes)
	return nodeContractSnapshot{}
}

func updateRunStartSnapshot(t *testing.T, ctx context.Context, store *Store, runID workflow.RunID, snapshot runStartSnapshot) {
	t.Helper()
	snapshotJSON, err := workflow.MarshalString(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	// Intentional corruption fixture: mutate a persisted run-start snapshot to
	// test snapshot drift/freeze behavior that product APIs cannot create.
	if _, err := store.db.ExecContext(ctx, `UPDATE task_runs SET run_start_snapshot_json = ? WHERE id = ?`, snapshotJSON, string(runID)); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}
}

func hasProjectWorkflowUnlinkBlocker(blockers []ProjectWorkflowUnlinkBlocker, code string, count int) bool {
	for _, blocker := range blockers {
		if blocker.Code == code && blocker.Count == count {
			return true
		}
	}
	return false
}

func confirmedWorkflowDeleteRequest(impact WorkflowDeleteImpact, cleanupArtifacts bool) WorkflowDeleteRequest {
	return WorkflowDeleteRequest{
		WorkflowID:           impact.WorkflowID,
		Confirmed:            true,
		ExpectedVersion:      impact.Version,
		ExpectedProjectCount: impact.ProjectCount,
		ExpectedLinkCount:    impact.LinkCount,
		ExpectedTaskCount:    impact.TaskCount,
		CleanupArtifacts:     cleanupArtifacts,
	}
}

func hasWorkflowDeleteBlocker(blockers []WorkflowDeleteBlocker, code string, count int64) bool {
	for _, blocker := range blockers {
		if blocker.Code == code && blocker.Count == count {
			return true
		}
	}
	return false
}

func workflowGraphSaveRequestFromDefinition(workflowID workflow.WorkflowID, revision int64, confirmed bool, def workflow.Definition) WorkflowGraphSaveRequest {
	req := WorkflowGraphSaveRequest{WorkflowID: workflowID, ExpectedVersion: revision, Confirmed: confirmed}
	for _, group := range def.NodeGroups {
		req.NodeGroups = append(req.NodeGroups, NodeGroupRecord{ID: group.ID, WorkflowID: workflowID, Key: group.Key, DisplayName: group.DisplayName})
	}
	for _, node := range def.Nodes {
		req.Nodes = append(req.Nodes, NodeRecord{ID: node.ID, WorkflowID: workflowID, Key: node.Key, Kind: node.Kind, DisplayName: node.DisplayName, GroupID: node.GroupID, SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, InputFields: node.InputFields, JoinInputProviders: node.JoinInputProviders, OutputFields: node.OutputFields})
	}
	for _, group := range def.TransitionGroups {
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{ID: group.ID, WorkflowID: workflowID, SourceNodeID: group.SourceNodeID, TransitionID: group.TransitionID, DisplayName: group.DisplayName})
	}
	for _, edge := range def.Edges {
		req.Edges = append(req.Edges, EdgeRecord{ID: edge.ID, WorkflowID: workflowID, TransitionGroupID: edge.TransitionGroupID, Key: edge.Key, TargetNodeID: edge.TargetNodeID, ContextMode: edge.ContextMode, ContextSource: edge.ContextSource, RequiresApproval: edge.RequiresApproval, PromptTemplate: edge.PromptTemplate, Parameters: edge.Parameters, InputBindings: edge.InputBindings, OutputRequirements: edge.OutputRequirements})
	}
	return req
}

func renameWorkflowGraphSaveNode(nodes []NodeRecord, nodeID workflow.NodeID, displayName string) []NodeRecord {
	renamed := make([]NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == nodeID {
			node.DisplayName = displayName
		}
		renamed = append(renamed, node)
	}
	return renamed
}

func setWorkflowGraphSaveNodeGroup(nodes []NodeRecord, nodeID workflow.NodeID, groupID string) []NodeRecord {
	changed := make([]NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == nodeID {
			node.GroupID = groupID
		}
		changed = append(changed, node)
	}
	return changed
}

func confirmWorkflowGraphSaveRequest(req WorkflowGraphSaveRequest, impact WorkflowGraphSaveImpact) WorkflowGraphSaveRequest {
	req.Confirmed = true
	req.ExpectedRemovedNodeCount = impact.RemovedNodeCount
	req.ExpectedRemovedTransitionGroupCount = impact.RemovedTransitionGroupCount
	req.ExpectedRemovedEdgeCount = impact.RemovedEdgeCount
	req.ExpectedNodeTaskReferenceCount = impact.NodeTaskReferenceCount
	req.ExpectedEdgeTaskReferenceCount = impact.EdgeTaskReferenceCount
	return req
}

func changeWorkflowGraphSaveNodeKind(nodes []NodeRecord, nodeID workflow.NodeID, kind workflow.NodeKind) []NodeRecord {
	changed := make([]NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == nodeID {
			node.Kind = kind
		}
		changed = append(changed, node)
	}
	return changed
}

func removeWorkflowGraphSaveNode(nodes []NodeRecord, nodeID workflow.NodeID) []NodeRecord {
	filtered := make([]NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if node.ID != nodeID {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func removeWorkflowGraphSaveTransitionGroupsTouchingNode(def workflow.Definition, groups []TransitionGroupRecord, nodeID workflow.NodeID) []TransitionGroupRecord {
	touching := map[workflow.TransitionGroupID]bool{}
	for _, group := range def.TransitionGroups {
		if group.SourceNodeID == nodeID {
			touching[group.ID] = true
		}
	}
	for _, edge := range def.Edges {
		if edge.TargetNodeID == nodeID {
			touching[edge.TransitionGroupID] = true
		}
	}
	filtered := make([]TransitionGroupRecord, 0, len(groups))
	for _, group := range groups {
		if !touching[group.ID] {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func removeWorkflowGraphSaveTransitionGroupByID(groups []TransitionGroupRecord, groupID workflow.TransitionGroupID) []TransitionGroupRecord {
	filtered := make([]TransitionGroupRecord, 0, len(groups))
	for _, group := range groups {
		if group.ID != groupID {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func removeWorkflowGraphSaveEdge(edges []EdgeRecord, edgeID workflow.EdgeID) []EdgeRecord {
	filtered := make([]EdgeRecord, 0, len(edges))
	for _, edge := range edges {
		if edge.ID != edgeID {
			filtered = append(filtered, edge)
		}
	}
	return filtered
}
