package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func completionHasCode(err error, code CompletionCode) bool {
	var cve CompletionValidationError
	return errors.As(err, &cve) && cve.HasCode(code)
}

func projectNextTaskSequence(t *testing.T, ctx context.Context, store *Store, projectID string) int64 {
	t.Helper()
	var sequence int64
	if err := store.db.QueryRowContext(ctx, `SELECT next_task_seq FROM projects WHERE id = ?`, projectID).Scan(&sequence); err != nil {
		t.Fatalf("query project next task sequence: %v", err)
	}
	return sequence
}

func assertTaskCreationUnchanged(t *testing.T, ctx context.Context, store *Store, projectID string, wantSequence int64) {
	t.Helper()
	if got := projectNextTaskSequence(t, ctx, store, projectID); got != wantSequence {
		t.Fatalf("project next task sequence = %d, want unchanged %d", got, wantSequence)
	}
	var taskCount int64
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_records WHERE project_id = ?`, projectID).Scan(&taskCount); err != nil {
		t.Fatalf("count project tasks: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("project task count = %d, want 0", taskCount)
	}
	var currentNodeCount int64
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM task_current_nodes current_node
JOIN task_records task ON task.id = current_node.task_id
WHERE task.project_id = ?`, projectID).Scan(&currentNodeCount); err != nil {
		t.Fatalf("count project current nodes: %v", err)
	}
	if currentNodeCount != 0 {
		t.Fatalf("project current node count = %d, want 0", currentNodeCount)
	}
}

