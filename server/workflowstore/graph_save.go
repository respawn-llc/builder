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
	"core/server/workflowscript"
	"core/shared/runtimeids"
)

type WorkflowGraphSaveRequest struct {
	WorkflowID                          runtimeids.WorkflowID
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
	Name                  string
	Description           string
	ExecutionTargetPolicy *workflow.ExecutionTargetPolicy
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
	Definition           workflow.Definition
	Record               WorkflowRecord
	ValidationResults    map[workflow.ValidationContext]workflow.ValidationResult
}

type WorkflowGraphSavePlan struct {
	WorkflowID        runtimeids.WorkflowID
	Version           int64
	Prepared          preparedWorkflowGraphSave
	Metadata          *WorkflowGraphSaveMetadata
	GraphChanged      bool
	MetadataChanged   bool
	Structural        workflowGraphSaveStructuralDescriptor
	Removed           removedWorkflowGraphRows
	Impact            WorkflowGraphSaveImpact
	EditPolicy        WorkflowGraphEditPolicyResult
	Blockers          []WorkflowGraphSaveBlocker
	ValidationErrors  []workflow.ValidationError
	Definition        workflow.Definition
	Record            WorkflowRecord
	ValidationResults map[workflow.ValidationContext]workflow.ValidationResult
}

func (s *Store) PreviewWorkflowGraphSave(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSaveResult, error) {
	plan, err := s.prepareWorkflowGraphSave(ctx, req)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	return plan.workflowGraphSaveResult(false), nil
}

func (s *Store) prepareWorkflowGraphSave(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSavePlan, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WorkflowGraphSavePlan{}, fmt.Errorf("begin workflow graph save preparation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := s.planWorkflowGraphSave(ctx, s.queries.WithTx(tx), req)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowGraphSavePlan{}, fmt.Errorf("commit workflow graph save preparation: %w", err)
	}
	return plan, nil
}

func (s *Store) planWorkflowGraphSave(ctx context.Context, q *sqlitegen.Queries, req WorkflowGraphSaveRequest) (WorkflowGraphSavePlan, error) {
	workflowID := req.WorkflowID
	if workflowID.IsZero() {
		return WorkflowGraphSavePlan{}, ErrWorkflowIDRequired
	}
	if req.ExpectedVersion < 0 {
		return WorkflowGraphSavePlan{}, errors.New("expected version must be non-negative")
	}
	current, err := q.GetWorkflow(ctx, workflowID)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	currentPolicy := workflow.ExecutionTargetPolicy{
		Mode:      workflow.ExecutionTargetMode(current.ExecutionTargetPolicy),
		CustomRef: workflowCustomRefFromRow(current.ExecutionTargetCustomRef),
	}.Canonical()
	metadata, metadataChanged, err := prepareWorkflowGraphSaveMetadata(current.Name, current.Description, currentPolicy, req.Metadata)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	displayName := current.Name
	if metadata != nil {
		displayName = metadata.Name
	}
	executionTargetPolicy := currentPolicy
	if metadata != nil && metadata.ExecutionTargetPolicy != nil {
		executionTargetPolicy = *metadata.ExecutionTargetPolicy
	}
	prepared, def, err := prepareWorkflowGraphSave(workflowID, displayName, executionTargetPolicy, req)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	graphChanged := !workflowGraphSavePreparedEqual(currentGraph, prepared)
	structural := describeWorkflowGraphSave(currentGraph, prepared)
	plan := WorkflowGraphSavePlan{
		WorkflowID:      workflowID,
		Version:         current.Version,
		Prepared:        prepared,
		Metadata:        metadata,
		GraphChanged:    graphChanged,
		MetadataChanged: metadataChanged,
		Structural:      structural,
		Definition:      def,
	}
	currentDefinition, record, err := workflowDefinitionFromQueries(ctx, q, workflowID)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	plan.Record = record
	var evaluation workflowGraphSaveDynamicImpact
	if (graphChanged || metadataChanged) && current.Version == req.ExpectedVersion {
		evaluation, err = evaluateWorkflowGraphSaveDynamicImpact(ctx, q, workflowID, currentDefinition, def, structural)
		if err != nil {
			return WorkflowGraphSavePlan{}, err
		}
		plan.Removed = structural.Removed
		plan.Impact = evaluation.Impact
		plan.EditPolicy = evaluation.EditPolicy
	}
	plan.ValidationResults = workflowscript.EvaluateDefinition(def, []workflow.ValidationContext{
		workflow.ValidationContextDraft,
		workflow.ValidationContextExecution,
	}, s.roleResolver, nil)
	validation := plan.ValidationResults[workflow.ValidationContextDraft]
	plan.ValidationErrors = validation.Errors
	if !graphChanged && !metadataChanged {
		return plan, nil
	}
	if current.Version != req.ExpectedVersion {
		plan.Blockers = workflowGraphSaveVersionChangedBlockers(current.Version)
		return plan, nil
	}
	blockingValidationErrors := validation.BlockingErrors()
	plan.ValidationErrors = validation.Errors
	blockers := workflowGraphSaveBlockers(req, evaluation.Impact)
	blockers = append(blockers, workflowGraphSaveBlockersFromEditPolicy(evaluation.EditPolicy.Blockers)...)
	if len(blockingValidationErrors) > 0 {
		blockers = append(blockers, WorkflowGraphSaveBlocker{Code: "validation_failed", Message: "Workflow graph has blocking validation errors.", Count: int64(len(blockingValidationErrors))})
	}
	plan.Blockers = blockers
	return plan, nil
}

