package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"core/shared/apicontract"
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
	_, remote, err := openBindingCommandRemote(context.Background(), ".")
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
	remote apicontract.WorkflowService,
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
	outcome := saveWorkflowGraphApply(ctx, remote, current, graph, nil)
	if !confirmed || outcome.Outcome != workflowGraphApplyConfirmationRequired {
		return outcome
	}
	return saveWorkflowGraphApply(
		ctx,
		remote,
		current,
		graph,
		workflowGraphSaveConfirmationFromImpact(*outcome.Impact),
	)
}

func validateWorkflowGraphAdditionIdentities(
	current serverapi.WorkflowDefinition,
	submitted serverapi.WorkflowGraphDraft,
) error {
	currentTypes := make(map[string]map[serverapi.WorkflowGraphEntityType]bool)
	indexCurrent := func(entityType serverapi.WorkflowGraphEntityType, ids []string) {
		for _, id := range ids {
			if currentTypes[id] == nil {
				currentTypes[id] = make(map[serverapi.WorkflowGraphEntityType]bool)
			}
			currentTypes[id][entityType] = true
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
			if currentTypes[id][collection.entityType] {
				continue
			}
			if len(currentTypes[id]) != 0 {
				return fmt.Errorf("%s[%d].id %q matches a current entity of another type", collection.name, index, id)
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
	remote apicontract.WorkflowService,
	current serverapi.WorkflowDefinition,
	graph serverapi.WorkflowGraphDraft,
	confirmation *serverapi.WorkflowGraphSaveConfirmation,
) workflowGraphApplyOutcome {
	workflowID := workflowGraphApplyPointer(current.Workflow.ID)
	response, err := remote.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      current.Workflow.ID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           graph,
		Confirmation:    confirmation,
	})
	if err != nil {
		return workflowGraphApplyFailure(
			workflowGraphApplyRequestFailed,
			workflowID,
			workflowGraphApplyPointer(current.Workflow.Version),
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
		if response.ConfirmationRequired && workflowGraphApplyHasBlocker(response.Blockers, true) &&
			!workflowGraphApplyHasBlocker(response.Blockers, false) {
			outcome.Outcome = workflowGraphApplyConfirmationRequired
			return outcome
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
		_, err := fmt.Fprintf(stdout, "Workflow graph saved at version %d.\n", *outcome.CurrentVersion)
		return err
	case workflowGraphApplyUnchanged:
		_, err := fmt.Fprintln(stdout, "Workflow was already in the requested state, no changes applied")
		return err
	case workflowGraphApplyConfirmationRequired:
		if _, err := fmt.Fprintln(stderr, "Workflow has destructive changes pending. Rerun the command with --confirm to apply anyway"); err != nil {
			return err
		}
		return writeWorkflowGraphApplyDetails(stderr, outcome)
	case workflowGraphApplyBlocked:
		if _, err := fmt.Fprintf(stderr, "Workflow graph apply was blocked: %s\n", outcome.Blockers[0].Message); err != nil {
			return err
		}
		return writeWorkflowGraphApplyDetails(stderr, outcome)
	case workflowGraphApplyInvalidDocument, workflowGraphApplyRequestFailed:
		_, err := fmt.Fprintln(stderr, *outcome.Message)
		return err
	}
	panic(fmt.Sprintf("Workflow graph apply outcome %q passed validation without a human projection", outcome.Outcome))
}

func writeWorkflowGraphApplyDetails(stderr io.Writer, outcome workflowGraphApplyOutcome) error {
	var writeErr error
	write := func(format string, args ...any) {
		if writeErr == nil {
			_, writeErr = fmt.Fprintf(stderr, format, args...)
		}
	}
	writeEntities := func(label string, entities []serverapi.WorkflowGraphEntityReference) {
		if writeErr == nil {
			writeErr = writeWorkflowGraphEntityReferences(stderr, label, entities)
		}
	}
	if len(outcome.ValidationResults) > 0 {
		write("Validation:\n")
		for _, mode := range slices.Sorted(maps.Keys(outcome.ValidationResults)) {
			result := outcome.ValidationResults[mode]
			write("- %s: valid=%t\n", mode, result.Valid)
			for _, validationError := range result.Errors {
				write("  - [%s] %s\n", validationError.Code, validationError.Message)
				identities := []struct{ name, value string }{
					{"node", validationError.NodeID},
					{"transition_group", validationError.TransitionGroupID},
					{"edge", validationError.EdgeID},
				}
				if validationError.WorkflowID != nil {
					identities = append([]struct{ name, value string }{{"workflow", validationError.WorkflowID.String()}}, identities...)
				}
				for _, identity := range identities {
					if identity.value != "" {
						write("    %s: %s\n", identity.name, identity.value)
					}
				}
				for _, relatedID := range validationError.RelatedIDs {
					write("    related: %s\n", relatedID)
				}
				if details := validationError.Details; details != nil {
					encoded, err := json.Marshal(details)
					if err != nil {
						return err
					}
					write("    details: %s\n", encoded)
				}
			}
		}
	}
	if outcome.Outcome == workflowGraphApplyConfirmationRequired && outcome.Impact != nil {
		write("Impact:\n")
		for _, count := range []struct {
			name  string
			value int64
		}{
			{"removed_node_groups", outcome.Impact.RemovedNodeGroupCount},
			{"removed_nodes", outcome.Impact.RemovedNodeCount},
			{"removed_transition_groups", outcome.Impact.RemovedTransitionGroupCount},
			{"removed_edges", outcome.Impact.RemovedEdgeCount},
			{"node_task_references", outcome.Impact.NodeTaskReferenceCount},
			{"edge_task_references", outcome.Impact.EdgeTaskReferenceCount},
			{"active_current_nodes", outcome.Impact.ActiveCurrentNodeCount},
			{"pending_approvals", outcome.Impact.PendingApprovalCount},
			{"start_node_changes", outcome.Impact.StartNodeChangeCount},
			{"last_terminal_changes", outcome.Impact.LastTerminalChangeCount},
			{"task_referenced_node_kind_changes", outcome.Impact.TaskReferencedNodeKindChangeCount},
		} {
			write("- %s: %d\n", count.name, count.value)
		}
		writeEntities("Removed entities", outcome.Impact.RemovedEntities)
	}
	if len(outcome.Blockers) > 0 {
		write("Blockers:\n")
		for _, blocker := range outcome.Blockers {
			write("- [%s] %s (count=%d)\n", blocker.Code, blocker.Message, blocker.Count)
			writeEntities("  Affected entities", blocker.AffectedEntities)
		}
	}
	return writeErr
}
func writeWorkflowGraphEntityReferences(stderr io.Writer, label string, entities []serverapi.WorkflowGraphEntityReference) error {
	if _, err := fmt.Fprintf(stderr, "%s:\n", label); err != nil {
		return err
	}
	for _, entity := range entities {
		if _, err := fmt.Fprintf(stderr, "  - %s %s\n", entity.EntityType, entity.EntityID); err != nil {
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
	if outcome.Message != nil && strings.TrimSpace(*outcome.Message) == "" {
		return errors.New("Workflow graph apply message must not be blank")
	}
	return nil
}
