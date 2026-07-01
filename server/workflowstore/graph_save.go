package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/metadata/sqlitelifecyclegen"
	"core/server/workflow"
)

type WorkflowGraphSaveRequest struct {
	WorkflowID                          workflow.WorkflowID
	ExpectedVersion                     int64
	Metadata                            *WorkflowGraphSaveMetadata
	Confirmed                           bool
	ExpectedRemovedNodeCount            int64
	ExpectedRemovedTransitionGroupCount int64
	ExpectedRemovedEdgeCount            int64
	ExpectedNodeTaskReferenceCount      int64
	ExpectedEdgeTaskReferenceCount      int64
	NodeGroups                          []NodeGroupRecord
	Nodes                               []NodeRecord
	TransitionGroups                    []TransitionGroupRecord
	Edges                               []EdgeRecord
}

type WorkflowGraphSaveMetadata struct {
	Name        string
	Description string
}

type WorkflowGraphSaveImpact struct {
	RemovedNodeCount              int64
	RemovedTransitionGroupCount   int64
	RemovedEdgeCount              int64
	NodeTaskReferenceCount        int64
	CurrentNodeTaskReferenceCount int64
	EdgeTaskReferenceCount        int64
}

type WorkflowGraphSaveBlocker struct {
	Code    string
	Message string
	Count   int64
}

type WorkflowGraphSaveResult struct {
	Saved                bool
	Changed              bool
	CanSave              bool
	ConfirmationRequired bool
	Version              int64
	Impact               WorkflowGraphSaveImpact
	EditPolicyImpact     WorkflowGraphEditPolicyImpact
	Blockers             []WorkflowGraphSaveBlocker
	ValidationErrors     []workflow.ValidationError
}

type WorkflowGraphSavePlan struct {
	WorkflowID       workflow.WorkflowID
	Version          int64
	Prepared         preparedWorkflowGraphSave
	Metadata         *WorkflowGraphSaveMetadata
	GraphChanged     bool
	MetadataChanged  bool
	Removed          removedWorkflowGraphRows
	Impact           WorkflowGraphSaveImpact
	EditPolicy       WorkflowGraphEditPolicyResult
	Blockers         []WorkflowGraphSaveBlocker
	ValidationErrors []workflow.ValidationError
}

func (s *Store) PreviewWorkflowGraphSave(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSaveResult, error) {
	plan, err := s.planWorkflowGraphSave(ctx, s.queries, req)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	return plan.workflowGraphSaveResult(false), nil
}

func (s *Store) planWorkflowGraphSave(ctx context.Context, q *sqlitegen.Queries, req WorkflowGraphSaveRequest) (WorkflowGraphSavePlan, error) {
	workflowID := workflow.WorkflowID(strings.TrimSpace(string(req.WorkflowID)))
	if workflowID == "" {
		return WorkflowGraphSavePlan{}, errors.New("workflow id is required")
	}
	if req.ExpectedVersion < 0 {
		return WorkflowGraphSavePlan{}, errors.New("expected version must be non-negative")
	}
	current, err := q.GetWorkflow(ctx, string(workflowID))
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	metadata, metadataChanged, err := prepareWorkflowGraphSaveMetadata(current.Name, current.Description, req.Metadata)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	displayName := current.Name
	if metadata != nil {
		displayName = metadata.Name
	}
	prepared, def, err := prepareWorkflowGraphSave(workflowID, displayName, req)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	graphChanged := !workflowGraphSavePreparedEqual(currentGraph, prepared)
	plan := WorkflowGraphSavePlan{
		WorkflowID:      workflowID,
		Version:         current.Version,
		Prepared:        prepared,
		Metadata:        metadata,
		GraphChanged:    graphChanged,
		MetadataChanged: metadataChanged,
	}
	if !graphChanged && !metadataChanged {
		return plan, nil
	}
	if current.Version != req.ExpectedVersion {
		plan.Blockers = []WorkflowGraphSaveBlocker{{Code: "version_changed", Message: "Workflow changed. Refresh before saving.", Count: current.Version}}
		return plan, nil
	}
	if !graphChanged {
		return plan, nil
	}
	validation := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: s.roleResolver})
	validationErrors := validation.BlockingErrors()
	plan.ValidationErrors = validationErrors
	impact, removed, err := workflowGraphSaveImpact(ctx, q, workflowID, prepared)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	editPolicy, err := workflowGraphEditPolicy(ctx, q, workflowID, prepared)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	blockers := workflowGraphSaveBlockers(req, impact)
	blockers = append(blockers, workflowGraphSaveBlockersFromEditPolicy(editPolicy.Blockers)...)
	if len(validationErrors) > 0 {
		blockers = append(blockers, WorkflowGraphSaveBlocker{Code: "validation_failed", Message: "Workflow graph has blocking validation errors.", Count: int64(len(validationErrors))})
	}
	plan.Removed = removed
	plan.Impact = impact
	plan.EditPolicy = editPolicy
	plan.Blockers = blockers
	return plan, nil
}

