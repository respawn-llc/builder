package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowGraphApplyOutcomeKind string

const (
	workflowGraphApplySaved                workflowGraphApplyOutcomeKind = "saved"
	workflowGraphApplyUnchanged            workflowGraphApplyOutcomeKind = "unchanged"
	workflowGraphApplyConfirmationRequired workflowGraphApplyOutcomeKind = "confirmation_required"
	workflowGraphApplyBlocked              workflowGraphApplyOutcomeKind = "blocked"
	workflowGraphApplyInvalidDocument      workflowGraphApplyOutcomeKind = "invalid_document"
	workflowGraphApplyRequestFailed        workflowGraphApplyOutcomeKind = "request_failed"
)

type workflowGraphApplyOutcome struct {
	Outcome           workflowGraphApplyOutcomeKind                                           `json:"outcome"`
	WorkflowID        *runtimeids.WorkflowID                                                  `json:"workflow_id,omitempty"`
	CurrentVersion    *int64                                                                  `json:"current_version,omitempty"`
	ValidationResults map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse `json:"validation_results,omitempty"`
	Impact            *serverapi.WorkflowGraphSaveImpact                                      `json:"impact,omitempty"`
	Blockers          []serverapi.WorkflowGraphSaveBlocker                                    `json:"blockers,omitempty"`
	Definition        *serverapi.WorkflowDefinition                                           `json:"definition,omitempty"`
	Message           *string                                                                 `json:"message,omitempty"`
}

func workflowGraphApplySubcommand(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow graph apply", stderr, workflowGraphApplyUsage)
	confirm := fs.Bool("confirm", false, "save using the impact calculated by this invocation")
	jsonOut := fs.Bool("json", false, "write one typed graph apply outcome as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow graph apply requires <path|->")
	if !ok {
		return exitCode
	}

	data, err := loadWorkflowGraphApplyInput(positionals[0], stdin)
	if err != nil {
		return writeWorkflowGraphApplyOutcome(stdout, stderr, workflowGraphApplyFailure(workflowGraphApplyRequestFailed, nil, nil, err), *jsonOut)
	}
	document, err := decodeWorkflowGraphDocument(data)
	if err != nil {
		return writeWorkflowGraphApplyOutcome(stdout, stderr, workflowGraphApplyFailure(workflowGraphApplyInvalidDocument, nil, nil, err), *jsonOut)
	}

	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		return writeWorkflowGraphApplyOutcome(stdout, stderr, workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowGraphApplyWorkflowID(document.WorkflowID),
			nil,
			err,
		), *jsonOut)
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	outcome := runWorkflowGraphApply(ctx, remote, document, *confirm)
	exitCode = writeWorkflowGraphApplyOutcome(stdout, stderr, outcome, *jsonOut)
	if closeErr := remote.Close(); closeErr != nil {
		fmt.Fprintf(stderr, "close workflow graph apply session: %v\n", closeErr)
	}
	return exitCode
}

func loadWorkflowGraphApplyInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		if stdin == nil {
			return nil, errors.New("read Workflow graph document from stdin: stdin is unavailable")
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read Workflow graph document from stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Workflow graph document %q: %w", path, err)
	}
	return data, nil
}

