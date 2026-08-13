package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"core/server/metadata/sqlitegen"
	"core/server/metadata/sqlitelifecyclegen"
	"core/server/workflow"
	"core/server/workflowscript"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/workflowcontract"
)

type WorkflowGraphSaveRequest struct {
	WorkflowID                          runtimeids.WorkflowID
	ExpectedVersion                     int64
	Metadata                            *WorkflowGraphSaveMetadata
	Confirmed                           bool
	ExpectedRemovedNodeGroupCount       int64
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
	RemovedNodeGroupCount         int64
	RemovedNodeCount              int64
	RemovedTransitionGroupCount   int64
	RemovedEdgeCount              int64
	RemovedEntities               []WorkflowGraphEntityReference
	NodeTaskReferenceCount        int64
	CurrentNodeTaskReferenceCount int64
	EdgeTaskReferenceCount        int64
}

const (
	WorkflowGraphEntityTypeEdge            = workflowcontract.WorkflowGraphEntityTypeEdge
	WorkflowGraphEntityTypeNode            = workflowcontract.WorkflowGraphEntityTypeNode
	WorkflowGraphEntityTypeNodeGroup       = workflowcontract.WorkflowGraphEntityTypeNodeGroup
	WorkflowGraphEntityTypeTransitionGroup = workflowcontract.WorkflowGraphEntityTypeTransitionGroup
)

type WorkflowGraphEntityReference = workflowcontract.WorkflowGraphEntityReference