func (s *Store) SaveWorkflowGraph(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSaveResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sqlitelifecyclegen.New(tx).DeferForeignKeys(ctx); err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	q := s.queries.WithTx(tx)
	plan, err := s.planWorkflowGraphSave(ctx, q, req)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	if len(plan.Blockers) > 0 {
		return plan.workflowGraphSaveResult(false), nil
	}
	version := plan.Version
	if plan.GraphChanged {
		if err := applyWorkflowGraphSave(ctx, q, plan.WorkflowID, plan.Prepared, plan.Removed); err != nil {
			return WorkflowGraphSaveResult{}, err
		}
		revision, err := s.incrementWorkflowVersion(ctx, q, plan.WorkflowID)
		if err != nil {
			return WorkflowGraphSaveResult{}, err
		}
		version = revision
	}
	if plan.MetadataChanged && plan.Metadata != nil {
		if plan.GraphChanged {
			updated, err := q.UpdateWorkflowInfoWithoutVersion(ctx, sqlitegen.UpdateWorkflowInfoWithoutVersionParams{
				ID:              string(plan.WorkflowID),
				Name:            plan.Metadata.Name,
				Description:     plan.Metadata.Description,
				UpdatedAtUnixMs: s.now().UnixMilli(),
			})
			if err != nil {
				return WorkflowGraphSaveResult{}, fmt.Errorf("update workflow metadata: %w", err)
			}
			if updated != 1 {
				return WorkflowGraphSaveResult{}, sql.ErrNoRows
			}
		} else {
			updated, err := q.UpdateWorkflowInfo(ctx, sqlitegen.UpdateWorkflowInfoParams{ID: string(plan.WorkflowID), Name: plan.Metadata.Name, Description: plan.Metadata.Description, UpdatedAtUnixMs: s.now().UnixMilli()})
			if err != nil {
				return WorkflowGraphSaveResult{}, fmt.Errorf("update workflow metadata: %w", err)
			}
			if updated != 1 {
				return WorkflowGraphSaveResult{}, sql.ErrNoRows
			}
			version++
		}
	}
	if err := tx.Commit(); err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	result := plan.workflowGraphSaveResult(true)
	result.Version = version
	return result, nil
}

func (p WorkflowGraphSavePlan) workflowGraphSaveResult(saved bool) WorkflowGraphSaveResult {
	return WorkflowGraphSaveResult{
		Saved:                saved,
		Changed:              p.GraphChanged || p.MetadataChanged,
		CanSave:              len(p.Blockers) == 0,
		ConfirmationRequired: workflowGraphSaveHasBlocker(p.Blockers, "confirmation_required"),
		Version:              p.Version,
		Impact:               p.Impact,
		EditPolicyImpact:     p.EditPolicy.Impact,
		Blockers:             p.Blockers,
		ValidationErrors:     p.ValidationErrors,
	}
}

type preparedWorkflowGraphSave struct {
	nodeGroups       []NodeGroupRecord
	nodes            []NodeRecord
	transitionGroups []TransitionGroupRecord
	edges            []EdgeRecord
}

type removedWorkflowGraphRows struct {
	nodeGroups       []string
	nodes            []workflow.NodeID
	transitionGroups []workflow.TransitionGroupID
	edges            []workflow.EdgeID
}

func prepareWorkflowGraphSaveMetadata(currentName string, currentDescription string, metadata *WorkflowGraphSaveMetadata) (*WorkflowGraphSaveMetadata, bool, error) {
	if metadata == nil {
		return nil, false, nil
	}
	prepared := WorkflowGraphSaveMetadata{Name: strings.TrimSpace(metadata.Name), Description: strings.TrimSpace(metadata.Description)}
	if prepared.Name == "" {
		return nil, false, ErrWorkflowNameRequired
	}
	changed := prepared.Name != currentName || prepared.Description != currentDescription
	return &prepared, changed, nil
}