func runWorkflowGraphApply(
	ctx context.Context,
	remote workflowCommandRemote,
	document workflowGraphDocument,
	confirmed bool,
) workflowGraphApplyOutcome {
	workflowID := workflowGraphApplyWorkflowID(document.WorkflowID)
	current, err := resolveWorkflowDefinition(ctx, remote, document.WorkflowID)
	if err != nil {
		return workflowGraphApplyFailure(workflowGraphApplyRequestFailed, workflowID, nil, err)
	}
	currentVersion := workflowGraphApplyVersion(current.Workflow.Version)
	if document.ExpectedVersion != current.Workflow.Version {
		return workflowGraphApplyOutcome{
			Outcome:        workflowGraphApplyBlocked,
			WorkflowID:     workflowID,
			CurrentVersion: currentVersion,
			Blockers: []serverapi.WorkflowGraphSaveBlocker{{
				Code:             "version_changed",
				Message:          "Workflow changed. Refresh before saving.",
				Count:            current.Workflow.Version,
				AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
			}},
		}
	}

	graph, err := document.WorkflowGraphDraft()
	if err != nil {
		return workflowGraphApplyFailure(workflowGraphApplyInvalidDocument, workflowID, currentVersion, err)
	}
	if err := validateWorkflowGraphAdditionIdentities(current, graph); err != nil {
		return workflowGraphApplyFailure(workflowGraphApplyInvalidDocument, workflowID, currentVersion, err)
	}
	preview, err := previewWorkflowGraphDraft(ctx, remote, current, graph)
	if err != nil {
		return workflowGraphApplyFailure(workflowGraphApplyRequestFailed, workflowID, currentVersion, err)
	}
	if err := preview.Response.Validate(); err != nil {
		return workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowID,
			currentVersion,
			fmt.Errorf("validate Workflow graph save preview: %w", err),
		)
	}
	outcome := workflowGraphApplyOutcome{
		WorkflowID:        workflowID,
		CurrentVersion:    workflowGraphApplyVersion(preview.Response.CurrentVersion),
		ValidationResults: preview.Response.ValidationResults,
		Impact:            &preview.Response.Impact,
		Blockers:          preview.Response.Blockers,
	}
	if workflowGraphApplyHasNonConfirmationBlocker(preview.Response.Blockers) {
		outcome.Outcome = workflowGraphApplyBlocked
		return outcome
	}
	if preview.Response.ConfirmationRequired {
		if !preview.Response.Changed || !workflowGraphApplyHasConfirmationBlocker(preview.Response.Blockers) {
			return workflowGraphApplyFailure(
				workflowGraphApplyRequestFailed,
				workflowID,
				outcome.CurrentVersion,
				errors.New("Workflow graph save preview returned inconsistent confirmation state"),
			)
		}
		if confirmed {
			return saveWorkflowGraphApply(ctx, remote, current, preview, workflowGraphSaveConfirmationFromImpact(preview.Response.Impact))
		}
		outcome.Outcome = workflowGraphApplyConfirmationRequired
		return outcome
	}
	if len(preview.Response.Blockers) != 0 || !preview.Response.CanSave {
		return workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowID,
			outcome.CurrentVersion,
			errors.New("Workflow graph save preview returned inconsistent blocker state"),
		)
	}
	if !preview.Response.Changed {
		outcome.Outcome = workflowGraphApplyUnchanged
		return outcome
	}
	return saveWorkflowGraphApply(ctx, remote, current, preview, nil)
}

func validateWorkflowGraphAdditionIdentities(
	current serverapi.WorkflowDefinition,
	submitted serverapi.WorkflowGraphDraft,
) error {
	currentIDs := workflowGraphCurrentEntityIDs{
		nodeGroups:       workflowGraphEntityIDSet(current.NodeGroups, func(group serverapi.WorkflowNodeGroup) string { return group.GroupID }),
		nodes:            workflowGraphEntityIDSet(current.Nodes, func(node serverapi.WorkflowNode) string { return node.ID }),
		transitionGroups: workflowGraphEntityIDSet(current.TransitionGroups, func(group serverapi.WorkflowTransitionGroup) string { return group.ID }),
		edges:            workflowGraphEntityIDSet(current.Edges, func(edge serverapi.WorkflowEdge) string { return edge.ID }),
	}
	for index, group := range submitted.NodeGroups {
		if err := currentIDs.validateAddition(group.ID, workflowGraphIdentityNodeGroup, "graph.node_groups", index); err != nil {
			return err
		}
	}
	for index, node := range submitted.Nodes {
		if err := currentIDs.validateAddition(node.ID, workflowGraphIdentityNode, "graph.nodes", index); err != nil {
			return err
		}
	}
	for index, group := range submitted.TransitionGroups {
		if err := currentIDs.validateAddition(group.ID, workflowGraphIdentityTransitionGroup, "graph.transition_groups", index); err != nil {
			return err
		}
	}
	for index, edge := range submitted.Edges {
		if err := currentIDs.validateAddition(edge.ID, workflowGraphIdentityEdge, "graph.edges", index); err != nil {
			return err
		}
	}
	return nil
}

type workflowGraphCurrentEntityIDs struct {
	nodeGroups       map[string]struct{}
	nodes            map[string]struct{}
	transitionGroups map[string]struct{}
	edges            map[string]struct{}
}