func setCommentCreatedAt(t *testing.T, ctx context.Context, store *Store, commentID string, createdAtUnixMs int64) {
	t.Helper()
	// Intentional direct timestamp fixture: comment pagination needs stable
	// created_at rows without sleeping between comment writes.
	if _, err := store.db.ExecContext(ctx, `UPDATE task_comments SET created_at_unix_ms = ? WHERE id = ?`, createdAtUnixMs, commentID); err != nil {
		t.Fatalf("force comment timestamp: %v", err)
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

type workflowDeletionFixture struct {
	ctx       context.Context
	store     *Store
	projectID string
}

func newWorkflowDeletionFixture(t *testing.T) workflowDeletionFixture {
	t.Helper()
	ctx, store, binding := newTestStoreContext(t)
	return workflowDeletionFixture{ctx: ctx, store: store, projectID: binding.ProjectID}
}

func (f workflowDeletionFixture) linkedWorkflow(t *testing.T, isDefault bool) (runtimeids.WorkflowID, ProjectWorkflowLinkRecord) {
	t.Helper()
	workflowID := createValidWorkflow(t, f.ctx, f.store)
	return workflowID, linkWorkflow(t, f.ctx, f.store, f.projectID, workflowID, isDefault)
}

func (f workflowDeletionFixture) unlink(t *testing.T, linkID, replacementDefaultLinkID string) ProjectWorkflowUnlinkResult {
	t.Helper()
	result, err := f.store.UnlinkProjectWorkflow(f.ctx, linkID, replacementDefaultLinkID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkflow: %v", err)
	}
	return result
}

func (f workflowDeletionFixture) preview(t *testing.T, workflowID runtimeids.WorkflowID) WorkflowDeleteImpact {
	t.Helper()
	impact, err := f.store.PreviewWorkflowDelete(f.ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	return impact
}

func (f workflowDeletionFixture) delete(t *testing.T, req WorkflowDeleteRequest) WorkflowDeleteResult {
	t.Helper()
	result, err := f.store.DeleteWorkflow(f.ctx, req)
	if err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}
	return result
}

func (f workflowDeletionFixture) confirmDelete(t *testing.T, impact WorkflowDeleteImpact, cleanupArtifacts bool) WorkflowDeleteResult {
	t.Helper()
	return f.delete(t, confirmedWorkflowDeleteRequest(impact, cleanupArtifacts))
}

func (f workflowDeletionFixture) links(t *testing.T) []ProjectWorkflowLinkRecord {
	t.Helper()
	links, err := f.store.ListProjectWorkflowLinks(f.ctx, f.projectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks: %v", err)
	}
	return links
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

func workflowGraphSaveRequestFromDefinition(workflowID runtimeids.WorkflowID, revision int64, confirmed bool, def workflow.Definition) WorkflowGraphSaveRequest {
	req := WorkflowGraphSaveRequest{WorkflowID: workflowID, ExpectedVersion: revision, Confirmed: confirmed}
	groupKeyByID := make(map[string]string, len(def.NodeGroups))
	for index, group := range def.NodeGroups {
		req.NodeGroups = append(req.NodeGroups, NodeGroupRecord{ID: group.ID, WorkflowID: workflowID, Key: group.Key, DisplayName: group.DisplayName, SortOrder: int64(index * 100)})
		groupKeyByID[group.ID] = string(group.Key)
	}
	for _, node := range def.Nodes {
		groupID := workflow.NodeGroupID(node)
		req.Nodes = append(req.Nodes, NodeRecord{ID: workflow.NodeIDOf(node), WorkflowID: workflowID, Key: workflow.NodeKey(node), Kind: node.Kind(), DisplayName: workflow.NodeDisplayName(node), GroupID: groupID, GroupKey: groupKeyByID[groupID], SubagentRole: workflow.NodeSubagentRole(node), CompletionMode: workflow.NodeCompletionMode(node), ScriptPath: workflow.NodeScriptPath(node).String(), JoinInputProviders: workflow.NodeJoinInputProviders(node)})
	}
	for _, group := range def.TransitionGroups {
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{ID: group.ID, WorkflowID: workflowID, SourceNodeID: group.SourceNodeID, TransitionID: group.TransitionID, DisplayName: group.DisplayName, Description: group.Description})
	}
	for _, edge := range def.Edges {
		req.Edges = append(req.Edges, EdgeRecord{ID: edge.ID, WorkflowID: workflowID, TransitionGroupID: edge.TransitionGroupID, Key: edge.Key, TargetNodeID: edge.TargetNodeID, ContextMode: edge.ContextMode, ContextSource: edge.ContextSource, RequiresApproval: edge.RequiresApproval, PromptTemplate: edge.PromptTemplate, Parameters: edge.Parameters, InputBindings: edge.InputBindings, OutputRequirements: edge.OutputRequirements})
	}
	return req
}

func saveWorkflowGraphFixture(t *testing.T, ctx context.Context, store *Store, workflowID runtimeids.WorkflowID, edit func(workflow.Definition, *WorkflowGraphSaveRequest)) WorkflowGraphSaveResult {
	t.Helper()
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition workflow fixture: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	edit(def, &req)
	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph workflow fixture: %v", err)
	}
	if !result.Saved {
		t.Fatalf("SaveWorkflowGraph workflow fixture rejected: blockers=%+v validation=%+v", result.Blockers, result.ValidationErrors)
	}
	return result
}

func workflowGraphSaveNodeRecord(t *testing.T, nodes []NodeRecord, nodeID workflow.NodeID) *NodeRecord {
	t.Helper()
	for index := range nodes {
		if nodes[index].ID == nodeID {
			return &nodes[index]
		}
	}
	t.Fatalf("node record %q missing from %+v", nodeID, nodes)
	return nil
}

func workflowGraphSaveEdgeRecord(t *testing.T, edges []EdgeRecord, edgeID workflow.EdgeID) *EdgeRecord {
	t.Helper()
	for index := range edges {
		if edges[index].ID == edgeID {
			return &edges[index]
		}
	}
	t.Fatalf("edge record %q missing from %+v", edgeID, edges)
	return nil
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
			if groupID == "" {
				node.GroupKey = ""
			}
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

func mutateWorkflowGraphSaveEdge(edges []EdgeRecord, edgeID workflow.EdgeID, mutate func(*EdgeRecord)) []EdgeRecord {
	changed := make([]EdgeRecord, 0, len(edges))
	for _, edge := range edges {
		if edge.ID == edgeID {
			mutate(&edge)
		}
		changed = append(changed, edge)
	}
	return changed
}

func mutateWorkflowGraphSaveTransitionGroup(groups []TransitionGroupRecord, groupID workflow.TransitionGroupID, mutate func(*TransitionGroupRecord)) []TransitionGroupRecord {
	changed := make([]TransitionGroupRecord, 0, len(groups))
	for _, group := range groups {
		if group.ID == groupID {
			mutate(&group)
		}
		changed = append(changed, group)
	}
	return changed
}

func nodeByKey(t *testing.T, def workflow.Definition, key string) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if string(workflow.NodeKey(node)) == key {
			return node
		}
	}
	t.Fatalf("missing node key %q", key)
	return nil
}

func edgeByKey(t *testing.T, def workflow.Definition, key string) workflow.Edge {
	t.Helper()
	for _, edge := range def.Edges {
		if string(edge.Key) == key {
			return edge
		}
	}
	t.Fatalf("missing edge key %q", key)
	return workflow.Edge{}
}

func nodeByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("missing node kind %q", kind)
	return nil
}