func prepareWorkflowGraphSave(workflowID workflow.WorkflowID, displayName string, req WorkflowGraphSaveRequest) (preparedWorkflowGraphSave, workflow.Definition, error) {
	prepared := preparedWorkflowGraphSave{
		nodeGroups:       append([]NodeGroupRecord(nil), req.NodeGroups...),
		nodes:            append([]NodeRecord(nil), req.Nodes...),
		transitionGroups: append([]TransitionGroupRecord(nil), req.TransitionGroups...),
		edges:            append([]EdgeRecord(nil), req.Edges...),
	}
	groupsByKey := map[workflow.ModelKey]string{}
	groupsByID := map[string]bool{}
	for i, group := range prepared.nodeGroups {
		if strings.TrimSpace(string(group.WorkflowID)) == "" {
			group.WorkflowID = workflowID
		}
		group.ID = strings.TrimSpace(group.ID)
		if group.ID == "" {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, errors.New("workflow node group id is required")
		}
		group.Key = workflow.ModelKey(strings.TrimSpace(string(group.Key)))
		if group.Key == "" {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, errors.New("workflow node group key is required")
		}
		if group.WorkflowID != workflowID {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, fmt.Errorf("workflow node group %q belongs to workflow %q: %w", group.ID, group.WorkflowID, ErrBelongsToOtherWorkflow)
		}
		group.DisplayName = strings.TrimSpace(group.DisplayName)
		if group.SortOrder == 0 {
			group.SortOrder = int64(i * 100)
		}
		if groupsByID[group.ID] {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, fmt.Errorf("duplicate workflow node group id %q", group.ID)
		}
		if existingID, exists := groupsByKey[group.Key]; exists {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, fmt.Errorf("duplicate workflow node group key %q between %q and %q", group.Key, existingID, group.ID)
		}
		groupsByID[group.ID] = true
		groupsByKey[group.Key] = group.ID
		prepared.nodeGroups[i] = group
	}

	def := workflow.Definition{ID: workflowID, DisplayName: displayName}
	groupNodeIDs := map[string][]workflow.NodeID{}
	for i, node := range prepared.nodes {
		if strings.TrimSpace(string(node.WorkflowID)) == "" {
			node.WorkflowID = workflowID
		}
		node.GroupID = strings.TrimSpace(node.GroupID)
		node.GroupKey = strings.TrimSpace(node.GroupKey)
		if node.GroupID == "" && node.GroupKey != "" {
			groupID, ok := groupsByKey[workflow.ModelKey(node.GroupKey)]
			if !ok {
				return preparedWorkflowGraphSave{}, workflow.Definition{}, fmt.Errorf("workflow node group key %q is not in the saved graph", node.GroupKey)
			}
			node.GroupID = groupID
		}
		if node.GroupID != "" && !groupsByID[node.GroupID] {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, fmt.Errorf("workflow node group %q is not in the saved graph", node.GroupID)
		}
		if err := validateNodeCompletionMode(node.Kind, node.CompletionMode); err != nil {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, err
		}
		node.CompletionMode = nodeCompletionMode(node)
		prepared.nodes[i] = node
		if node.GroupID != "" {
			groupNodeIDs[node.GroupID] = append(groupNodeIDs[node.GroupID], node.ID)
		}
		def.Nodes = append(def.Nodes, workflow.Node{WorkflowID: node.WorkflowID, ID: node.ID, Key: node.Key, Kind: node.Kind, DisplayName: node.DisplayName, GroupID: node.GroupID, SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, InputFields: node.InputFields, JoinInputProviders: node.JoinInputProviders, OutputFields: node.OutputFields})
	}
	for _, group := range prepared.nodeGroups {
		def.NodeGroups = append(def.NodeGroups, workflow.NodeGroup{WorkflowID: group.WorkflowID, ID: group.ID, Key: group.Key, DisplayName: group.DisplayName, MemberNodeIDs: groupNodeIDs[group.ID]})
	}
	for i, group := range prepared.transitionGroups {
		if strings.TrimSpace(string(group.WorkflowID)) == "" {
			group.WorkflowID = workflowID
		}
		prepared.transitionGroups[i] = group
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: group.WorkflowID, ID: group.ID, SourceNodeID: group.SourceNodeID, TransitionID: group.TransitionID, DisplayName: group.DisplayName, Description: group.Description})
	}
	for i, edge := range prepared.edges {
		if strings.TrimSpace(string(edge.WorkflowID)) == "" {
			edge.WorkflowID = workflowID
		}
		edge.ContextSource = workflow.CanonicalContextSource(edge.ContextSource)
		prepared.edges[i] = edge
		def.Edges = append(def.Edges, workflow.Edge{WorkflowID: edge.WorkflowID, ID: edge.ID, Key: edge.Key, TransitionGroupID: edge.TransitionGroupID, TargetNodeID: edge.TargetNodeID, ContextMode: edge.ContextMode, ContextSource: edge.ContextSource, RequiresApproval: edge.RequiresApproval, PromptTemplate: edge.PromptTemplate, Parameters: edge.Parameters, InputBindings: edge.InputBindings, OutputRequirements: edge.OutputRequirements})
	}
	return prepared, def, nil
}

