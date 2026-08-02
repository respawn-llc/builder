package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"core/shared/config"
	"core/shared/labelcontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowProjectLabelCatalogSnapshot struct {
	ProjectID          string
	LabelsByID         map[string]serverapi.WorkflowProjectLabel
	LabelsByFoldedName map[string]serverapi.WorkflowProjectLabel
}

const taskLabelMovePlainAcknowledgement = "ok"

type taskLabelMovePlacementKind uint8

const (
	taskLabelMovePlacementFirst taskLabelMovePlacementKind = iota
	taskLabelMovePlacementLast
	taskLabelMovePlacementBefore
	taskLabelMovePlacementAfter
)

type taskLabelMovePlacement struct {
	Kind             taskLabelMovePlacementKind
	RelativeSelector *string
	RelativeID       *string
}

type taskLabelAssignmentOperation uint8

const (
	taskLabelAssignmentOperationAdd taskLabelAssignmentOperation = iota
	taskLabelAssignmentOperationRemove
)

func taskLabelSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return dispatchCommandGroup(args, stdout, stderr, commandGroup{
		path:  "task label",
		usage: taskLabelUsage,
		routes: map[string]commandHandler{
			"add":    taskLabelAddSubcommand,
			"create": taskLabelCreateSubcommand,
			"delete": taskLabelDeleteSubcommand,
			"list":   taskLabelListSubcommand,
			"move":   taskLabelMoveSubcommand,
			"remove": taskLabelRemoveSubcommand,
			"rename": taskLabelRenameSubcommand,
		},
	})
}

func taskLabelMoveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task label move", stderr, taskLabelMoveUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path")
	selector := fs.String("label", "", "label name or canonical UUIDv4")
	first := fs.Bool("first", false, "move the label to the first position")
	last := fs.Bool("last", false, "move the label to the last position")
	before := fs.String("before", "", "place the label immediately before another label")
	after := fs.String("after", "", "place the label immediately after another label")
	jsonOut := fs.Bool("json", false, "write the resulting Project label catalog as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "task label move does not accept positional arguments")
		return 2
	}
	if !flagExplicit(fs, "label") {
		fmt.Fprintln(stderr, "task label move requires --label <name-or-uuid>")
		return 2
	}
	placementCount := 0
	var placement taskLabelMovePlacement
	if *first {
		placementCount++
		placement = taskLabelMovePlacement{Kind: taskLabelMovePlacementFirst}
	}
	if *last {
		placementCount++
		placement = taskLabelMovePlacement{Kind: taskLabelMovePlacementLast}
	}
	if flagExplicit(fs, "before") {
		placementCount++
		relativeSelector := *before
		placement = taskLabelMovePlacement{Kind: taskLabelMovePlacementBefore, RelativeSelector: &relativeSelector}
	}
	if flagExplicit(fs, "after") {
		placementCount++
		relativeSelector := *after
		placement = taskLabelMovePlacement{Kind: taskLabelMovePlacementAfter, RelativeSelector: &relativeSelector}
	}
	if placementCount == 0 {
		fmt.Fprintln(stderr, "task label move requires exactly one of --first, --last, --before, or --after")
		return 2
	}
	if placementCount > 1 {
		fmt.Fprintln(stderr, "task label move accepts exactly one of --first, --last, --before, or --after")
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		catalog, snapshot, err := loadWorkflowProjectLabelCatalog(context.Background(), remote, projectID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		groups := [][]string{{*selector}}
		if placement.Kind == taskLabelMovePlacementBefore || placement.Kind == taskLabelMovePlacementAfter {
			groups = append(groups, []string{*placement.RelativeSelector})
		}
		resolved, err := resolveWorkflowProjectLabelSelectorGroups(snapshot, groups)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(resolved) > 1 {
			relativeID := resolved[1].IDs[0]
			placement.RelativeID = &relativeID
		}
		labelIDs := workflowProjectLabelMovePermutation(catalog.Catalog.Labels, resolved[0].IDs[0], placement)
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		response, err := remote.ReorderWorkflowProjectLabels(ctx, serverapi.WorkflowProjectLabelReorderRequest{
			ProjectID: projectID,
			LabelIDs:  labelIDs,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if _, err := workflowProjectLabelCatalogSnapshotFromResponse(response.Catalog, projectID); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		responseIDs := make([]string, len(response.Catalog.Labels))
		for index, record := range response.Catalog.Labels {
			responseIDs[index] = record.ID
		}
		if !slices.Equal(responseIDs, labelIDs) {
			fmt.Fprintln(stderr, "Project label reorder response does not match the requested order")
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, response)
		}
		fmt.Fprintln(stdout, taskLabelMovePlainAcknowledgement)
		return 0
	})
}