type workflowGraphIdentityType uint8

const (
	workflowGraphIdentityNodeGroup workflowGraphIdentityType = iota
	workflowGraphIdentityNode
	workflowGraphIdentityTransitionGroup
	workflowGraphIdentityEdge
)

type workflowGraphIdentityTypeError struct {
	EntityType workflowGraphIdentityType
}

func (e workflowGraphIdentityTypeError) Error() string {
	return fmt.Sprintf("unsupported Workflow graph identity type %d", e.EntityType)
}

func workflowGraphEntityIDSet[T any](entities []T, id func(T) string) map[string]struct{} {
	ids := make(map[string]struct{}, len(entities))
	for _, entity := range entities {
		ids[id(entity)] = struct{}{}
	}
	return ids
}

func (ids workflowGraphCurrentEntityIDs) validateAddition(
	id string,
	entityType workflowGraphIdentityType,
	collection string,
	index int,
) error {
	currentTypeIDs, err := ids.byType(entityType)
	if err != nil {
		return err
	}
	if _, exists := currentTypeIDs[id]; exists {
		return nil
	}
	existsInAnotherType, err := ids.existsInAnotherType(id, entityType)
	if err != nil {
		return err
	}
	if existsInAnotherType {
		return fmt.Errorf("%s[%d].id %q matches a current entity of another type", collection, index, id)
	}
	if _, err := runtimeids.ParseCanonicalUUIDv4(id, fmt.Sprintf("%s[%d].id", collection, index)); err != nil {
		return err
	}
	return nil
}

func (ids workflowGraphCurrentEntityIDs) existsInAnotherType(id string, entityType workflowGraphIdentityType) (bool, error) {
	for _, candidateType := range []workflowGraphIdentityType{
		workflowGraphIdentityNodeGroup,
		workflowGraphIdentityNode,
		workflowGraphIdentityTransitionGroup,
		workflowGraphIdentityEdge,
	} {
		if candidateType == entityType {
			continue
		}
		candidateIDs, err := ids.byType(candidateType)
		if err != nil {
			return false, err
		}
		if _, exists := candidateIDs[id]; exists {
			return true, nil
		}
	}
	return false, nil
}

func (ids workflowGraphCurrentEntityIDs) byType(entityType workflowGraphIdentityType) (map[string]struct{}, error) {
	switch entityType {
	case workflowGraphIdentityNodeGroup:
		return ids.nodeGroups, nil
	case workflowGraphIdentityNode:
		return ids.nodes, nil
	case workflowGraphIdentityTransitionGroup:
		return ids.transitionGroups, nil
	case workflowGraphIdentityEdge:
		return ids.edges, nil
	default:
		return nil, workflowGraphIdentityTypeError{EntityType: entityType}
	}
}

func workflowGraphSaveConfirmationFromImpact(impact serverapi.WorkflowGraphSaveImpact) *serverapi.WorkflowGraphSaveConfirmation {
	return &serverapi.WorkflowGraphSaveConfirmation{
		ExpectedRemovedNodeGroupCount:       impact.RemovedNodeGroupCount,
		ExpectedRemovedNodeCount:            impact.RemovedNodeCount,
		ExpectedRemovedTransitionGroupCount: impact.RemovedTransitionGroupCount,
		ExpectedRemovedEdgeCount:            impact.RemovedEdgeCount,
		ExpectedNodeTaskReferenceCount:      impact.NodeTaskReferenceCount,
		ExpectedEdgeTaskReferenceCount:      impact.EdgeTaskReferenceCount,
	}
}

