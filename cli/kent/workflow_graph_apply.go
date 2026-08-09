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
			workflowGraphApplyPointer(document.WorkflowID),
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
	workflowID := workflowGraphApplyPointer(document.WorkflowID)
	current, err := resolveWorkflowDefinition(ctx, remote, document.WorkflowID)
	if err != nil {
		return workflowGraphApplyFailure(workflowGraphApplyRequestFailed, workflowID, nil, err)
	}
	currentVersion := workflowGraphApplyPointer(current.Workflow.Version)
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
		CurrentVersion:    workflowGraphApplyPointer(preview.Response.CurrentVersion),
		ValidationResults: preview.Response.ValidationResults,
		Impact:            &preview.Response.Impact,
		Blockers:          preview.Response.Blockers,
	}
	if workflowGraphApplyHasBlocker(preview.Response.Blockers, false) {
		outcome.Outcome = workflowGraphApplyBlocked
		return outcome
	}
	if preview.Response.ConfirmationRequired {
		if !preview.Response.Changed || !workflowGraphApplyHasBlocker(preview.Response.Blockers, true) {
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
	currentTypes := make(map[string]serverapi.WorkflowGraphEntityType)
	indexCurrent := func(entityType serverapi.WorkflowGraphEntityType, ids []string) {
		for _, id := range ids {
			currentTypes[id] = entityType
		}
	}
	indexCurrent(serverapi.WorkflowGraphEntityTypeNodeGroup, workflowGraphEntityIDs(current.NodeGroups, func(group serverapi.WorkflowNodeGroup) string { return group.GroupID }))
	indexCurrent(serverapi.WorkflowGraphEntityTypeNode, workflowGraphEntityIDs(current.Nodes, func(node serverapi.WorkflowNode) string { return node.ID }))
	indexCurrent(serverapi.WorkflowGraphEntityTypeTransitionGroup, workflowGraphEntityIDs(current.TransitionGroups, func(group serverapi.WorkflowTransitionGroup) string { return group.ID }))
	indexCurrent(serverapi.WorkflowGraphEntityTypeEdge, workflowGraphEntityIDs(current.Edges, func(edge serverapi.WorkflowEdge) string { return edge.ID }))
	collections := []struct {
		name       string
		entityType serverapi.WorkflowGraphEntityType
		ids        []string
	}{
		{"graph.node_groups", serverapi.WorkflowGraphEntityTypeNodeGroup, workflowGraphEntityIDs(submitted.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID })},
		{"graph.nodes", serverapi.WorkflowGraphEntityTypeNode, workflowGraphEntityIDs(submitted.Nodes, func(node serverapi.WorkflowGraphDraftNode) string { return node.ID })},
		{"graph.transition_groups", serverapi.WorkflowGraphEntityTypeTransitionGroup, workflowGraphEntityIDs(submitted.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) string { return group.ID })},
		{"graph.edges", serverapi.WorkflowGraphEntityTypeEdge, workflowGraphEntityIDs(submitted.Edges, func(edge serverapi.WorkflowGraphDraftEdge) string { return edge.ID })},
	}
	for _, collection := range collections {
		for index, id := range collection.ids {
			if currentType, exists := currentTypes[id]; exists {
				if currentType != collection.entityType {
					return fmt.Errorf("%s[%d].id %q matches a current entity of another type", collection.name, index, id)
				}
				continue
			}
			if _, err := runtimeids.ParseCanonicalUUIDv4(id, fmt.Sprintf("%s[%d].id", collection.name, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func workflowGraphEntityIDs[T any](entities []T, id func(T) string) []string {
	ids := make([]string, 0, len(entities))
	for _, entity := range entities {
		ids = append(ids, id(entity))
	}
	return ids
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
	workflowID := workflowGraphApplyPointer(current.Workflow.ID)
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
			workflowGraphApplyPointer(preview.Response.CurrentVersion),
			err,
		)
	}
	if err := response.Validate(); err != nil {
		return workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowID,
			workflowGraphApplyPointer(response.CurrentVersion),
			fmt.Errorf("validate Workflow graph save response: %w", err),
		)
	}
	outcome := workflowGraphApplyOutcome{
		WorkflowID:        workflowID,
		CurrentVersion:    workflowGraphApplyPointer(response.CurrentVersion),
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

func workflowGraphApplyHasBlocker(blockers []serverapi.WorkflowGraphSaveBlocker, confirmation bool) bool {
	for _, blocker := range blockers {
		if (blocker.Code == "confirmation_required") == confirmation {
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

func workflowGraphApplyPointer[T any](value T) *T {
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
	write := func(format string, args ...any) error {
		_, err := fmt.Fprintf(stderr, format, args...)
		return err
	}
	if len(outcome.ValidationResults) > 0 {
		if err := write("Validation:\n"); err != nil {
			return err
		}
		for _, mode := range slices.Sorted(maps.Keys(outcome.ValidationResults)) {
			result := outcome.ValidationResults[mode]
			if err := write("- %s: valid=%t\n", mode, result.Valid); err != nil {
				return err
			}
			for _, validationError := range result.Errors {
				if err := write("  - [%s] %s\n", validationError.Code, validationError.Message); err != nil {
					return err
				}
			}
		}
	}
	if outcome.Impact != nil {
		impact := *outcome.Impact
		if err := write("Impact:\n"); err != nil {
			return err
		}
		for _, count := range []struct {
			name  string
			value int64
		}{
			{"removed_node_groups", impact.RemovedNodeGroupCount},
			{"removed_nodes", impact.RemovedNodeCount},
			{"removed_transition_groups", impact.RemovedTransitionGroupCount},
			{"removed_edges", impact.RemovedEdgeCount},
			{"node_task_references", impact.NodeTaskReferenceCount},
			{"edge_task_references", impact.EdgeTaskReferenceCount},
			{"active_current_nodes", impact.ActiveCurrentNodeCount},
			{"pending_approvals", impact.PendingApprovalCount},
			{"start_node_changes", impact.StartNodeChangeCount},
			{"last_terminal_changes", impact.LastTerminalChangeCount},
			{"task_referenced_node_kind_changes", impact.TaskReferencedNodeKindChangeCount},
		} {
			if err := write("- %s: %d\n", count.name, count.value); err != nil {
				return err
			}
		}
		if err := writeWorkflowGraphEntityReferences(stderr, "Removed entities", impact.RemovedEntities); err != nil {
			return err
		}
	}
	if len(outcome.Blockers) > 0 {
		if err := write("Blockers:\n"); err != nil {
			return err
		}
		for _, blocker := range outcome.Blockers {
			if err := write("- [%s] %s (count=%d)\n", blocker.Code, blocker.Message, blocker.Count); err != nil {
				return err
			}
			if err := writeWorkflowGraphEntityReferences(stderr, "  Affected entities", blocker.AffectedEntities); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeWorkflowGraphEntityReferences(stderr io.Writer, label string, entities []serverapi.WorkflowGraphEntityReference) error {
	write := func(format string, args ...any) error {
		_, err := fmt.Fprintf(stderr, format, args...)
		return err
	}
	if err := write("%s:\n", label); err != nil {
		return err
	}
	for _, entity := range entities {
		if err := write("  - %s %s\n", entity.EntityType, entity.EntityID); err != nil {
			return err
		}
	}
	return nil
}

func (outcome workflowGraphApplyOutcome) Validate() error {
	valid := false
	switch outcome.Outcome {
	case workflowGraphApplySaved:
		valid = outcome.WorkflowID != nil && outcome.CurrentVersion != nil && outcome.Impact != nil &&
			outcome.Definition != nil && len(outcome.Blockers) == 0 && outcome.Message == nil
	case workflowGraphApplyUnchanged:
		valid = outcome.WorkflowID != nil && outcome.CurrentVersion != nil && outcome.Impact != nil &&
			outcome.Definition == nil && len(outcome.Blockers) == 0 && outcome.Message == nil
	case workflowGraphApplyConfirmationRequired:
		valid = outcome.WorkflowID != nil && outcome.CurrentVersion != nil && outcome.Impact != nil &&
			len(outcome.Blockers) > 0 && !workflowGraphApplyHasBlocker(outcome.Blockers, false) &&
			outcome.Definition == nil && outcome.Message == nil
	case workflowGraphApplyBlocked:
		valid = outcome.WorkflowID != nil && outcome.CurrentVersion != nil && len(outcome.Blockers) > 0 &&
			outcome.Definition == nil && outcome.Message == nil
	case workflowGraphApplyInvalidDocument:
		knownWorkflow := outcome.WorkflowID != nil && outcome.CurrentVersion != nil
		unknownWorkflow := outcome.WorkflowID == nil && outcome.CurrentVersion == nil
		valid = outcome.Message != nil && (knownWorkflow || unknownWorkflow) &&
			outcome.ValidationResults == nil && outcome.Impact == nil &&
			len(outcome.Blockers) == 0 && outcome.Definition == nil
	case workflowGraphApplyRequestFailed:
		valid = outcome.Message != nil && outcome.ValidationResults == nil && outcome.Impact == nil &&
			len(outcome.Blockers) == 0 && outcome.Definition == nil
	}
	if !valid {
		return fmt.Errorf("Workflow graph apply %q outcome is invalid", outcome.Outcome)
	}
	if outcome.CurrentVersion != nil && *outcome.CurrentVersion < 0 {
		return errors.New("Workflow graph apply current version must be non-negative")
	}
	if outcome.Message != nil && strings.TrimSpace(*outcome.Message) == "" {
		return errors.New("Workflow graph apply message must not be blank")
	}
	return nil
}