func workflowProjectLabelMovePermutation(labels []serverapi.WorkflowProjectLabel, movedID string, placement taskLabelMovePlacement) []string {
	original := make([]string, len(labels))
	for index, label := range labels {
		original[index] = label.ID
	}
	if placement.RelativeID != nil {
		if *placement.RelativeID == movedID {
			return original
		}
	}
	remaining := make([]string, 0, len(original)-1)
	for _, id := range original {
		if id != movedID {
			remaining = append(remaining, id)
		}
	}
	position := len(remaining)
	switch placement.Kind {
	case taskLabelMovePlacementFirst:
		position = 0
	case taskLabelMovePlacementBefore:
		for index, id := range remaining {
			if placement.RelativeID != nil && id == *placement.RelativeID {
				position = index
				break
			}
		}
	case taskLabelMovePlacementAfter:
		for index, id := range remaining {
			if placement.RelativeID != nil && id == *placement.RelativeID {
				position = index + 1
				break
			}
		}
	}
	result := make([]string, 0, len(original))
	result = append(result, remaining[:position]...)
	result = append(result, movedID)
	result = append(result, remaining[position:]...)
	return result
}

func taskLabelAddSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return taskLabelAssignmentSubcommand(args, stdout, stderr, taskLabelAssignmentOperationAdd)
}

func taskLabelRemoveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return taskLabelAssignmentSubcommand(args, stdout, stderr, taskLabelAssignmentOperationRemove)
}

func taskLabelAssignmentSubcommand(args []string, stdout io.Writer, stderr io.Writer, operation taskLabelAssignmentOperation) int {
	usage := taskLabelAddUsage
	commandName := "add"
	if operation == taskLabelAssignmentOperationRemove {
		usage = taskLabelRemoveUsage
		commandName = "remove"
	}
	fs := newCommandFlagSet(config.Command+" task label "+commandName, stderr, usage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	var selectors repeatedStringFlag
	fs.Var(&selectors, "label", "label name or canonical UUIDv4; repeat for multiple labels")
	jsonOut := fs.Bool("json", false, "write the authoritative task label assignment as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "task label "+commandName+" requires <short-id-or-task-id>")
	if !ok {
		return exitCode
	}
	if len(selectors) == 0 {
		fmt.Fprintln(stderr, "task label "+commandName+" requires at least one --label <name-or-uuid>")
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		task, err := resolveWorkflowTask(context.Background(), cfg, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if strings.TrimSpace(task.Summary.ProjectID) == "" {
			fmt.Fprintln(stderr, "resolved task is missing project_id")
			return 1
		}
		_, snapshot, err := loadWorkflowProjectLabelCatalog(context.Background(), remote, task.Summary.ProjectID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		labelIDs, err := resolveWorkflowProjectLabelSelectors(snapshot, selectors)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		request := serverapi.WorkflowTaskLabelsUpdateRequest{TaskID: task.Summary.ID}
		switch operation {
		case taskLabelAssignmentOperationAdd:
			request.AddLabelIDs = labelIDs
		case taskLabelAssignmentOperationRemove:
			request.RemoveLabelIDs = labelIDs
		default:
			fmt.Fprintln(stderr, "invalid task label assignment operation")
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		response, err := remote.UpdateWorkflowTaskLabels(ctx, request)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := response.Validate(); err != nil {
			fmt.Fprintf(stderr, "invalid task label assignment response: %v\n", err)
			return 1
		}
		if response.Assignment.TaskID != task.Summary.ID {
			fmt.Fprintf(stderr, "task label assignment response task %q does not match resolved task %q\n", response.Assignment.TaskID, task.Summary.ID)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, response)
		}
		fmt.Fprintf(stdout, "Updated labels for task %s.\n", taskDisplayID(task))
		return 0
	})
}

func taskLabelCreateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task label create", stderr, taskLabelCreateUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path")
	jsonOut := fs.Bool("json", false, "write the created Project label as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "task label create requires <name>")
	if !ok {
		return exitCode
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		response, err := remote.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
			ProjectID: projectID,
			Name:      positionals[0],
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, response)
		}
		fmt.Fprintf(stdout, "Created label %q (%s).\n", response.Label.Name, response.Label.ID)
		return 0
	})
}

func taskLabelListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task label list", stderr, taskLabelListUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path")
	name := fs.String("name", "", "label name to match")
	jsonOut := fs.Bool("json", false, "write the Project label catalog as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "task label list does not accept positional arguments")
		return 2
	}
	nameProvided := flagExplicit(fs, "name")
	if nameProvided && strings.TrimSpace(*name) == "" {
		fmt.Fprintln(stderr, "task label list --name requires a non-blank value")
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		catalog, snapshot, err := loadWorkflowProjectLabelCatalog(context.Background(), remote, projectID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if nameProvided {
			catalog.Catalog.Labels = []serverapi.WorkflowProjectLabel{}
			if record, found := resolveWorkflowProjectLabelName(snapshot, *name); found {
				catalog.Catalog.Labels = append(catalog.Catalog.Labels, record)
			}
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, catalog)
		}
		for _, record := range catalog.Catalog.Labels {
			fmt.Fprintf(stdout, "%q (%s)\n", record.Name, record.ID)
		}
		return 0
	})
}

func taskLabelRenameSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task label rename", stderr, taskLabelRenameUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path")
	selector := fs.String("label", "", "label name or canonical UUIDv4")
	jsonOut := fs.Bool("json", false, "write the renamed Project label as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "task label rename requires <new-name>")
	if !ok {
		return exitCode
	}
	if !flagExplicit(fs, "label") {
		fmt.Fprintln(stderr, "task label rename requires --label <name-or-uuid>")
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_, snapshot, err := loadWorkflowProjectLabelCatalog(context.Background(), remote, projectID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		resolvedIDs, err := resolveWorkflowProjectLabelSelectors(snapshot, []string{*selector})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		labelID := resolvedIDs[0]
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		response, err := remote.RenameWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelRenameRequest{
			ProjectID: projectID,
			LabelID:   labelID,
			Name:      positionals[0],
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := response.Label.Validate(); err != nil {
			fmt.Fprintf(stderr, "invalid Project label rename response: %v\n", err)
			return 1
		}
		if response.Label.ID != labelID {
			fmt.Fprintf(stderr, "Project label rename response ID %q does not match selected label %q\n", response.Label.ID, labelID)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, response)
		}
		fmt.Fprintf(stdout, "Renamed label %q (%s).\n", response.Label.Name, response.Label.ID)
		return 0
	})
}

func taskLabelDeleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task label delete", stderr, taskLabelDeleteUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path")
	selector := fs.String("label", "", "label name or canonical UUIDv4")
	jsonOut := fs.Bool("json", false, "write the deleted Project label ID as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "task label delete does not accept positional arguments")
		return 2
	}
	if !flagExplicit(fs, "label") {
		fmt.Fprintln(stderr, "task label delete requires --label <name-or-uuid>")
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_, snapshot, err := loadWorkflowProjectLabelCatalog(context.Background(), remote, projectID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		resolvedIDs, err := resolveWorkflowProjectLabelSelectors(snapshot, []string{*selector})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		labelID := resolvedIDs[0]
		selected := snapshot.LabelsByID[labelID]
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		response, err := remote.DeleteWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelDeleteRequest{
			ProjectID: projectID,
			LabelID:   labelID,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := response.Validate(); err != nil {
			fmt.Fprintf(stderr, "invalid Project label delete response: %v\n", err)
			return 1
		}
		if response.LabelID != labelID {
			fmt.Fprintf(stderr, "Project label delete response ID %q does not match selected label %q\n", response.LabelID, labelID)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, response)
		}
		fmt.Fprintf(stdout, "Deleted label %q (%s).\n", selected.Name, response.LabelID)
		return 0
	})
}