func saveWorkflowGraphApply(
	ctx context.Context,
	remote workflowCommandRemote,
	current serverapi.WorkflowDefinition,
	preview workflowGraphPreview,
	confirmation *serverapi.WorkflowGraphSaveConfirmation,
) workflowGraphApplyOutcome {
	workflowID := workflowGraphApplyWorkflowID(current.Workflow.ID)
	response, err := remote.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      current.Workflow.ID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           preview.Graph,
		Confirmation:    confirmation,
	})
	if err != nil {
		return workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowID,
			workflowGraphApplyVersion(preview.Response.CurrentVersion),
			err,
		)
	}
	if err := response.Validate(); err != nil {
		return workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowID,
			workflowGraphApplyVersion(response.CurrentVersion),
			fmt.Errorf("validate Workflow graph save response: %w", err),
		)
	}
	outcome := workflowGraphApplyOutcome{
		WorkflowID:        workflowID,
		CurrentVersion:    workflowGraphApplyVersion(response.CurrentVersion),
		ValidationResults: response.ValidationResults,
		Impact:            &response.Impact,
		Blockers:          response.Blockers,
	}
	if !response.Saved {
		if len(response.Blockers) == 0 {
			return workflowGraphApplyFailure(
				workflowGraphApplyRequestFailed,
				workflowID,
				outcome.CurrentVersion,
				errors.New("Workflow graph save returned blocked without a blocker"),
			)
		}
		outcome.Outcome = workflowGraphApplyBlocked
		return outcome
	}
	if len(response.Blockers) != 0 {
		return workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowID,
			outcome.CurrentVersion,
			errors.New("Workflow graph save returned saved with blockers"),
		)
	}
	if !response.Changed {
		outcome.Outcome = workflowGraphApplyUnchanged
		return outcome
	}
	if response.Definition == nil {
		return workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowID,
			outcome.CurrentVersion,
			errors.New("Workflow graph save returned changed without a definition"),
		)
	}
	outcome.Outcome = workflowGraphApplySaved
	outcome.Definition = response.Definition
	return outcome
}

func workflowGraphApplyHasConfirmationBlocker(blockers []serverapi.WorkflowGraphSaveBlocker) bool {
	for _, blocker := range blockers {
		if blocker.Code == "confirmation_required" {
			return true
		}
	}
	return false
}

func workflowGraphApplyHasNonConfirmationBlocker(blockers []serverapi.WorkflowGraphSaveBlocker) bool {
	for _, blocker := range blockers {
		if blocker.Code != "confirmation_required" {
			return true
		}
	}
	return false
}

func workflowGraphApplyFailure(
	kind workflowGraphApplyOutcomeKind,
	workflowID *runtimeids.WorkflowID,
	currentVersion *int64,
	err error,
) workflowGraphApplyOutcome {
	message := err.Error()
	return workflowGraphApplyOutcome{
		Outcome:        kind,
		WorkflowID:     workflowID,
		CurrentVersion: currentVersion,
		Message:        &message,
	}
}

func workflowGraphApplyWorkflowID(id runtimeids.WorkflowID) *runtimeids.WorkflowID {
	value := id
	return &value
}

func workflowGraphApplyVersion(version int64) *int64 {
	value := version
	return &value
}

func writeWorkflowGraphApplyOutcome(stdout io.Writer, stderr io.Writer, outcome workflowGraphApplyOutcome, jsonOut bool) int {
	if err := outcome.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	exitCode := 1
	if outcome.Outcome == workflowGraphApplySaved || outcome.Outcome == workflowGraphApplyUnchanged {
		exitCode = 0
	}
	if jsonOut {
		if written := writeCommandJSON(stdout, stderr, outcome); written != 0 {
			return written
		}
		return exitCode
	}
	if err := writeWorkflowGraphApplyHumanOutcome(stdout, stderr, outcome); err != nil {
		_, _ = fmt.Fprintf(stderr, "write Workflow graph apply outcome: %v\n", err)
		return 1
	}
	return exitCode
}

func writeWorkflowGraphApplyHumanOutcome(stdout io.Writer, stderr io.Writer, outcome workflowGraphApplyOutcome) error {
	switch outcome.Outcome {
	case workflowGraphApplySaved:
		_, err := fmt.Fprintf(stdout, "Saved Workflow %s graph at version %d.\n", outcome.WorkflowID.String(), *outcome.CurrentVersion)
		return err
	case workflowGraphApplyUnchanged:
		_, err := fmt.Fprintf(stdout, "Workflow %s graph is unchanged at version %d.\n", outcome.WorkflowID.String(), *outcome.CurrentVersion)
		return err
	case workflowGraphApplyConfirmationRequired:
		if _, err := fmt.Fprintf(stderr, "Workflow %s graph changes require confirmation. Rerun with --confirm.\n", outcome.WorkflowID.String()); err != nil {
			return err
		}
		return writeWorkflowGraphApplyPreviewDetails(stderr, outcome)
	case workflowGraphApplyBlocked:
		if _, err := fmt.Fprintf(stderr, "Workflow %s graph apply was blocked.\n", outcome.WorkflowID.String()); err != nil {
			return err
		}
		return writeWorkflowGraphApplyPreviewDetails(stderr, outcome)
	case workflowGraphApplyInvalidDocument, workflowGraphApplyRequestFailed:
		_, err := fmt.Fprintln(stderr, *outcome.Message)
		return err
	}
	return errors.New("Workflow graph apply outcome has no human projection")
}