func workflowGraphSaveImpact(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID, prepared preparedWorkflowGraphSave) (WorkflowGraphSaveImpact, removedWorkflowGraphRows, error) {
	currentGroups, err := q.ListWorkflowNodeGroups(ctx, string(workflowID))
	if err != nil {
		return WorkflowGraphSaveImpact{}, removedWorkflowGraphRows{}, err
	}
	currentNodes, err := q.ListWorkflowNodes(ctx, string(workflowID))
	if err != nil {
		return WorkflowGraphSaveImpact{}, removedWorkflowGraphRows{}, err
	}
	currentTransitionGroups, err := q.ListWorkflowTransitionGroups(ctx, string(workflowID))
	if err != nil {
		return WorkflowGraphSaveImpact{}, removedWorkflowGraphRows{}, err
	}
	currentEdges, err := q.ListWorkflowEdges(ctx, string(workflowID))
	if err != nil {
		return WorkflowGraphSaveImpact{}, removedWorkflowGraphRows{}, err
	}
	removed := removedWorkflowGraphRows{}
	nextGroups := workflowGraphNodeGroupIDs(prepared.nodeGroups)
	for _, group := range currentGroups {
		if !nextGroups[group.ID] {
			removed.nodeGroups = append(removed.nodeGroups, group.ID)
		}
	}
	nextNodes := workflowGraphNodeIDs(prepared.nodes)
	for _, node := range currentNodes {
		id := workflow.NodeID(node.ID)
		if !nextNodes[id] {
			removed.nodes = append(removed.nodes, id)
		}
	}
	nextTransitionGroups := workflowGraphTransitionGroupIDs(prepared.transitionGroups)
	for _, group := range currentTransitionGroups {
		id := workflow.TransitionGroupID(group.ID)
		if !nextTransitionGroups[id] {
			removed.transitionGroups = append(removed.transitionGroups, id)
		}
	}
	nextEdges := workflowGraphEdgeIDs(prepared.edges)
	for _, edge := range currentEdges {
		id := workflow.EdgeID(edge.ID)
		if !nextEdges[id] {
			removed.edges = append(removed.edges, id)
		}
	}

	impact := WorkflowGraphSaveImpact{
		RemovedNodeCount:            int64(len(removed.nodes)),
		RemovedTransitionGroupCount: int64(len(removed.transitionGroups)),
		RemovedEdgeCount:            int64(len(removed.edges)),
	}
	for _, nodeID := range removed.nodes {
		count, err := q.CountTaskNodeReferences(ctx, nullableString(string(nodeID)))
		if err != nil {
			return WorkflowGraphSaveImpact{}, removedWorkflowGraphRows{}, err
		}
		impact.NodeTaskReferenceCount += count
		currentCount, err := q.CountCurrentTaskNodeAnchorReferences(ctx, nullableString(string(nodeID)))
		if err != nil {
			return WorkflowGraphSaveImpact{}, removedWorkflowGraphRows{}, err
		}
		impact.CurrentNodeTaskReferenceCount += currentCount
	}
	for _, edgeID := range removed.edges {
		count, err := q.CountTaskEdgeReferences(ctx, sql.NullString{String: string(edgeID), Valid: true})
		if err != nil {
			return WorkflowGraphSaveImpact{}, removedWorkflowGraphRows{}, err
		}
		impact.EdgeTaskReferenceCount += count
	}
	return impact, removed, nil
}