func loadWorkflowProjectLabelCatalog(ctx context.Context, remote workflowCommandRemote, projectID string) (serverapi.WorkflowProjectLabelCatalogResponse, workflowProjectLabelCatalogSnapshot, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	response, err := remote.ListWorkflowProjectLabels(rpcCtx, serverapi.WorkflowProjectLabelCatalogRequest{ProjectID: projectID})
	if err != nil {
		return serverapi.WorkflowProjectLabelCatalogResponse{}, workflowProjectLabelCatalogSnapshot{}, err
	}
	snapshot, err := workflowProjectLabelCatalogSnapshotFromResponse(response.Catalog, projectID)
	if err != nil {
		return serverapi.WorkflowProjectLabelCatalogResponse{}, workflowProjectLabelCatalogSnapshot{}, err
	}
	return response, snapshot, nil
}

func workflowProjectLabelCatalogSnapshotFromResponse(catalog serverapi.WorkflowProjectLabelCatalog, projectID string) (workflowProjectLabelCatalogSnapshot, error) {
	if err := catalog.Validate(); err != nil {
		return workflowProjectLabelCatalogSnapshot{}, fmt.Errorf("invalid Project label catalog response: %w", err)
	}
	if catalog.ProjectID != projectID {
		return workflowProjectLabelCatalogSnapshot{}, fmt.Errorf("Project label catalog response project %q does not match requested project %q", catalog.ProjectID, projectID)
	}
	snapshot := workflowProjectLabelCatalogSnapshot{
		ProjectID:          projectID,
		LabelsByID:         make(map[string]serverapi.WorkflowProjectLabel, len(catalog.Labels)),
		LabelsByFoldedName: make(map[string]serverapi.WorkflowProjectLabel, len(catalog.Labels)),
	}
	for _, record := range catalog.Labels {
		if _, exists := snapshot.LabelsByID[record.ID]; exists {
			return workflowProjectLabelCatalogSnapshot{}, fmt.Errorf("Project label catalog response contains duplicate label ID %q", record.ID)
		}
		foldedName := labelcontract.Fold(record.Name)
		if existing, exists := snapshot.LabelsByFoldedName[foldedName]; exists {
			return workflowProjectLabelCatalogSnapshot{}, fmt.Errorf("Project label catalog response contains duplicate folded label name %q for IDs %q and %q", foldedName, existing.ID, record.ID)
		}
		snapshot.LabelsByID[record.ID] = record
		snapshot.LabelsByFoldedName[foldedName] = record
	}
	return snapshot, nil
}

func resolveWorkflowProjectLabelSelectors(snapshot workflowProjectLabelCatalogSnapshot, rawSelectors []string) ([]string, error) {
	resolvedGroups, err := resolveWorkflowProjectLabelSelectorGroups(snapshot, [][]string{rawSelectors})
	if err != nil {
		return nil, err
	}
	return resolvedGroups[0].IDs, nil
}

func resolveWorkflowProjectLabelFilter(
	snapshot workflowProjectLabelCatalogSnapshot,
	mode serverapi.WorkflowTaskNamedLabelFilterMode,
	includedSelectors []string,
	excludedSelectors []string,
) (serverapi.WorkflowTaskLabelFilter, error) {
	resolvedGroups, err := resolveWorkflowProjectLabelSelectorGroups(snapshot, [][]string{includedSelectors, excludedSelectors})
	if err != nil {
		return serverapi.WorkflowTaskLabelFilter{}, err
	}
	if conflictID := sharedWorkflowProjectLabelSelectorGroups(resolvedGroups[0], resolvedGroups[1]); conflictID != nil {
		return serverapi.WorkflowTaskLabelFilter{}, conflictingWorkflowProjectLabelSelectorsError{
			Included: resolvedGroups[0].SelectorsByID[*conflictID],
			Excluded: resolvedGroups[1].SelectorsByID[*conflictID],
		}
	}
	filter := serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
		Named: &serverapi.WorkflowTaskNamedLabelFilter{
			Mode:             mode,
			LabelIDs:         resolvedGroups[0].IDs,
			ExcludedLabelIDs: resolvedGroups[1].IDs,
		},
	}
	if err := filter.Validate(); err != nil {
		return serverapi.WorkflowTaskLabelFilter{}, err
	}
	return filter, nil
}