func writeWorkflowGraphApplyPreviewDetails(stderr io.Writer, outcome workflowGraphApplyOutcome) error {
	if err := writeWorkflowGraphApplyValidation(stderr, outcome.ValidationResults); err != nil {
		return err
	}
	if outcome.Impact != nil {
		if err := writeWorkflowGraphApplyImpact(stderr, *outcome.Impact); err != nil {
			return err
		}
	}
	return writeWorkflowGraphApplyBlockers(stderr, outcome.Blockers)
}

func writeWorkflowGraphApplyValidation(
	stderr io.Writer,
	results map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse,
) error {
	if len(results) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(stderr, "Validation:"); err != nil {
		return err
	}
	for _, mode := range slices.Sorted(maps.Keys(results)) {
		result := results[mode]
		status := "invalid"
		if result.Valid {
			status = "valid"
		}
		if _, err := fmt.Fprintf(stderr, "- %s: %s\n", mode, status); err != nil {
			return err
		}
		for _, validationError := range result.Errors {
			if _, err := fmt.Fprintf(stderr, "  - [%s] %s\n", validationError.Code, validationError.Message); err != nil {
				return err
			}
			if err := writeWorkflowGraphApplyValidationEntityDetails(stderr, validationError); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeWorkflowGraphApplyValidationEntityDetails(stderr io.Writer, validationError serverapi.WorkflowValidationError) error {
	for _, identity := range []struct {
		kind string
		id   string
	}{
		{kind: "node", id: validationError.NodeID},
		{kind: "transition_group", id: validationError.TransitionGroupID},
		{kind: "edge", id: validationError.EdgeID},
	} {
		if identity.id == "" {
			continue
		}
		if _, err := fmt.Fprintf(stderr, "    %s: %s\n", identity.kind, identity.id); err != nil {
			return err
		}
	}
	for _, relatedID := range validationError.RelatedIDs {
		if _, err := fmt.Fprintf(stderr, "    related: %s\n", relatedID); err != nil {
			return err
		}
	}
	return nil
}

func writeWorkflowGraphApplyImpact(stderr io.Writer, impact serverapi.WorkflowGraphSaveImpact) error {
	if _, err := fmt.Fprintln(stderr, "Impact:"); err != nil {
		return err
	}
	for _, count := range []struct {
		name  string
		value int64
	}{
		{name: "removed_node_groups", value: impact.RemovedNodeGroupCount},
		{name: "removed_nodes", value: impact.RemovedNodeCount},
		{name: "removed_transition_groups", value: impact.RemovedTransitionGroupCount},
		{name: "removed_edges", value: impact.RemovedEdgeCount},
		{name: "node_task_references", value: impact.NodeTaskReferenceCount},
		{name: "edge_task_references", value: impact.EdgeTaskReferenceCount},
		{name: "active_current_nodes", value: impact.ActiveCurrentNodeCount},
		{name: "pending_approvals", value: impact.PendingApprovalCount},
		{name: "start_node_changes", value: impact.StartNodeChangeCount},
		{name: "last_terminal_changes", value: impact.LastTerminalChangeCount},
		{name: "task_referenced_node_kind_changes", value: impact.TaskReferencedNodeKindChangeCount},
	} {
		if _, err := fmt.Fprintf(stderr, "- %s: %d\n", count.name, count.value); err != nil {
			return err
		}
	}
	return writeWorkflowGraphApplyEntityReferences(stderr, "Removed entities", impact.RemovedEntities)
}

func writeWorkflowGraphApplyBlockers(stderr io.Writer, blockers []serverapi.WorkflowGraphSaveBlocker) error {
	if len(blockers) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(stderr, "Blockers:"); err != nil {
		return err
	}
	for _, blocker := range blockers {
		if blocker.Count > 0 {
			if _, err := fmt.Fprintf(stderr, "- [%s] %s (%d)\n", blocker.Code, blocker.Message, blocker.Count); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(stderr, "- [%s] %s\n", blocker.Code, blocker.Message); err != nil {
				return err
			}
		}
		if err := writeWorkflowGraphApplyEntityReferences(stderr, "  Affected entities", blocker.AffectedEntities); err != nil {
			return err
		}
	}
	return nil
}

func writeWorkflowGraphApplyEntityReferences(
	stderr io.Writer,
	label string,
	references []serverapi.WorkflowGraphEntityReference,
) error {
	if len(references) == 0 {
		_, err := fmt.Fprintf(stderr, "%s: none\n", label)
		return err
	}
	if _, err := fmt.Fprintf(stderr, "%s:\n", label); err != nil {
		return err
	}
	for _, reference := range references {
		if _, err := fmt.Fprintf(stderr, "    - %s %s\n", reference.EntityType, reference.EntityID); err != nil {
			return err
		}
	}
	return nil
}

func (outcome workflowGraphApplyOutcome) Validate() error {
	switch outcome.Outcome {
	case workflowGraphApplySaved:
		if outcome.WorkflowID == nil || outcome.CurrentVersion == nil || outcome.Impact == nil ||
			outcome.Definition == nil || len(outcome.Blockers) != 0 || outcome.Message != nil {
			return errors.New("Workflow graph apply saved outcome is invalid")
		}
	case workflowGraphApplyUnchanged:
		if outcome.WorkflowID == nil || outcome.CurrentVersion == nil || outcome.Impact == nil ||
			outcome.Definition != nil || len(outcome.Blockers) != 0 || outcome.Message != nil {
			return errors.New("Workflow graph apply unchanged outcome is invalid")
		}
	case workflowGraphApplyConfirmationRequired:
		if outcome.WorkflowID == nil || outcome.CurrentVersion == nil || outcome.Impact == nil ||
			len(outcome.Blockers) == 0 || outcome.Definition != nil || outcome.Message != nil {
			return errors.New("Workflow graph apply confirmation outcome is invalid")
		}
		if workflowGraphApplyHasNonConfirmationBlocker(outcome.Blockers) {
			return errors.New("Workflow graph apply confirmation outcome contains a blocking reason")
		}
	case workflowGraphApplyBlocked:
		if outcome.WorkflowID == nil || outcome.CurrentVersion == nil || len(outcome.Blockers) == 0 ||
			outcome.Definition != nil || outcome.Message != nil {
			return errors.New("Workflow graph apply blocked outcome is invalid")
		}
	case workflowGraphApplyInvalidDocument:
		knownWorkflow := outcome.WorkflowID != nil && outcome.CurrentVersion != nil
		unknownWorkflow := outcome.WorkflowID == nil && outcome.CurrentVersion == nil
		if outcome.Message == nil || (!knownWorkflow && !unknownWorkflow) ||
			outcome.ValidationResults != nil || outcome.Impact != nil || len(outcome.Blockers) != 0 || outcome.Definition != nil {
			return errors.New("Workflow graph apply invalid_document outcome is invalid")
		}
	case workflowGraphApplyRequestFailed:
		if outcome.Message == nil || outcome.ValidationResults != nil || outcome.Impact != nil ||
			len(outcome.Blockers) != 0 || outcome.Definition != nil {
			return errors.New("Workflow graph apply request_failed outcome is invalid")
		}
	default:
		return errors.New("Workflow graph apply outcome discriminator is invalid")
	}
	if outcome.CurrentVersion != nil && *outcome.CurrentVersion < 0 {
		return errors.New("Workflow graph apply current version must be non-negative")
	}
	if outcome.Impact != nil {
		if err := outcome.Impact.Validate(); err != nil {
			return fmt.Errorf("Workflow graph apply impact: %w", err)
		}
	}
	for index, blocker := range outcome.Blockers {
		if err := blocker.Validate(); err != nil {
			return fmt.Errorf("Workflow graph apply blocker %d: %w", index, err)
		}
	}
	if outcome.Message != nil && strings.TrimSpace(*outcome.Message) == "" {
		return errors.New("Workflow graph apply message must not be blank")
	}
	return nil
}