func workflowGraphSaveBlockers(req WorkflowGraphSaveRequest, impact WorkflowGraphSaveImpact) []WorkflowGraphSaveBlocker {
	blockers := []WorkflowGraphSaveBlocker{}
	if impact.CurrentNodeTaskReferenceCount > 0 {
		blockers = append(blockers, WorkflowGraphSaveBlocker{Code: "node_task_references", Message: "Removed workflow nodes are referenced by current task state.", Count: impact.CurrentNodeTaskReferenceCount})
	}
	if impact.EdgeTaskReferenceCount > 0 {
		blockers = append(blockers, WorkflowGraphSaveBlocker{Code: "edge_task_references", Message: "Removed workflow edges are referenced by existing tasks.", Count: impact.EdgeTaskReferenceCount})
	}
	removedCount := impact.RemovedNodeCount + impact.RemovedTransitionGroupCount + impact.RemovedEdgeCount
	if removedCount > 0 && !req.Confirmed {
		blockers = append(blockers, WorkflowGraphSaveBlocker{Code: "confirmation_required", Message: "Workflow graph save removes graph rows. Confirm with the current impact before saving.", Count: removedCount})
	}
	if removedCount > 0 && req.Confirmed && !workflowGraphSaveConfirmationMatches(req, impact) {
		blockers = append(blockers, WorkflowGraphSaveBlocker{Code: "impact_changed", Message: "Workflow graph save impact changed. Refresh the preview before saving.", Count: 1})
	}
	return blockers
}

func workflowGraphSaveConfirmationMatches(req WorkflowGraphSaveRequest, impact WorkflowGraphSaveImpact) bool {
	return req.Confirmed &&
		req.ExpectedRemovedNodeCount == impact.RemovedNodeCount &&
		req.ExpectedRemovedTransitionGroupCount == impact.RemovedTransitionGroupCount &&
		req.ExpectedRemovedEdgeCount == impact.RemovedEdgeCount &&
		req.ExpectedNodeTaskReferenceCount == impact.NodeTaskReferenceCount &&
		req.ExpectedEdgeTaskReferenceCount == impact.EdgeTaskReferenceCount
}