func (s *Store) SaveWorkflowGraph(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSaveResult, error) {
	workflowID := req.WorkflowID
	if workflowID.IsZero() {
		return WorkflowGraphSaveResult{}, ErrWorkflowIDRequired
	}
	if s.graphSaves == nil {
		return WorkflowGraphSaveResult{}, errors.New("workflow graph save lanes are required")
	}
	lease, err := s.graphSaves.Acquire(ctx, workflowID)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	defer lease.Release()

	plan, err := s.prepareWorkflowGraphSave(ctx, req)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	if len(plan.Blockers) > 0 {
		return plan.workflowGraphSaveResult(false), nil
	}
	if !plan.GraphChanged && !plan.MetadataChanged {
		return plan.workflowGraphSaveResult(true), nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sqlitelifecyclegen.New(tx).DeferForeignKeys(ctx); err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireWorkflowGraphSaveWriteLock(ctx, workflowID)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	if locked != 1 {
		return WorkflowGraphSaveResult{}, sql.ErrNoRows
	}
	current, err := q.GetWorkflow(ctx, workflowID)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	if current.Version != plan.Version {
		plan.Version = current.Version
		plan.Blockers = workflowGraphSaveVersionChangedBlockers(current.Version)
		return plan.workflowGraphSaveResult(false), nil
	}
	currentDefinition, _, err := workflowDefinitionFromQueries(ctx, q, plan.WorkflowID)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	evaluation, err := evaluateWorkflowGraphSaveDynamicImpact(ctx, q, plan.WorkflowID, currentDefinition, plan.Definition, plan.Structural)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	blockers := workflowGraphSaveBlockers(req, evaluation.Impact)
	blockers = append(blockers, workflowGraphSaveBlockersFromEditPolicy(evaluation.EditPolicy.Blockers)...)
	if len(blockers) > 0 {
		plan.Removed = plan.Structural.Removed
		plan.Impact = evaluation.Impact
		plan.EditPolicy = evaluation.EditPolicy
		plan.Blockers = blockers
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
		if plan.Metadata.ExecutionTargetPolicy == nil {
			return WorkflowGraphSaveResult{}, errors.New("prepared workflow execution target policy is required")
		}
		customRef := workflowCustomRefForQuery(plan.Metadata.ExecutionTargetPolicy.CustomRef)
		if plan.GraphChanged {
			updated, err := q.UpdateWorkflowMetadataWithoutVersion(ctx, sqlitegen.UpdateWorkflowMetadataWithoutVersionParams{
				ID:                       plan.WorkflowID,
				Name:                     plan.Metadata.Name,
				Description:              plan.Metadata.Description,
				ExecutionTargetPolicy:    string(plan.Metadata.ExecutionTargetPolicy.Mode),
				ExecutionTargetCustomRef: customRef,
				UpdatedAtUnixMs:          s.now().UnixMilli(),
			})
			if err != nil {
				return WorkflowGraphSaveResult{}, fmt.Errorf("update workflow metadata: %w", err)
			}
			if updated != 1 {
				return WorkflowGraphSaveResult{}, sql.ErrNoRows
			}
		} else {
			updated, err := q.UpdateWorkflowMetadata(ctx, sqlitegen.UpdateWorkflowMetadataParams{
				ID:                       plan.WorkflowID,
				Name:                     plan.Metadata.Name,
				Description:              plan.Metadata.Description,
				ExecutionTargetPolicy:    string(plan.Metadata.ExecutionTargetPolicy.Mode),
				ExecutionTargetCustomRef: customRef,
				UpdatedAtUnixMs:          s.now().UnixMilli(),
			})
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
	record := plan.Record
	record.Version = version
	if plan.MetadataChanged && plan.Metadata != nil {
		record.Name = plan.Metadata.Name
		record.Description = plan.Metadata.Description
		record.ExecutionTargetPolicy = *plan.Metadata.ExecutionTargetPolicy
	}
	result.Record = record
	result.Definition = plan.Definition
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
		Definition:           p.Definition,
		Record:               p.Record,
		ValidationResults:    p.ValidationResults,
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

// workflowGraphSaveStructuralDescriptor is derived from one immutable graph
// snapshot. Dynamic Task and Current Node references are intentionally absent:
// they are evaluated again immediately before a save commits.
type workflowGraphSaveStructuralDescriptor struct {
	Removed    removedWorkflowGraphRows
	EditPolicy workflowGraphEditPolicyStructuralDescriptor
}

func prepareWorkflowGraphSaveMetadata(currentName string, currentDescription string, currentPolicy workflow.ExecutionTargetPolicy, metadata *WorkflowGraphSaveMetadata) (*WorkflowGraphSaveMetadata, bool, error) {
	if metadata == nil {
		return nil, false, nil
	}
	policy := currentPolicy
	if metadata.ExecutionTargetPolicy != nil {
		policy = normalizeWorkflowExecutionTargetPolicy(*metadata.ExecutionTargetPolicy)
	}
	prepared := WorkflowGraphSaveMetadata{Name: strings.TrimSpace(metadata.Name), Description: strings.TrimSpace(metadata.Description), ExecutionTargetPolicy: &policy}
	if prepared.Name == "" {
		return nil, false, ErrWorkflowNameRequired
	}
	if policy.Mode != workflow.ExecutionTargetModeCustomRef && policy.CustomRef != nil {
		return nil, false, errors.New("execution target custom ref is only valid for custom_ref policy")
	}
	if policy.Mode == workflow.ExecutionTargetModeCustomRef && policy.CustomRef != nil && strings.TrimSpace(*policy.CustomRef) == "" {
		return nil, false, errors.New("execution target custom ref must be non-blank when present")
	}
	if policy.Mode != workflow.ExecutionTargetModeNone &&
		policy.Mode != workflow.ExecutionTargetModeHead &&
		policy.Mode != workflow.ExecutionTargetModeDefaultBranch &&
		policy.Mode != workflow.ExecutionTargetModeCustomRef &&
		policy.Mode != workflow.ExecutionTargetModeAskOnFirstExecution {
		return nil, false, errors.New("execution target policy mode is invalid")
	}
	changed := prepared.Name != currentName || prepared.Description != currentDescription || !workflowExecutionTargetPoliciesEqual(*prepared.ExecutionTargetPolicy, currentPolicy)
	return &prepared, changed, nil
}

func workflowExecutionTargetPoliciesEqual(a workflow.ExecutionTargetPolicy, b workflow.ExecutionTargetPolicy) bool {
	if a.Mode != b.Mode {
		return false
	}
	if a.CustomRef == nil || b.CustomRef == nil {
		return a.CustomRef == nil && b.CustomRef == nil
	}
	return *a.CustomRef == *b.CustomRef
}

func validateWorkflowGraphRecordWorkflowID(expected runtimeids.WorkflowID, actual runtimeids.WorkflowID, kind string, recordID string) error {
	if actual.IsZero() {
		return fmt.Errorf("workflow %s %w", kind, ErrWorkflowIDRequired)
	}
	if actual != expected {
		return fmt.Errorf("workflow %s %q belongs to workflow %q: %w", kind, recordID, actual, ErrBelongsToOtherWorkflow)
	}
	return nil
}

func prepareWorkflowGraphSave(workflowID runtimeids.WorkflowID, displayName string, executionTargetPolicy workflow.ExecutionTargetPolicy, req WorkflowGraphSaveRequest) (preparedWorkflowGraphSave, workflow.Definition, error) {
	prepared := preparedWorkflowGraphSave{
		nodeGroups:       append([]NodeGroupRecord(nil), req.NodeGroups...),
		nodes:            append([]NodeRecord(nil), req.Nodes...),
		transitionGroups: append([]TransitionGroupRecord(nil), req.TransitionGroups...),
		edges:            append([]EdgeRecord(nil), req.Edges...),
	}
	groupsByKey := map[workflow.ModelKey]string{}
	groupsByID := map[string]bool{}
	for i, group := range prepared.nodeGroups {
		if err := validateWorkflowGraphRecordWorkflowID(workflowID, group.WorkflowID, "node group", group.ID); err != nil {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, err
		}
		group.ID = strings.TrimSpace(group.ID)
		if group.ID == "" {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, errors.New("workflow node group id is required")
		}
		group.Key = workflow.ModelKey(strings.TrimSpace(string(group.Key)))
		if group.Key == "" {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, errors.New("workflow node group key is required")
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

	for i, node := range prepared.nodes {
		if err := validateWorkflowGraphRecordWorkflowID(workflowID, node.WorkflowID, "node", string(node.ID)); err != nil {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, err
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
	}
	for i, group := range prepared.transitionGroups {
		if err := validateWorkflowGraphRecordWorkflowID(workflowID, group.WorkflowID, "transition group", string(group.ID)); err != nil {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, err
		}
		prepared.transitionGroups[i] = group
	}
	for i, edge := range prepared.edges {
		if err := validateWorkflowGraphRecordWorkflowID(workflowID, edge.WorkflowID, "edge", string(edge.ID)); err != nil {
			return preparedWorkflowGraphSave{}, workflow.Definition{}, err
		}
		edge.ContextSource = workflow.CanonicalContextSource(edge.ContextSource)
		prepared.edges[i] = edge
	}
	def, err := workflowDefinitionFromPreparedGraph(prepared, workflowID, displayName, executionTargetPolicy)
	if err != nil {
		return preparedWorkflowGraphSave{}, workflow.Definition{}, err
	}
	return prepared, def, nil
}

func describeWorkflowGraphSave(current preparedWorkflowGraphSave, next preparedWorkflowGraphSave) workflowGraphSaveStructuralDescriptor {
	removed := removedWorkflowGraphRows{}
	nextGroups := workflowGraphNodeGroupIDs(next.nodeGroups)
	for _, group := range current.nodeGroups {
		if !nextGroups[group.ID] {
			removed.nodeGroups = append(removed.nodeGroups, group.ID)
		}
	}
	nextNodes := workflowGraphNodeIDs(next.nodes)
	for _, node := range current.nodes {
		id := node.ID
		if !nextNodes[id] {
			removed.nodes = append(removed.nodes, id)
		}
	}
	nextTransitionGroups := workflowGraphTransitionGroupIDs(next.transitionGroups)
	for _, group := range current.transitionGroups {
		id := group.ID
		if !nextTransitionGroups[id] {
			removed.transitionGroups = append(removed.transitionGroups, id)
		}
	}
	nextEdges := workflowGraphEdgeIDs(next.edges)
	for _, edge := range current.edges {
		id := edge.ID
		if !nextEdges[id] {
			removed.edges = append(removed.edges, id)
		}
	}
	return workflowGraphSaveStructuralDescriptor{
		Removed:    removed,
		EditPolicy: describeWorkflowGraphEditPolicy(current, next),
	}
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

func workflowGraphSaveVersionChangedBlockers(version int64) []WorkflowGraphSaveBlocker {
	return []WorkflowGraphSaveBlocker{{Code: "version_changed", Message: "Workflow changed. Refresh before saving.", Count: version}}
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
	WorkflowID  runtimeids.WorkflowID
	Key         workflow.ModelKey
	DisplayName string
	SortOrder   int64
}

type comparableWorkflowGraphSaveNode struct {
	ID                 workflow.NodeID
	WorkflowID         runtimeids.WorkflowID
	Key                workflow.ModelKey
	Kind               workflow.NodeKind
	DisplayName        string
	GroupID            string
	SubagentRole       string
	CompletionMode     string
	ScriptPath         string
	JoinInputProviders []workflow.JoinInputProvider
	SortOrder          int64
}

type comparableWorkflowGraphSaveTransitionGroup struct {
	ID           workflow.TransitionGroupID
	WorkflowID   runtimeids.WorkflowID
	SourceNodeID workflow.NodeID
	TransitionID workflow.TransitionID
	DisplayName  string
	Description  string
	SortOrder    int64
}

type comparableWorkflowGraphSaveEdge struct {
	ID                 workflow.EdgeID
	WorkflowID         runtimeids.WorkflowID
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
	return item.ID == other.ID && item.WorkflowID == other.WorkflowID && item.Key == other.Key && item.Kind == other.Kind && item.DisplayName == other.DisplayName && item.GroupID == other.GroupID && item.SubagentRole == other.SubagentRole && item.CompletionMode == other.CompletionMode && item.ScriptPath == other.ScriptPath && item.SortOrder == other.SortOrder && slices.Equal(item.JoinInputProviders, other.JoinInputProviders)
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
		out.Nodes = append(out.Nodes, comparableWorkflowGraphSaveNode{ID: node.ID, WorkflowID: node.WorkflowID, Key: node.Key, Kind: node.Kind, DisplayName: strings.TrimSpace(node.DisplayName), GroupID: strings.TrimSpace(node.GroupID), SubagentRole: strings.TrimSpace(node.SubagentRole), CompletionMode: nodeCompletionMode(node), ScriptPath: strings.TrimSpace(node.ScriptPath), JoinInputProviders: node.JoinInputProviders, SortOrder: int64(index * 100)})
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

func applyWorkflowGraphSave(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID, prepared preparedWorkflowGraphSave, removed removedWorkflowGraphRows) error {
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
		if deleted, err := q.DeleteWorkflowNodeGroup(ctx, sqlitegen.DeleteWorkflowNodeGroupParams{ID: groupID, WorkflowID: workflowID}); err != nil {
			return fmt.Errorf("delete removed workflow node group: %w", err)
		} else if deleted != 1 {
			return sql.ErrNoRows
		}
	}
	for _, group := range prepared.nodeGroups {
		if err := upsertWorkflowNodeGroup(ctx, q, group, "save workflow node group"); err != nil {
			return err
		}
	}
	for index, node := range prepared.nodes {
		if err := upsertWorkflowNode(ctx, q, node, int64(index*100), "save workflow node"); err != nil {
			return err
		}
	}
	for index, group := range prepared.transitionGroups {
		if err := upsertWorkflowTransitionGroup(ctx, q, group, int64(index*100), "save workflow transition group"); err != nil {
			return err
		}
	}
	for index, edge := range prepared.edges {
		if err := upsertWorkflowEdge(ctx, q, edge, int64(index*100), "save workflow edge"); err != nil {
			return err
		}
	}
	return nil
}

func upsertWorkflowNodeGroup(ctx context.Context, q *sqlitegen.Queries, group NodeGroupRecord, op string) error {
	updated, err := q.UpsertWorkflowNodeGroup(ctx, sqlitegen.UpsertWorkflowNodeGroupParams{
		ID:          group.ID,
		WorkflowID:  group.WorkflowID,
		GroupKey:    string(group.Key),
		DisplayName: strings.TrimSpace(group.DisplayName),
		SortOrder:   group.SortOrder,
	})
	return expectAffectedRowCount(updated, err, op)
}

func upsertWorkflowNode(ctx context.Context, q *sqlitegen.Queries, node NodeRecord, sortOrder int64, op string) error {
	if err := validateNodeCompletionMode(node.Kind, node.CompletionMode); err != nil {
		return err
	}
	joinProviders, err := workflow.MarshalString(node.JoinInputProviders)
	if err != nil {
		return err
	}
	updated, err := q.UpsertWorkflowNode(ctx, sqlitegen.UpsertWorkflowNodeParams{
		ID:                     string(node.ID),
		WorkflowID:             node.WorkflowID,
		NodeKey:                string(node.Key),
		Kind:                   string(node.Kind),
		DisplayName:            strings.TrimSpace(node.DisplayName),
		SubagentRole:           strings.TrimSpace(node.SubagentRole),
		CompletionMode:         nodeCompletionMode(node),
		ScriptPath:             nullableString(node.ScriptPath),
		JoinInputProvidersJson: joinProviders,
		GroupID:                nullableString(node.GroupID),
		SortOrder:              sortOrder,
	})
	return expectAffectedRowCount(updated, err, op)
}

func upsertWorkflowTransitionGroup(ctx context.Context, q *sqlitegen.Queries, group TransitionGroupRecord, sortOrder int64, op string) error {
	updated, err := q.UpsertWorkflowTransitionGroup(ctx, sqlitegen.UpsertWorkflowTransitionGroupParams{
		ID:           string(group.ID),
		SourceNodeID: string(group.SourceNodeID),
		TransitionID: strings.TrimSpace(string(group.TransitionID)),
		DisplayName:  strings.TrimSpace(group.DisplayName),
		Description:  strings.TrimSpace(group.Description),
		SortOrder:    sortOrder,
		WorkflowID:   group.WorkflowID,
	})
	return expectAffectedRowCount(updated, err, op)
}

func upsertWorkflowEdge(ctx context.Context, q *sqlitegen.Queries, edge EdgeRecord, sortOrder int64, op string) error {
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
		WorkflowID:             edge.WorkflowID,
	})
	return expectAffectedRowCount(updated, err, op)
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