type resolvedWorkflowProjectLabelSelectorGroup struct {
	IDs           []string
	SelectorsByID map[string]string
}

func resolveWorkflowProjectLabelSelectorGroups(
	snapshot workflowProjectLabelCatalogSnapshot,
	rawSelectorGroups [][]string,
) ([]resolvedWorkflowProjectLabelSelectorGroup, error) {
	resolvedGroups := make([]resolvedWorkflowProjectLabelSelectorGroup, len(rawSelectorGroups))
	unresolved := make([]string, 0)
	for groupIndex, rawSelectors := range rawSelectorGroups {
		resolvedIDs := make([]string, 0, len(rawSelectors))
		seenIDs := make(map[string]struct{}, len(rawSelectors))
		selectorsByID := make(map[string]string, len(rawSelectors))
		for _, raw := range rawSelectors {
			record, found := resolveWorkflowProjectLabelSelector(snapshot, raw)
			if !found {
				unresolved = append(unresolved, raw)
				continue
			}
			if _, exists := seenIDs[record.ID]; exists {
				continue
			}
			seenIDs[record.ID] = struct{}{}
			resolvedIDs = append(resolvedIDs, record.ID)
			selectorsByID[record.ID] = raw
		}
		resolvedGroups[groupIndex] = resolvedWorkflowProjectLabelSelectorGroup{
			IDs:           resolvedIDs,
			SelectorsByID: selectorsByID,
		}
	}
	if len(unresolved) > 0 {
		return nil, unresolvedWorkflowProjectLabelSelectorsError{Selectors: unresolved}
	}
	return resolvedGroups, nil
}

func sharedWorkflowProjectLabelSelectorGroups(
	included resolvedWorkflowProjectLabelSelectorGroup,
	excluded resolvedWorkflowProjectLabelSelectorGroup,
) *string {
	for _, labelID := range included.IDs {
		if _, exists := excluded.SelectorsByID[labelID]; exists {
			return &labelID
		}
	}
	return nil
}

type conflictingWorkflowProjectLabelSelectorsError struct {
	Included string
	Excluded string
}

func (err conflictingWorkflowProjectLabelSelectorsError) Error() string {
	return fmt.Sprintf(
		"--label %s conflicts with --not-label %s because both select the same Label",
		strconv.Quote(err.Included),
		strconv.Quote(err.Excluded),
	)
}

func workflowProjectLabelNames(snapshot workflowProjectLabelCatalogSnapshot, ids []string) ([]string, error) {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		record, found := snapshot.LabelsByID[id]
		if !found {
			return nil, fmt.Errorf("Project label catalog changed while rendering task labels; retry the command")
		}
		names = append(names, record.Name)
	}
	return names, nil
}

func resolveWorkflowProjectLabelSelector(snapshot workflowProjectLabelCatalogSnapshot, raw string) (serverapi.WorkflowProjectLabel, bool) {
	if _, err := runtimeids.ParseCanonicalUUIDv4(raw, "label selector"); err == nil {
		record, found := snapshot.LabelsByID[raw]
		return record, found
	}
	return resolveWorkflowProjectLabelName(snapshot, raw)
}

func resolveWorkflowProjectLabelName(snapshot workflowProjectLabelCatalogSnapshot, raw string) (serverapi.WorkflowProjectLabel, bool) {
	record, found := snapshot.LabelsByFoldedName[labelcontract.Fold(strings.TrimSpace(raw))]
	return record, found
}

type unresolvedWorkflowProjectLabelSelectorsError struct {
	Selectors []string
}

func (err unresolvedWorkflowProjectLabelSelectorsError) Error() string {
	quoted := make([]string, len(err.Selectors))
	for index, selector := range err.Selectors {
		quoted[index] = strconv.Quote(selector)
	}
	return "unresolved label selectors: " + strings.Join(quoted, ", ")
}