func workflowGraphSaveHasBlocker(blockers []WorkflowGraphSaveBlocker, code string) bool {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func workflowGraphSaveBlockersFromEditPolicy(blockers []WorkflowGraphEditPolicyBlocker) []WorkflowGraphSaveBlocker {
	if len(blockers) == 0 {
		return nil
	}
	out := make([]WorkflowGraphSaveBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		out = append(out, WorkflowGraphSaveBlocker{Code: blocker.Code, Message: blocker.Message, Count: blocker.Count})
	}
	return out
}

func workflowGraphSavePreparedEqual(left preparedWorkflowGraphSave, right preparedWorkflowGraphSave) bool {
	leftComparable := workflowGraphSaveComparable(left)
	rightComparable := workflowGraphSaveComparable(right)
	return slices.Equal(leftComparable.NodeGroups, rightComparable.NodeGroups) &&
		slices.EqualFunc(leftComparable.Nodes, rightComparable.Nodes, comparableWorkflowGraphSaveNodesEqual) &&
		slices.Equal(leftComparable.TransitionGroups, rightComparable.TransitionGroups) &&
		slices.EqualFunc(leftComparable.Edges, rightComparable.Edges, comparableWorkflowGraphSaveEdgesEqual)
}

type comparableWorkflowGraphSave struct {
	NodeGroups       []comparableWorkflowGraphSaveNodeGroup
	Nodes            []comparableWorkflowGraphSaveNode
	TransitionGroups []comparableWorkflowGraphSaveTransitionGroup
	Edges            []comparableWorkflowGraphSaveEdge
}

type comparableWorkflowGraphSaveNodeGroup struct {
	ID          string
	WorkflowID  workflow.WorkflowID
	Key         workflow.ModelKey
	DisplayName string
	SortOrder   int64
}

type comparableWorkflowGraphSaveNode struct {
	ID                 workflow.NodeID
	WorkflowID         workflow.WorkflowID
	Key                workflow.ModelKey
	Kind               workflow.NodeKind
	DisplayName        string
	GroupID            string
	SubagentRole       string
	PromptTemplate     string
	CompletionMode     string
	InputFields        []workflow.InputField
	JoinInputProviders []workflow.JoinInputProvider
	OutputFields       []workflow.OutputField
	SortOrder          int64
}

type comparableWorkflowGraphSaveTransitionGroup struct {
	ID           workflow.TransitionGroupID
	WorkflowID   workflow.WorkflowID
	SourceNodeID workflow.NodeID
	TransitionID workflow.TransitionID
	DisplayName  string
	Description  string
	SortOrder    int64
}

type comparableWorkflowGraphSaveEdge struct {
	ID                 workflow.EdgeID
	WorkflowID         workflow.WorkflowID
	TransitionGroupID  workflow.TransitionGroupID
	Key                workflow.ModelKey
	TargetNodeID       workflow.NodeID
	RequiresApproval   bool
	ContextMode        workflow.ContextMode
	ContextSource      workflow.ContextSource
	PromptTemplate     string
	Parameters         []workflow.Parameter
	InputBindings      []workflow.InputBinding
	OutputRequirements []workflow.OutputRequirement
	SortOrder          int64
}

func comparableWorkflowGraphSaveNodesEqual(item comparableWorkflowGraphSaveNode, other comparableWorkflowGraphSaveNode) bool {
	return item.ID == other.ID && item.WorkflowID == other.WorkflowID && item.Key == other.Key && item.Kind == other.Kind && item.DisplayName == other.DisplayName && item.GroupID == other.GroupID && item.SubagentRole == other.SubagentRole && item.PromptTemplate == other.PromptTemplate && item.CompletionMode == other.CompletionMode && item.SortOrder == other.SortOrder && slices.Equal(item.InputFields, other.InputFields) && slices.Equal(item.JoinInputProviders, other.JoinInputProviders) && slices.Equal(item.OutputFields, other.OutputFields)
}

func comparableWorkflowGraphSaveEdgesEqual(item comparableWorkflowGraphSaveEdge, other comparableWorkflowGraphSaveEdge) bool {
	return item.ID == other.ID && item.WorkflowID == other.WorkflowID && item.TransitionGroupID == other.TransitionGroupID && item.Key == other.Key && item.TargetNodeID == other.TargetNodeID && item.RequiresApproval == other.RequiresApproval && item.ContextMode == other.ContextMode && item.ContextSource == other.ContextSource && item.PromptTemplate == other.PromptTemplate && item.SortOrder == other.SortOrder && slices.Equal(item.Parameters, other.Parameters) && slices.Equal(item.InputBindings, other.InputBindings) && slices.Equal(item.OutputRequirements, other.OutputRequirements)
}

func workflowGraphSaveComparable(prepared preparedWorkflowGraphSave) comparableWorkflowGraphSave {
	out := comparableWorkflowGraphSave{
		NodeGroups:       make([]comparableWorkflowGraphSaveNodeGroup, 0, len(prepared.nodeGroups)),
		Nodes:            make([]comparableWorkflowGraphSaveNode, 0, len(prepared.nodes)),
		TransitionGroups: make([]comparableWorkflowGraphSaveTransitionGroup, 0, len(prepared.transitionGroups)),
		Edges:            make([]comparableWorkflowGraphSaveEdge, 0, len(prepared.edges)),
	}
	for index, group := range prepared.nodeGroups {
		sortOrder := group.SortOrder
		if sortOrder == 0 {
			sortOrder = int64(index * 100)
		}
		out.NodeGroups = append(out.NodeGroups, comparableWorkflowGraphSaveNodeGroup{ID: group.ID, WorkflowID: group.WorkflowID, Key: group.Key, DisplayName: strings.TrimSpace(group.DisplayName), SortOrder: sortOrder})
	}
	for index, node := range prepared.nodes {
		out.Nodes = append(out.Nodes, comparableWorkflowGraphSaveNode{ID: node.ID, WorkflowID: node.WorkflowID, Key: node.Key, Kind: node.Kind, DisplayName: strings.TrimSpace(node.DisplayName), GroupID: strings.TrimSpace(node.GroupID), SubagentRole: strings.TrimSpace(node.SubagentRole), PromptTemplate: strings.TrimSpace(node.PromptTemplate), CompletionMode: nodeCompletionMode(node), InputFields: node.InputFields, JoinInputProviders: node.JoinInputProviders, OutputFields: node.OutputFields, SortOrder: int64(index * 100)})
	}
	for index, group := range prepared.transitionGroups {
		out.TransitionGroups = append(out.TransitionGroups, comparableWorkflowGraphSaveTransitionGroup{ID: group.ID, WorkflowID: group.WorkflowID, SourceNodeID: group.SourceNodeID, TransitionID: workflow.TransitionID(strings.TrimSpace(string(group.TransitionID))), DisplayName: strings.TrimSpace(group.DisplayName), Description: strings.TrimSpace(group.Description), SortOrder: int64(index * 100)})
	}
	for index, edge := range prepared.edges {
		contextSource := workflow.CanonicalContextSource(edge.ContextSource)
		out.Edges = append(out.Edges, comparableWorkflowGraphSaveEdge{ID: edge.ID, WorkflowID: edge.WorkflowID, TransitionGroupID: edge.TransitionGroupID, Key: edge.Key, TargetNodeID: edge.TargetNodeID, RequiresApproval: edge.RequiresApproval, ContextMode: edge.ContextMode, ContextSource: contextSource, PromptTemplate: strings.TrimSpace(edge.PromptTemplate), Parameters: edge.Parameters, InputBindings: edge.InputBindings, OutputRequirements: edge.OutputRequirements, SortOrder: int64(index * 100)})
	}
	return out
}

func applyWorkflowGraphSave(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID, prepared preparedWorkflowGraphSave, removed removedWorkflowGraphRows) error {
	for _, edgeID := range removed.edges {
		if deleted, err := q.DeleteWorkflowEdge(ctx, string(edgeID)); err != nil {
			return fmt.Errorf("delete removed workflow edge: %w", err)
		} else if deleted != 1 {
			return sql.ErrNoRows
		}
	}
	for _, groupID := range removed.transitionGroups {
		if deleted, err := q.DeleteWorkflowTransitionGroupByID(ctx, string(groupID)); err != nil {
			return fmt.Errorf("delete removed workflow transition group: %w", err)
		} else if deleted != 1 {
			return sql.ErrNoRows
		}
	}
	for _, nodeID := range removed.nodes {
		if deleted, err := q.DeleteWorkflowNode(ctx, string(nodeID)); err != nil {
			return fmt.Errorf("delete removed workflow node: %w", err)
		} else if deleted != 1 {
			return sql.ErrNoRows
		}
	}
	for _, groupID := range removed.nodeGroups {
		if deleted, err := q.DeleteWorkflowNodeGroup(ctx, sqlitegen.DeleteWorkflowNodeGroupParams{ID: groupID, WorkflowID: string(workflowID)}); err != nil {
			return fmt.Errorf("delete removed workflow node group: %w", err)
		} else if deleted != 1 {
			return sql.ErrNoRows
		}
	}
	for _, group := range prepared.nodeGroups {
		if err := upsertWorkflowNodeGroup(ctx, q, group); err != nil {
			return err
		}
	}
	for index, node := range prepared.nodes {
		if err := upsertWorkflowNode(ctx, q, node, int64(index*100)); err != nil {
			return err
		}
	}
	for index, group := range prepared.transitionGroups {
		if err := upsertWorkflowTransitionGroup(ctx, q, group, int64(index*100)); err != nil {
			return err
		}
	}
	for index, edge := range prepared.edges {
		if err := upsertWorkflowEdge(ctx, q, edge, int64(index*100)); err != nil {
			return err
		}
	}
	return nil
}

func upsertWorkflowNodeGroup(ctx context.Context, q *sqlitegen.Queries, group NodeGroupRecord) error {
	updated, err := q.UpsertWorkflowNodeGroup(ctx, sqlitegen.UpsertWorkflowNodeGroupParams{
		ID:          group.ID,
		WorkflowID:  string(group.WorkflowID),
		GroupKey:    string(group.Key),
		DisplayName: strings.TrimSpace(group.DisplayName),
		SortOrder:   group.SortOrder,
	})
	return expectAffectedRowCount(updated, err, "save workflow node group")
}

func upsertWorkflowNode(ctx context.Context, q *sqlitegen.Queries, node NodeRecord, sortOrder int64) error {
	if err := validateNodeCompletionMode(node.Kind, node.CompletionMode); err != nil {
		return err
	}
	inputFields, err := workflow.MarshalString(node.InputFields)
	if err != nil {
		return err
	}
	joinProviders, err := workflow.MarshalString(node.JoinInputProviders)
	if err != nil {
		return err
	}
	outputFields, err := workflow.MarshalString(node.OutputFields)
	if err != nil {
		return err
	}
	updated, err := q.UpsertWorkflowNode(ctx, sqlitegen.UpsertWorkflowNodeParams{
		ID:                     string(node.ID),
		WorkflowID:             string(node.WorkflowID),
		NodeKey:                string(node.Key),
		Kind:                   string(node.Kind),
		DisplayName:            strings.TrimSpace(node.DisplayName),
		SubagentRole:           strings.TrimSpace(node.SubagentRole),
		PromptTemplate:         strings.TrimSpace(node.PromptTemplate),
		CompletionMode:         nodeCompletionMode(node),
		InputFieldsJson:        inputFields,
		JoinInputProvidersJson: joinProviders,
		OutputFieldsJson:       outputFields,
		GroupID:                nullableString(node.GroupID),
		SortOrder:              sortOrder,
	})
	return expectAffectedRowCount(updated, err, "save workflow node")
}

func upsertWorkflowTransitionGroup(ctx context.Context, q *sqlitegen.Queries, group TransitionGroupRecord, sortOrder int64) error {
	updated, err := q.UpsertWorkflowTransitionGroup(ctx, sqlitegen.UpsertWorkflowTransitionGroupParams{
		ID:           string(group.ID),
		SourceNodeID: string(group.SourceNodeID),
		TransitionID: strings.TrimSpace(string(group.TransitionID)),
		DisplayName:  strings.TrimSpace(group.DisplayName),
		Description:  strings.TrimSpace(group.Description),
		SortOrder:    sortOrder,
		WorkflowID:   string(group.WorkflowID),
	})
	return expectAffectedRowCount(updated, err, "save workflow transition group")
}

func upsertWorkflowEdge(ctx context.Context, q *sqlitegen.Queries, edge EdgeRecord, sortOrder int64) error {
	contextSource := workflow.CanonicalContextSource(edge.ContextSource)
	parameters, err := marshalJSONArray(edge.Parameters)
	if err != nil {
		return err
	}
	inputs, err := workflow.MarshalString(edge.InputBindings)
	if err != nil {
		return err
	}
	requirements, err := workflow.MarshalString(edge.OutputRequirements)
	if err != nil {
		return err
	}
	requiresApproval := int64(0)
	if edge.RequiresApproval {
		requiresApproval = 1
	}
	updated, err := q.UpsertWorkflowEdge(ctx, sqlitegen.UpsertWorkflowEdgeParams{
		ID:                     string(edge.ID),
		TransitionGroupID:      string(edge.TransitionGroupID),
		EdgeKey:                string(edge.Key),
		TargetNodeID:           string(edge.TargetNodeID),
		RequiresApproval:       requiresApproval,
		ContextMode:            string(edge.ContextMode),
		ContextSourceKind:      string(contextSource.Kind),
		ContextSourceNodeKey:   string(contextSource.NodeKey),
		PromptTemplate:         strings.TrimSpace(edge.PromptTemplate),
		ParametersJson:         parameters,
		InputBindingsJson:      inputs,
		OutputRequirementsJson: requirements,
		SortOrder:              sortOrder,
		WorkflowID:             string(edge.WorkflowID),
	})
	return expectAffectedRowCount(updated, err, "save workflow edge")
}

func expectAffectedRowCount(count int64, err error, op string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if count != 1 {
		return fmt.Errorf("%s: %w", op, sql.ErrNoRows)
	}
	return nil
}

func workflowGraphNodeGroupIDs(groups []NodeGroupRecord) map[string]bool {
	out := make(map[string]bool, len(groups))
	for _, group := range groups {
		out[group.ID] = true
	}
	return out
}

func workflowGraphNodeIDs(nodes []NodeRecord) map[workflow.NodeID]bool {
	out := make(map[workflow.NodeID]bool, len(nodes))
	for _, node := range nodes {
		out[node.ID] = true
	}
	return out
}

func workflowGraphTransitionGroupIDs(groups []TransitionGroupRecord) map[workflow.TransitionGroupID]bool {
	out := make(map[workflow.TransitionGroupID]bool, len(groups))
	for _, group := range groups {
		out[group.ID] = true
	}
	return out
}

func workflowGraphEdgeIDs(edges []EdgeRecord) map[workflow.EdgeID]bool {
	out := make(map[workflow.EdgeID]bool, len(edges))
	for _, edge := range edges {
		out[edge.ID] = true
	}
	return out
}