type WorkflowGraphSaveBlocker struct {
	Code             string
	Message          string
	Count            int64
	AffectedEntities []WorkflowGraphEntityReference
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
	current           preparedWorkflowGraphSave
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

type workflowGraphSaveLaneContext struct {
	store      *Store
	workflowID runtimeids.WorkflowID
}

type workflowGraphSaveLaneContextKey struct{}

func (s *Store) RunWorkflowGraphSaveOperation(
	ctx context.Context,
	workflowID runtimeids.WorkflowID,
	operation func(context.Context) (WorkflowGraphSaveResult, error),
) (WorkflowGraphSaveResult, error) {
	if operation == nil {
		return WorkflowGraphSaveResult{}, errors.New("workflow graph save operation is required")
	}
	if active, ok := ctx.Value(workflowGraphSaveLaneContextKey{}).(workflowGraphSaveLaneContext); ok &&
		active.store == s &&
		active.workflowID == workflowID {
		return operation(ctx)
	}
	if s.graphSaves == nil {
		return WorkflowGraphSaveResult{}, errors.New("workflow graph save lanes are required")
	}
	lease, err := s.graphSaves.Acquire(ctx, workflowID)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	defer lease.Release()
	return operation(context.WithValue(ctx, workflowGraphSaveLaneContextKey{}, workflowGraphSaveLaneContext{
		store:      s,
		workflowID: workflowID,
	}))
}

func (s *Store) PreviewWorkflowGraphSave(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSaveResult, error) {
	return s.RunWorkflowGraphSaveOperation(ctx, req.WorkflowID, func(ctx context.Context) (WorkflowGraphSaveResult, error) {
		return s.previewWorkflowGraphSave(ctx, req)
	})
}

func (s *Store) previewWorkflowGraphSave(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSaveResult, error) {
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
	current, err := q.GetWorkflow(ctx, workflowID)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	plan := WorkflowGraphSavePlan{
		WorkflowID: workflowID,
		Version:    current.Version,
	}
	if current.Version != req.ExpectedVersion {
		plan.Blockers = workflowGraphSaveVersionChangedBlockers(current.Version)
		return plan, nil
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
	if err := validateWorkflowGraphAdditionIdentityOwnership(currentGraph, prepared); err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	graphChanged := !workflowGraphSavePreparedEqual(currentGraph, prepared)
	structural := describeWorkflowGraphSave(currentGraph, prepared)
	plan = WorkflowGraphSavePlan{
		WorkflowID:      workflowID,
		Version:         current.Version,
		Prepared:        prepared,
		current:         currentGraph,
		Metadata:        metadata,
		GraphChanged:    graphChanged,
		MetadataChanged: metadataChanged,
		Structural:      structural,
		Definition:      def,
	}
	_, record, err := workflowDefinitionFromQueries(ctx, q, workflowID)
	if err != nil {
		return WorkflowGraphSavePlan{}, err
	}
	plan.Record = record
	var evaluation workflowGraphSaveDynamicImpact
	if (graphChanged || metadataChanged) && current.Version == req.ExpectedVersion {
		evaluation, err = evaluateWorkflowGraphSaveDynamicImpact(ctx, q, workflowID, structural)
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
	blockingValidationErrors := validation.BlockingErrors()
	plan.ValidationErrors = validation.Errors
	blockers := workflowGraphSaveBlockers(req, evaluation)
	blockers = append(blockers, workflowGraphSaveBlockersFromEditPolicy(evaluation.EditPolicy.Blockers)...)
	if len(blockingValidationErrors) > 0 {
		blockers = append(blockers, WorkflowGraphSaveBlocker{
			Code:             "validation_failed",
			Message:          "Workflow graph has blocking validation errors.",
			Count:            int64(len(blockingValidationErrors)),
			AffectedEntities: workflowGraphEntityReferencesFromValidationErrors(blockingValidationErrors),
		})
	}
	plan.Blockers = blockers
	return plan, nil
}

func (s *Store) SaveWorkflowGraph(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSaveResult, error) {
	return s.RunWorkflowGraphSaveOperation(ctx, req.WorkflowID, func(ctx context.Context) (WorkflowGraphSaveResult, error) {
		return s.saveWorkflowGraph(ctx, req)
	})
}

func (s *Store) saveWorkflowGraph(ctx context.Context, req WorkflowGraphSaveRequest) (WorkflowGraphSaveResult, error) {
	workflowID := req.WorkflowID
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
	evaluation, err := evaluateWorkflowGraphSaveDynamicImpact(ctx, q, plan.WorkflowID, plan.Structural)
	if err != nil {
		return WorkflowGraphSaveResult{}, err
	}
	blockers := workflowGraphSaveBlockers(req, evaluation)
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
		if err := applyWorkflowGraphSave(ctx, q, plan.WorkflowID, plan.current, plan.Prepared, plan.Removed); err != nil {
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
	prepared := WorkflowGraphSaveMetadata{Name: metadata.Name, Description: strings.TrimSpace(metadata.Description), ExecutionTargetPolicy: &policy}
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

func prepareWorkflowGraphSave(workflowID runtimeids.WorkflowID, displayName string, executionTargetPolicy workflow.ExecutionTargetPolicy, req WorkflowGraphSaveRequest) (preparedWorkflowGraphSave, workflow.Definition, error) {
	prepared := preparedWorkflowGraphSave{
		nodeGroups:       append([]NodeGroupRecord(nil), req.NodeGroups...),
		nodes:            append([]NodeRecord(nil), req.Nodes...),
		transitionGroups: append([]TransitionGroupRecord(nil), req.TransitionGroups...),
		edges:            append([]EdgeRecord(nil), req.Edges...),
	}
	for i, group := range prepared.nodeGroups {
		group.SortOrder = int64(i * 100)
		prepared.nodeGroups[i] = group
	}

	for i, node := range prepared.nodes {
		prepared.nodes[i] = node
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

func validateWorkflowGraphAdditionIdentityOwnership(current preparedWorkflowGraphSave, next preparedWorkflowGraphSave) error {
	currentTypes := make(map[string]map[workflowcontract.WorkflowGraphEntityType]bool)
	indexCurrent := func(entityType workflowcontract.WorkflowGraphEntityType, ids []string) {
		for _, id := range ids {
			if currentTypes[id] == nil {
				currentTypes[id] = make(map[workflowcontract.WorkflowGraphEntityType]bool)
			}
			currentTypes[id][entityType] = true
		}
	}
	indexCurrent(WorkflowGraphEntityTypeNodeGroup, workflowGraphRecordIDs(current.nodeGroups, func(group NodeGroupRecord) string { return group.ID }))
	indexCurrent(WorkflowGraphEntityTypeNode, workflowGraphRecordIDs(current.nodes, func(node NodeRecord) string { return string(node.ID) }))
	indexCurrent(WorkflowGraphEntityTypeTransitionGroup, workflowGraphRecordIDs(current.transitionGroups, func(group TransitionGroupRecord) string { return string(group.ID) }))
	indexCurrent(WorkflowGraphEntityTypeEdge, workflowGraphRecordIDs(current.edges, func(edge EdgeRecord) string { return string(edge.ID) }))

	collections := []struct {
		name       string
		entityType workflowcontract.WorkflowGraphEntityType
		ids        []string
	}{
		{"graph.node_groups", WorkflowGraphEntityTypeNodeGroup, workflowGraphRecordIDs(next.nodeGroups, func(group NodeGroupRecord) string { return group.ID })},
		{"graph.nodes", WorkflowGraphEntityTypeNode, workflowGraphRecordIDs(next.nodes, func(node NodeRecord) string { return string(node.ID) })},
		{"graph.transition_groups", WorkflowGraphEntityTypeTransitionGroup, workflowGraphRecordIDs(next.transitionGroups, func(group TransitionGroupRecord) string { return string(group.ID) })},
		{"graph.edges", WorkflowGraphEntityTypeEdge, workflowGraphRecordIDs(next.edges, func(edge EdgeRecord) string { return string(edge.ID) })},
	}
	for _, collection := range collections {
		for index, id := range collection.ids {
			if currentTypes[id][collection.entityType] || len(currentTypes[id]) == 0 {
				continue
			}
			return WorkflowGraphIdentityOwnershipError{
				Field:    fmt.Sprintf("%s[%d].id", collection.name, index),
				Identity: id,
			}
		}
	}
	return nil
}

func workflowGraphRecordIDs[T any](entities []T, id func(T) string) []string {
	ids := make([]string, 0, len(entities))
	for _, entity := range entities {
		ids = append(ids, id(entity))
	}
	return ids
}

func workflowGraphSaveBlockers(req WorkflowGraphSaveRequest, evaluation workflowGraphSaveDynamicImpact) []WorkflowGraphSaveBlocker {
	impact := evaluation.Impact
	blockers := []WorkflowGraphSaveBlocker{}
	if impact.CurrentNodeTaskReferenceCount > 0 {
		blockers = append(blockers, WorkflowGraphSaveBlocker{
			Code:             "node_task_references",
			Message:          "Removed workflow nodes are referenced by current task state.",
			Count:            impact.CurrentNodeTaskReferenceCount,
			AffectedEntities: evaluation.RemovedNodeTaskReferenceEntities,
		})
	}
	if impact.EdgeTaskReferenceCount > 0 {
		blockers = append(blockers, WorkflowGraphSaveBlocker{
			Code:             "edge_task_references",
			Message:          "Removed workflow edges are referenced by existing tasks.",
			Count:            impact.EdgeTaskReferenceCount,
			AffectedEntities: evaluation.RemovedEdgeTaskReferenceEntities,
		})
	}
	removedCount := impact.RemovedNodeCount + impact.RemovedTransitionGroupCount + impact.RemovedEdgeCount
	if removedCount > 0 && !req.Confirmed {
		blockers = append(blockers, WorkflowGraphSaveBlocker{
			Code:             "confirmation_required",
			Message:          "Workflow graph save removes graph rows. Confirm with the current impact before saving.",
			Count:            removedCount,
			AffectedEntities: workflowGraphSaveConfirmationEntities(impact.RemovedEntities),
		})
	}
	if removedCount > 0 && req.Confirmed && !workflowGraphSaveConfirmationMatches(req, impact) {
		blockers = append(blockers, WorkflowGraphSaveBlocker{
			Code:             "impact_changed",
			Message:          "Workflow graph changed while trying to save the new graph! Inspect the new topology and retry as needed.",
			Count:            1,
			AffectedEntities: canonicalWorkflowGraphEntityReferences(impact.RemovedEntities),
		})
	}
	return blockers
}

func workflowGraphSaveVersionChangedBlockers(version int64) []WorkflowGraphSaveBlocker {
	return []WorkflowGraphSaveBlocker{{
		Code:             "version_changed",
		Message:          "Workflow graph changed while trying to save the new graph! Inspect the new topology and retry as needed.",
		Count:            version,
		AffectedEntities: []WorkflowGraphEntityReference{},
	}}
}

func WorkflowGraphSaveVersionChangedResult(version int64) WorkflowGraphSaveResult {
	return WorkflowGraphSavePlan{
		Version:  version,
		Blockers: workflowGraphSaveVersionChangedBlockers(version),
	}.workflowGraphSaveResult(false)
}

func workflowGraphSaveConfirmationMatches(req WorkflowGraphSaveRequest, impact WorkflowGraphSaveImpact) bool {
	return req.Confirmed &&
		req.ExpectedRemovedNodeGroupCount == impact.RemovedNodeGroupCount &&
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
		out = append(out, WorkflowGraphSaveBlocker{
			Code:             blocker.Code,
			Message:          blocker.Message,
			Count:            blocker.Count,
			AffectedEntities: canonicalWorkflowGraphEntityReferences(blocker.AffectedEntities),
		})
	}
	return out
}

func workflowGraphSaveConfirmationEntities(removed []WorkflowGraphEntityReference) []WorkflowGraphEntityReference {
	entities := make([]WorkflowGraphEntityReference, 0, len(removed))
	for _, entity := range removed {
		if entity.EntityType != WorkflowGraphEntityTypeNodeGroup {
			entities = append(entities, entity)
		}
	}
	return canonicalWorkflowGraphEntityReferences(entities)
}

func canonicalWorkflowGraphEntityReferences(references []WorkflowGraphEntityReference) []WorkflowGraphEntityReference {
	out := append([]WorkflowGraphEntityReference(nil), references...)
	slices.SortFunc(out, workflowcontract.CompareWorkflowGraphEntityReferences)
	return slices.CompactFunc(out, func(left WorkflowGraphEntityReference, right WorkflowGraphEntityReference) bool {
		return workflowcontract.CompareWorkflowGraphEntityReferences(left, right) == 0
	})
}

func workflowGraphEntityReferencesFromValidationErrors(errors []workflow.ValidationError) []WorkflowGraphEntityReference {
	entities := make([]WorkflowGraphEntityReference, 0, len(errors))
	for _, validationError := range errors {
		if validationError.NodeID != nil {
			entities = append(entities, WorkflowGraphEntityReference{EntityType: WorkflowGraphEntityTypeNode, EntityID: string(*validationError.NodeID)})
		}
		if validationError.TransitionGroupID != nil {
			entities = append(entities, WorkflowGraphEntityReference{EntityType: WorkflowGraphEntityTypeTransitionGroup, EntityID: string(*validationError.TransitionGroupID)})
		}
		if validationError.EdgeID != nil {
			entities = append(entities, WorkflowGraphEntityReference{EntityType: WorkflowGraphEntityTypeEdge, EntityID: string(*validationError.EdgeID)})
		}
		if validationError.ProviderEdgeID != nil {
			entities = append(entities, WorkflowGraphEntityReference{EntityType: WorkflowGraphEntityTypeEdge, EntityID: string(*validationError.ProviderEdgeID)})
		}
		entities = append(entities, validationError.RelatedEntities...)
	}
	return canonicalWorkflowGraphEntityReferences(entities)
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
	GroupID            *string
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
	ID                workflow.EdgeID
	WorkflowID        runtimeids.WorkflowID
	TransitionGroupID workflow.TransitionGroupID
	Key               workflow.ModelKey
	TargetNodeID      workflow.NodeID
	AssigneeSelection workflow.AssigneeSelection
	ThinkingSelection workflow.ThinkingSelection
	RequiresApproval  bool
	ContextMode       workflow.ContextMode
	ContextSource     workflow.ContextSource
	PromptTemplate    string
	Parameters        []workflow.Parameter
	SortOrder         int64
}

func comparableWorkflowGraphSaveNodesEqual(item comparableWorkflowGraphSaveNode, other comparableWorkflowGraphSaveNode) bool {
	return item.ID == other.ID && item.WorkflowID == other.WorkflowID && item.Key == other.Key && item.Kind == other.Kind && item.DisplayName == other.DisplayName && textutil.EqualOptional(item.GroupID, other.GroupID) && item.SubagentRole == other.SubagentRole && item.CompletionMode == other.CompletionMode && item.ScriptPath == other.ScriptPath && item.SortOrder == other.SortOrder && slices.Equal(item.JoinInputProviders, other.JoinInputProviders)
}

func comparableWorkflowGraphSaveEdgesEqual(item comparableWorkflowGraphSaveEdge, other comparableWorkflowGraphSaveEdge) bool {
	return item.ID == other.ID && item.WorkflowID == other.WorkflowID && item.TransitionGroupID == other.TransitionGroupID && item.Key == other.Key && item.TargetNodeID == other.TargetNodeID && item.AssigneeSelection == other.AssigneeSelection && item.ThinkingSelection == other.ThinkingSelection && item.RequiresApproval == other.RequiresApproval && item.ContextMode == other.ContextMode && item.ContextSource == other.ContextSource && item.PromptTemplate == other.PromptTemplate && item.SortOrder == other.SortOrder && slices.Equal(item.Parameters, other.Parameters)
}

func workflowGraphSaveComparable(prepared preparedWorkflowGraphSave) comparableWorkflowGraphSave {
	out := comparableWorkflowGraphSave{
		NodeGroups:       make([]comparableWorkflowGraphSaveNodeGroup, 0, len(prepared.nodeGroups)),
		Nodes:            make([]comparableWorkflowGraphSaveNode, 0, len(prepared.nodes)),
		TransitionGroups: make([]comparableWorkflowGraphSaveTransitionGroup, 0, len(prepared.transitionGroups)),
		Edges:            make([]comparableWorkflowGraphSaveEdge, 0, len(prepared.edges)),
	}
	for index, group := range prepared.nodeGroups {
		out.NodeGroups = append(out.NodeGroups, comparableWorkflowGraphSaveNodeGroup{ID: group.ID, WorkflowID: group.WorkflowID, Key: group.Key, DisplayName: strings.TrimSpace(group.DisplayName), SortOrder: int64(index * 100)})
	}
	for index, node := range prepared.nodes {
		var groupID *string
		if node.GroupID != nil {
			trimmed := strings.TrimSpace(*node.GroupID)
			groupID = &trimmed
		}
		out.Nodes = append(out.Nodes, comparableWorkflowGraphSaveNode{ID: node.ID, WorkflowID: node.WorkflowID, Key: node.Key, Kind: node.Kind, DisplayName: strings.TrimSpace(node.DisplayName), GroupID: groupID, SubagentRole: strings.TrimSpace(node.SubagentRole), CompletionMode: nodeCompletionMode(node), ScriptPath: strings.TrimSpace(node.ScriptPath), JoinInputProviders: node.JoinInputProviders, SortOrder: int64(index * 100)})
	}
	for index, group := range prepared.transitionGroups {
		out.TransitionGroups = append(out.TransitionGroups, comparableWorkflowGraphSaveTransitionGroup{ID: group.ID, WorkflowID: group.WorkflowID, SourceNodeID: group.SourceNodeID, TransitionID: workflow.TransitionID(strings.TrimSpace(string(group.TransitionID))), DisplayName: strings.TrimSpace(group.DisplayName), Description: strings.TrimSpace(group.Description), SortOrder: int64(index * 100)})
	}
	for index, edge := range prepared.edges {
		contextSource := workflow.CanonicalContextSource(edge.ContextSource)
		out.Edges = append(out.Edges, comparableWorkflowGraphSaveEdge{ID: edge.ID, WorkflowID: edge.WorkflowID, TransitionGroupID: edge.TransitionGroupID, Key: edge.Key, TargetNodeID: edge.TargetNodeID, AssigneeSelection: workflow.CanonicalAssigneeSelection(edge.AssigneeSelection), ThinkingSelection: workflow.CanonicalThinkingSelection(edge.ThinkingSelection), RequiresApproval: edge.RequiresApproval, ContextMode: edge.ContextMode, ContextSource: contextSource, PromptTemplate: strings.TrimSpace(edge.PromptTemplate), Parameters: edge.Parameters, SortOrder: int64(index * 100)})
	}
	return out
}

func applyWorkflowGraphSave(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID, current preparedWorkflowGraphSave, prepared preparedWorkflowGraphSave, removed removedWorkflowGraphRows) error {
	if err := stageWorkflowGraphSaveKeyChanges(ctx, q, current); err != nil {
		return err
	}
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

func stageWorkflowGraphSaveKeyChanges(ctx context.Context, q *sqlitegen.Queries, current preparedWorkflowGraphSave) error {
	for _, group := range current.nodeGroups {
		group.Key = workflow.ModelKey(uuid.NewString())
		if err := upsertWorkflowNodeGroup(ctx, q, group, "stage workflow node group key"); err != nil {
			return err
		}
	}
	for _, node := range current.nodes {
		node.Key = workflow.ModelKey(uuid.NewString())
		if err := upsertWorkflowNode(ctx, q, node, node.SortOrder, "stage workflow node key"); err != nil {
			return err
		}
	}
	for _, group := range current.transitionGroups {
		group.TransitionID = workflow.TransitionID(uuid.NewString())
		if err := upsertWorkflowTransitionGroup(ctx, q, group, group.SortOrder, "stage workflow transition group id"); err != nil {
			return err
		}
	}
	for _, edge := range current.edges {
		edge.Key = workflow.ModelKey(uuid.NewString())
		if err := upsertWorkflowEdge(ctx, q, edge, edge.SortOrder, "stage workflow edge key"); err != nil {
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
		GroupID:                nullableGraphIdentityArgument(node.GroupID),
		SortOrder:              sortOrder,
	})
	if err != nil {
		return fmt.Errorf("%s %q: %w", op, node.ID, err)
	}
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
	canonicalParameters := make([]workflow.Parameter, 0, len(edge.Parameters))
	for _, parameter := range edge.Parameters {
		canonicalParameters = append(canonicalParameters, parameter.Canonical())
	}
	parameters, err := marshalJSONArray(canonicalParameters)
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
	assigneeSelection := workflow.DefaultAssigneeSelection(edge.AssigneeSelection)
	thinkingSelection := workflow.DefaultThinkingSelection(edge.ThinkingSelection)
	updated, err := q.UpsertWorkflowEdge(ctx, sqlitegen.UpsertWorkflowEdgeParams{
		ID:                     string(edge.ID),
		TransitionGroupID:      string(edge.TransitionGroupID),
		EdgeKey:                string(edge.Key),
		TargetNodeID:           string(edge.TargetNodeID),
		AssigneeSelection:      string(assigneeSelection),
		ThinkingSelection:      string(thinkingSelection),
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
