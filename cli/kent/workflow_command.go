package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/workflowkey"

	"github.com/google/uuid"
)

const (
	workflowCommandTimeout           = time.Minute
	workflowCommandWorkflowListLimit = serverapi.WorkflowPaginationMaxLimit
)

// workflowListOutput is the machine-readable shape of `workflow list --json`.
type workflowListOutput struct {
	Workflows  []serverapi.WorkflowRecord `json:"workflows"`
	ProjectID  *string                    `json:"project_id,omitempty"`
	NextOffset *int                       `json:"next_offset,omitempty"`
}

// workflowNodeOutput is the machine-readable shape of `workflow node add/update --json`.
type workflowNodeOutput struct {
	WorkflowID runtimeids.WorkflowID `json:"workflow_id"`
	NodeID     string                `json:"node_id"`
	Key        string                `json:"key"`
	Kind       string                `json:"kind,omitempty"`
	ScriptPath *string               `json:"script_path,omitempty"`
	Version    int64                 `json:"version"`
}

// workflowEdgeOutput is the machine-readable shape of `workflow edge add/update --json`.
type workflowEdgeOutput struct {
	WorkflowID        runtimeids.WorkflowID `json:"workflow_id"`
	EdgeID            string                `json:"edge_id"`
	TransitionGroupID string                `json:"transition_group_id"`
	Key               string                `json:"key,omitempty"`
	TransitionID      string                `json:"transition_id,omitempty"`
	Version           int64                 `json:"version"`
}

func runWorkflowCommandSession(stderr io.Writer, run func(config.App, *client.Remote) int) int {
	cfg, remote, err := openBindingCommandRemote(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() {
		if err := remote.Close(); err != nil {
			fmt.Fprintf(stderr, "close workflow command session: %v\n", err)
		}
	}()
	return run(cfg, remote)
}

func workflowSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return workflowSubcommandWithInput(args, os.Stdin, stdout, stderr)
}

func workflowSubcommandWithInput(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs := newCommandFlagSet(config.Command+" workflow", stderr, workflowUsage)
		fs.Usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "create":
		return workflowCreateSubcommand(args[1:], stdout, stderr)
	case "delete":
		return workflowDeleteSubcommand(args[1:], stdout, stderr)
	case "update":
		return workflowUpdateSubcommand(args[1:], stdout, stderr)
	case "list":
		return workflowListSubcommand(args[1:], stdout, stderr)
	case "graph":
		return workflowGraphSubcommand(args[1:], stdin, stdout, stderr)
	case "node":
		return workflowNodeSubcommand(args[1:], stdout, stderr)
	case "edge":
		return workflowEdgeSubcommand(args[1:], stdout, stderr)
	case "link":
		return workflowLinkSubcommand(args[1:], stdout, stderr)
	case "unlink":
		return workflowUnlinkSubcommand(args[1:], stdout, stderr)
	case "default":
		return workflowDefaultSubcommand(args[1:], stdout, stderr)
	case "validate":
		return workflowValidateSubcommand(args[1:], stdout, stderr)
	case "inspect":
		return workflowInspectSubcommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown workflow command: %s\n\n", args[0])
		fs := newCommandFlagSet(config.Command+" workflow", stderr, workflowUsage)
		workflowUsage.write(fs)
		return 2
	}
}

func workflowGraphSubcommand(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	return dispatchCommandGroup(args, stdout, stderr, commandGroup{
		path:  "workflow graph",
		usage: workflowGraphUsage,
		routes: map[string]commandHandler{
			"inspect": workflowGraphInspectSubcommand,
			"apply": func(args []string, stdout io.Writer, stderr io.Writer) int {
				return workflowGraphApplySubcommand(args, stdin, stdout, stderr)
			},
		},
	})
}

func workflowGraphInspectSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow graph inspect", stderr, workflowGraphInspectUsage)
	_ = fs.Bool("json", false, "write the graph document as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow graph inspect requires <uuid>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		definition, err := resolveWorkflowDefinition(context.Background(), remote, selector)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		document, err := workflowGraphDocumentFromDefinition(definition)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeCommandJSON(stdout, stderr, document)
	})
}

func workflowUpdateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow update", stderr, workflowUsage)
	name := fs.String("name", "", "replace the workflow name")
	description := fs.String("description", "", "replace the workflow description; pass an empty value to clear")
	executionTargetRaw := fs.String("execution-target", "", "workflow execution target: ask-on-first-execution, "+executionTargetSelectorHelp)
	jsonOut := fs.Bool("json", false, "write the updated workflow as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow update requires <uuid>")
	if !ok {
		return exitCode
	}
	if !flagExplicit(fs, "name") && !flagExplicit(fs, "description") && !flagExplicit(fs, "execution-target") {
		fmt.Fprintln(stderr, "workflow update requires at least one of --name, --description, or --execution-target")
		return 2
	}
	var targetPolicy *serverapi.WorkflowExecutionTargetConfiguration
	if flagExplicit(fs, "execution-target") {
		parsed, err := parseWorkflowExecutionTargetPolicySelector(*executionTargetRaw)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		targetPolicy = &parsed
	}
	selector, err := parseWorkflowSelector(positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		def, err := resolveWorkflowDefinition(context.Background(), remote, selector)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		metadata := serverapi.WorkflowGraphMetadata{
			Name:                  def.Workflow.Name,
			Description:           def.Workflow.Description,
			ExecutionTargetPolicy: targetPolicy,
		}
		if flagExplicit(fs, "name") {
			metadata.Name = strings.TrimSpace(*name)
		}
		if flagExplicit(fs, "description") {
			metadata.Description = strings.TrimSpace(*description)
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		resp, err := remote.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
			WorkflowID:      def.Workflow.ID,
			ExpectedVersion: def.Workflow.Version,
			Metadata:        &metadata,
			Graph:           serverapi.WorkflowGraphDraftFromDefinition(def),
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !resp.Saved || resp.Definition == nil {
			fmt.Fprintf(stderr, "workflow update was not saved at version %d\n", resp.CurrentVersion)
			for _, blocker := range resp.Blockers {
				fmt.Fprintf(stderr, "- [%s] %s\n", blocker.Code, blocker.Message)
			}
			return 1
		}
		projected, err := workflowRecordForCLI(resp.Definition.Workflow)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, projected)
		}
		fmt.Fprintf(stdout, "Updated workflow %q (%s) to version %d.\n", projected.Name, projected.ID, projected.Version)
		return 0
	})
}

func workflowCreateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow create", stderr, workflowCreateUsage)
	description := fs.String("description", "", "human-readable workflow description")
	jsonOut := fs.Bool("json", false, "write the created workflow as JSON")
	positionals, ok, exitCode := parseInterspersedPositionals(fs, args)
	if !ok {
		return exitCode
	}
	name := strings.TrimSpace(strings.Join(positionals, " "))
	if name == "" {
		fmt.Fprintln(stderr, "workflow create requires <name>")
		return 2
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		resp, err := remote.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: name, Description: *description})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		projected, err := workflowRecordForCLI(resp.Workflow)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, projected)
		}
		fmt.Fprintf(stdout, "Created workflow %q (%s).\n", projected.Name, projected.ID)
		return 0
	})
}

func workflowListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow list", stderr, workflowListUsage)
	offset := fs.Int("offset", 0, "zero-based workflow offset")
	limit := fs.Int("limit", workflowCommandWorkflowListLimit, "maximum number of workflows to return")
	project := fs.String("project", "", "project path or ID to list linked workflows for")
	jsonOut := fs.Bool("json", false, "write workflows and the next offset as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "workflow list does not accept positional arguments")
		return 2
	}
	projectProvided := flagExplicit(fs, "project")
	if projectProvided && strings.TrimSpace(*project) == "" {
		fmt.Fprintln(stderr, "workflow list --project requires a non-blank value")
		return 2
	}
	if err := validateWorkflowPagination(*offset, *limit); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		var projectID *string
		if projectProvided {
			resolved, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *project)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			projectID = &resolved
		}
		response, err := listWorkflowPage(context.Background(), remote, serverapi.WorkflowListRequest{Offset: offset, Limit: limit, ProjectID: projectID})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeWorkflowListResponse(
			stdout,
			stderr,
			response,
			workflowListExpectedScope{ProjectID: projectID},
			*jsonOut,
		)
	})
}

func writeWorkflowListResponse(
	stdout io.Writer,
	stderr io.Writer,
	response serverapi.WorkflowListResponse,
	expected workflowListExpectedScope,
	jsonOut bool,
) int {
	workflows, err := workflowRecordsForCLI(response.Workflows)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := validateWorkflowListProjectMetadata(expected, response.ProjectID, workflows); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOut {
		return writeCommandJSON(stdout, stderr, workflowListOutput{
			Workflows:  workflows,
			ProjectID:  response.ProjectID,
			NextOffset: response.NextOffset,
		})
	}
	for _, workflow := range workflows {
		if response.ProjectID != nil {
			fmt.Fprintf(stdout, "%s: %s (v%d; %s; target %s)\n", workflow.ID, workflow.Name, workflow.Version, workflowProjectLinkState(workflow.ProjectLink), workflowExecutionTargetPolicySelector(workflow.ExecutionTargetPolicy))
			continue
		}
		fmt.Fprintf(stdout, "%s: %s (v%d)\n", workflow.ID, workflow.Name, workflow.Version)
	}
	if response.NextOffset != nil {
		if err := writeNextOffset(stderr, *response.NextOffset); err != nil {
			return 1
		}
	}
	return 0
}

func validateWorkflowPagination(offset int, limit int) error {
	_, err := serverapi.ResolveWorkflowOffsetWindow(&offset, &limit)
	return err
}

type workflowListExpectedScope struct {
	ProjectID *string
}

func validateWorkflowListProjectMetadata(expected workflowListExpectedScope, responseProjectID *string, workflows []serverapi.WorkflowRecord) error {
	if expected.ProjectID == nil {
		if responseProjectID != nil {
			return fmt.Errorf("global workflow list response unexpectedly contains project_id %q", *responseProjectID)
		}
		for _, workflow := range workflows {
			if workflow.ProjectLink != nil {
				return fmt.Errorf("global workflow list response workflow %q contains project_link metadata", workflow.ID)
			}
		}
		return nil
	}
	if responseProjectID == nil || strings.TrimSpace(*responseProjectID) == "" {
		return errors.New("project workflow list response is missing project_id")
	}
	if *responseProjectID != *expected.ProjectID {
		return fmt.Errorf("project workflow list response project %q does not match requested project %q", *responseProjectID, *expected.ProjectID)
	}
	for _, workflow := range workflows {
		if workflow.ProjectLink == nil {
			return fmt.Errorf("project workflow list response workflow %q is missing project_link metadata", workflow.ID)
		}
	}
	return nil
}

func workflowProjectLinkState(link *serverapi.WorkflowListProjectLink) string {
	if link.Default {
		return "default"
	}
	return "linked"
}

func workflowNodeSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "add" {
		return workflowNodeAddSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "update" {
		return workflowNodeUpdateSubcommand(args[1:], stdout, stderr)
	}
	fs := newCommandFlagSet(config.Command+" workflow node", stderr, workflowNodeUsage)
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs.Usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	fmt.Fprintf(stderr, "unknown workflow node command: %s\n\n", args[0])
	fs.Usage()
	return 2
}

func workflowNodeAddSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow node add", stderr, workflowNodeAddUsage)
	key := fs.String("key", "", "stable node key used by transitions and task status")
	kind := fs.String("kind", "", "node kind: start|agent|script|join|terminal")
	displayName := fs.String("display-name", "", "name shown in task and workflow views; defaults from --key")
	agent := fs.String("agent", "", "subagent role assigned to an agent node")
	completionMode := fs.String("completion-mode", "", "agent completion contract: auto|structured_output|tool|shell_command|unstructured_output")
	scriptPath := fs.String("script-path", "", "server-side executable for a script node")
	jsonOut := fs.Bool("json", false, "write the added node as JSON")
	workflowRef, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow node add requires <uuid>")
	if !ok {
		return exitCode
	}
	if strings.TrimSpace(*key) == "" || strings.TrimSpace(*kind) == "" {
		fmt.Fprintln(stderr, "workflow node add requires --key and --kind")
		return 2
	}
	if *displayName == "" {
		*displayName = workflowDisplayNameFromKey(*key)
	}
	selector, err := parseWorkflowSelector(workflowRef[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	nodeID := uuid.NewString()
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		node := serverapi.WorkflowGraphDraftNode{
			ID:             nodeID,
			Key:            *key,
			Kind:           *kind,
			DisplayName:    *displayName,
			SubagentRole:   *agent,
			CompletionMode: *completionMode,
			ScriptPath:     workflowScriptPathFlagValue(fs, "script-path", *scriptPath),
		}
		added, result, err := runWorkflowGraphMutation(ctx, remote, selector, addWorkflowNodeDraftMutation(node))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, workflowNodeOutput{WorkflowID: selector, NodeID: added.ID, Key: added.Key, Kind: added.Kind, ScriptPath: added.ScriptPath, Version: result.Version})
		}
		fmt.Fprintf(stdout, "Added %s node `%s` (%s).\n", added.Kind, added.Key, added.ID)
		return 0
	})
}

func workflowNodeUpdateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow node update", stderr, workflowNodeUpdateUsage)
	key := fs.String("key", "", "new stable node key")
	kind := fs.String("kind", "", "node kind: start|agent|script|join|terminal")
	displayName := fs.String("display-name", "", "new name shown in task and workflow views")
	agent := fs.String("agent", "", "replace the assigned subagent role; pass an empty value to clear")
	completionMode := fs.String("completion-mode", "", "replace the agent completion contract: auto|structured_output|tool|shell_command|unstructured_output")
	scriptPath := fs.String("script-path", "", "replace the server-side executable; pass an empty value to clear")
	jsonOut := fs.Bool("json", false, "write the updated node as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow node update requires <uuid> <node-key>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		update := workflowNodeUpdateDraftMutation{NodeKey: positionals[1]}
		if strings.TrimSpace(*key) != "" {
			value := strings.TrimSpace(*key)
			update.Key = &value
		}
		if strings.TrimSpace(*kind) != "" {
			value := strings.TrimSpace(*kind)
			update.Kind = &value
		}
		if strings.TrimSpace(*displayName) != "" {
			value := strings.TrimSpace(*displayName)
			update.DisplayName = &value
		}
		if fs.Lookup("agent") != nil && flagExplicit(fs, "agent") {
			update.SubagentRole = workflowStringMutation{Set: true, Value: *agent}
		}
		if flagExplicit(fs, "completion-mode") {
			update.CompletionMode = workflowStringMutation{Set: true, Value: strings.TrimSpace(*completionMode)}
		}
		if flagExplicit(fs, "script-path") {
			update.ScriptPath = workflowOptionalStringMutation{
				Set:   true,
				Value: workflowScriptPathFlagValue(fs, "script-path", *scriptPath),
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		updated, result, err := runWorkflowGraphMutation(ctx, remote, selector, updateWorkflowNodeDraftMutation(update))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, workflowNodeOutput{WorkflowID: selector, NodeID: updated.ID, Key: updated.Key, Kind: updated.Kind, ScriptPath: updated.ScriptPath, Version: result.Version})
		}
		fmt.Fprintf(stdout, "Updated node `%s`.\n", updated.Key)
		return 0
	})
}

func workflowScriptPathFlagValue(fs *flag.FlagSet, name string, value string) *string {
	if !flagExplicit(fs, name) {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func workflowEdgeSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "add" {
		return workflowEdgeAddSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "update" {
		return workflowEdgeUpdateSubcommand(args[1:], stdout, stderr)
	}
	fs := newCommandFlagSet(config.Command+" workflow edge", stderr, workflowEdgeUsage)
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs.Usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	fmt.Fprintf(stderr, "unknown workflow edge command: %s\n\n", args[0])
	fs.Usage()
	return 2
}

func workflowEdgeAddSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow edge add", stderr, workflowEdgeAddUsage)
	fromKey := fs.String("from", "", "source node key")
	transitionID := fs.String("transition", "", "stable key for the source node's transition")
	edgeKey := fs.String("edge-key", "", "stable key for this transition branch")
	toKey := fs.String("to", "", "target node key")
	contextMode := fs.String("context", "", "target session policy: new_session|continue_session|compact_and_continue_session")
	contextSource := fs.String("context-source", "", "source session: immediate_source|previous_target|previous_target_or_new|node:<node-key>")
	requiresApproval := fs.Bool("requires-approval", false, "wait for user approval before starting the target")
	prompt := fs.String("prompt", "", "prompt template applied when the target is an agent node")
	transitionDescription := fs.String("transition-description", "", "guidance that tells an agent when to select this transition")
	assigneeSelection := fs.String("assignee-selection", "configured", "target assignee selection: configured|previous_node")
	thinkingSelection := fs.String("thinking-selection", "configured", "target thinking selection: configured|previous_node")
	targetAssigneeParam := fs.String("target-assignee-param", "", "protected target-assignee parameter as key=description")
	targetThinkingParam := fs.String("target-thinking-param", "", "protected target-thinking parameter as key=description")
	var params repeatedStringFlag
	fs.Var(&params, "param", "required transition value as key=description; repeatable")
	jsonOut := fs.Bool("json", false, "write the added branch as JSON")
	workflowRef, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow edge add requires <uuid>")
	if !ok {
		return exitCode
	}
	if strings.TrimSpace(*fromKey) == "" || strings.TrimSpace(*transitionID) == "" || strings.TrimSpace(*edgeKey) == "" || strings.TrimSpace(*toKey) == "" || strings.TrimSpace(*contextMode) == "" {
		fmt.Fprintln(stderr, "workflow edge add requires --from, --transition, --edge-key, --to, and --context")
		return 2
	}
	parsedContextSource, err := parseWorkflowContextSourceSelector(*contextSource)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	parsedParameters, err := parseWorkflowParameters(params)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	parsedAssigneeSelection, err := parseWorkflowSelectionMode("assignee-selection", *assigneeSelection)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	parsedThinkingSelection, err := parseWorkflowSelectionMode("thinking-selection", *thinkingSelection)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	parsedAssigneeParam, err := parseWorkflowProtectedParameterFlag(fs, "target-assignee-param", *targetAssigneeParam, "target_assignee")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	parsedThinkingParam, err := parseWorkflowProtectedParameterFlag(fs, "target-thinking-param", *targetThinkingParam, "target_thinking")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	parsedParameters, err = workflowEdgeParametersForAdd(parsedParameters, parsedAssigneeSelection, parsedThinkingSelection, parsedAssigneeParam, parsedThinkingParam)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	selector, err := parseWorkflowSelector(workflowRef[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	edgeID := uuid.NewString()
	newTransitionGroupID := uuid.NewString()
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		added, result, err := runWorkflowGraphMutation(ctx, remote, selector, addWorkflowEdgeDraftMutation(workflowEdgeAddDraftMutation{
			SourceNodeKey:        *fromKey,
			TransitionID:         *transitionID,
			NewTransitionGroupID: newTransitionGroupID,
			TransitionDescription: workflowStringMutation{
				Set:   flagExplicit(fs, "transition-description"),
				Value: strings.TrimSpace(*transitionDescription),
			},
			TargetNodeKey: *toKey,
			Edge: serverapi.WorkflowGraphDraftEdge{
				ID:                edgeID,
				Key:               *edgeKey,
				AssigneeSelection: parsedAssigneeSelection,
				ThinkingSelection: parsedThinkingSelection,
				ContextMode:       *contextMode,
				ContextSource:     parsedContextSource,
				RequiresApproval:  *requiresApproval,
				PromptTemplate:    *prompt,
				Parameters:        parsedParameters,
			},
		}))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, workflowEdgeOutput{WorkflowID: selector, EdgeID: added.Edge.ID, TransitionGroupID: added.Group.ID, Key: added.Edge.Key, TransitionID: added.Group.TransitionID, Version: result.Version})
		}
		fmt.Fprintf(stdout, "Added edge `%s` (%s) on transition `%s`: `%s` → `%s` (%s).\n", added.Edge.Key, added.Edge.ID, added.Group.TransitionID, *fromKey, *toKey, workflowEdgeContextDetail(added.Edge.ContextMode, added.Edge.RequiresApproval, added.Edge.ContextSource))
		return 0
	})
}

func workflowEdgeUpdateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow edge update", stderr, workflowEdgeUpdateUsage)
	transitionID := fs.String("transition", "", "new stable transition key")
	transitionDisplayName := fs.String("transition-display-name", "", "new human-readable transition name")
	transitionDescription := fs.String("transition-description", "", "replace the agent selection guidance; pass an empty value to clear")
	edgeKey := fs.String("edge-key", "", "new stable branch key")
	toKey := fs.String("to", "", "target node key")
	contextMode := fs.String("context", "", "target session policy: new_session|continue_session|compact_and_continue_session")
	contextSource := fs.String("context-source", "", "source session: immediate_source|previous_target|previous_target_or_new|node:<node-key>")
	requiresApproval := fs.Bool("requires-approval", false, "wait for user approval; pass false to disable")
	prompt := fs.String("prompt", "", "replace the target agent prompt; pass an empty value to clear")
	assigneeSelection := fs.String("assignee-selection", "", "target assignee selection: configured|previous_node")
	thinkingSelection := fs.String("thinking-selection", "", "target thinking selection: configured|previous_node")
	targetAssigneeParam := fs.String("target-assignee-param", "", "protected target-assignee parameter as key=description")
	targetThinkingParam := fs.String("target-thinking-param", "", "protected target-thinking parameter as key=description")
	var params repeatedStringFlag
	fs.Var(&params, "param", "required transition value as key=description; repeatable and replaces all existing values")
	clearParams := fs.Bool("clear-params", false, "remove all transition parameters")
	jsonOut := fs.Bool("json", false, "write the updated branch as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow edge update requires <uuid> <edge-id>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var parsedAssigneeSelection *string
	if flagExplicit(fs, "assignee-selection") {
		value, err := parseWorkflowSelectionMode("assignee-selection", *assigneeSelection)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		parsedAssigneeSelection = &value
	}
	var parsedThinkingSelection *string
	if flagExplicit(fs, "thinking-selection") {
		value, err := parseWorkflowSelectionMode("thinking-selection", *thinkingSelection)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		parsedThinkingSelection = &value
	}
	assigneeParam, err := parseWorkflowProtectedParameterFlag(fs, "target-assignee-param", *targetAssigneeParam, "target_assignee")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	thinkingParam, err := parseWorkflowProtectedParameterFlag(fs, "target-thinking-param", *targetThinkingParam, "target_thinking")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *clearParams && flagExplicit(fs, "param") {
		fmt.Fprintln(stderr, "use either --param or --clear-params, not both")
		return 2
	}
	var ordinaryParameters *[]serverapi.WorkflowParameter
	if *clearParams {
		values := []serverapi.WorkflowParameter{}
		ordinaryParameters = &values
	} else if flagExplicit(fs, "param") {
		values, err := parseWorkflowParameters(params)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		ordinaryParameters = &values
	}
	var parsedContextSource *serverapi.WorkflowContextSource
	if strings.TrimSpace(*contextSource) != "" {
		value, err := parseWorkflowContextSourceSelector(*contextSource)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		parsedContextSource = &value
	}
	update := workflowEdgeUpdateDraftMutation{
		EdgeID:                  positionals[1],
		AssigneeSelection:       parsedAssigneeSelection,
		ThinkingSelection:       parsedThinkingSelection,
		TargetAssigneeParameter: assigneeParam,
		TargetThinkingParameter: thinkingParam,
		OrdinaryParameters:      ordinaryParameters,
	}
	if strings.TrimSpace(*transitionID) != "" {
		value := strings.TrimSpace(*transitionID)
		update.TransitionID = &value
	}
	if strings.TrimSpace(*transitionDisplayName) != "" {
		value := strings.TrimSpace(*transitionDisplayName)
		update.TransitionDisplayName = &value
	}
	if flagExplicit(fs, "transition-description") {
		update.TransitionDescription = workflowStringMutation{Set: true, Value: strings.TrimSpace(*transitionDescription)}
	}
	if strings.TrimSpace(*edgeKey) != "" {
		value := strings.TrimSpace(*edgeKey)
		update.EdgeKey = &value
	}
	if strings.TrimSpace(*toKey) != "" {
		value := strings.TrimSpace(*toKey)
		update.TargetNodeKey = &value
	}
	if strings.TrimSpace(*contextMode) != "" {
		value := strings.TrimSpace(*contextMode)
		update.ContextMode = &value
	}
	update.ContextSource = parsedContextSource
	if flagExplicit(fs, "requires-approval") {
		update.RequiresApproval = requiresApproval
	}
	if flagExplicit(fs, "prompt") {
		update.PromptTemplate = workflowStringMutation{Set: true, Value: *prompt}
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		updated, result, err := runWorkflowGraphMutation(ctx, remote, selector, updateWorkflowEdgeDraftMutation(update))
		if err != nil {
			var usageErr workflowGraphMutationUsageError
			if errors.As(err, &usageErr) {
				fmt.Fprintln(stderr, usageErr)
				return 2
			}
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, workflowEdgeOutput{WorkflowID: selector, EdgeID: updated.Edge.ID, TransitionGroupID: updated.Group.ID, Key: updated.Edge.Key, TransitionID: updated.Group.TransitionID, Version: result.Version})
		}
		fmt.Fprintf(stdout, "Updated edge `%s`: `%s` → `%s` (%s).\n", updated.Edge.Key, updated.Group.TransitionID, updated.TargetNodeKey, workflowEdgeContextDetail(updated.Edge.ContextMode, updated.Edge.RequiresApproval, updated.Edge.ContextSource))
		return 0
	})
}

func workflowLinkSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow link", stderr, workflowLinkUsage)
	defaultLink := fs.Bool("default", false, "use this workflow when a project task omits --workflow")
	jsonOut := fs.Bool("json", false, "write the project-workflow link as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow link requires <project> and <uuid>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		workflowID := selector
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		defaultPolicy := serverapi.WorkflowProjectLinkDefaultNever
		if *defaultLink {
			defaultPolicy = serverapi.WorkflowProjectLinkDefaultAlways
		}
		resp, err := remote.LinkWorkflowToProject(ctx, serverapi.WorkflowLinkProjectRequest{
			ProjectID:     projectID,
			WorkflowID:    workflowID,
			DefaultPolicy: defaultPolicy,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			projected, projectionErr := projectWorkflowLinkForCLI(resp.Link)
			if projectionErr != nil {
				fmt.Fprintln(stderr, projectionErr)
				return 1
			}
			return writeCommandJSON(stdout, stderr, projected)
		}
		suffix := ""
		if resp.Link.Default {
			suffix = " as the default workflow"
		}
		fmt.Fprintf(stdout, "Linked workflow %s to project %s%s.\n", positionals[1], positionals[0], suffix)
		return 0
	})
}

func workflowUnlinkSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow unlink", stderr, workflowUnlinkUsage)
	jsonOut := fs.Bool("json", false, "write the unlink result and blockers as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow unlink requires <project> and <uuid>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		link, err := resolveWorkflowProjectLink(context.Background(), cfg, remote, remote, positionals[0], selector)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		resp, err := remote.UnlinkWorkflowFromProject(ctx, serverapi.WorkflowUnlinkProjectRequest{LinkID: link.ID})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !resp.Unlinked {
			if *jsonOut {
				writeCommandJSON(stdout, stderr, resp)
				return 1
			}
			writeWorkflowUnlinkBlockers(stderr, resp.Blockers)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, resp)
		}
		fmt.Fprintf(stdout, "Unlinked workflow %s from project %s.\n", positionals[1], positionals[0])
		return 0
	})
}

func workflowDefaultSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow default", stderr, workflowDefaultUsage)
	jsonOut := fs.Bool("json", false, "write the updated project-workflow link as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow default requires <project> and <uuid>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		workflowID := selector
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		resp, err := remote.SetDefaultProjectWorkflowLink(ctx, serverapi.WorkflowSetDefaultProjectLinkRequest{ProjectID: projectID, WorkflowID: workflowID})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			projected, projectionErr := projectWorkflowLinkForCLI(resp.Link)
			if projectionErr != nil {
				fmt.Fprintln(stderr, projectionErr)
				return 1
			}
			return writeCommandJSON(stdout, stderr, projected)
		}
		fmt.Fprintf(stdout, "Set workflow %s as the default for project %s.\n", positionals[1], positionals[0])
		return 0
	})
}

func workflowValidateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow validate", stderr, workflowValidateUsage)
	mode := fs.String("mode", string(serverapi.WorkflowValidationModeExecution), "validation context: draft|task_creation|execution")
	jsonOut := fs.Bool("json", false, "write validation diagnostics as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow validate requires <uuid>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		workflowID := selector
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		resp, err := remote.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{WorkflowID: workflowID, Mode: serverapi.WorkflowValidationMode(*mode)})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		resp, err = workflowValidationForCLI(resp)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			exit := writeCommandJSON(stdout, stderr, resp)
			if exit == 0 && !resp.Valid {
				return 1
			}
			return exit
		}
		if resp.Valid {
			if len(resp.Errors) == 0 {
				fmt.Fprintf(stdout, "Workflow %s is valid in %s mode.\n", selector.String(), *mode)
				return 0
			}
			fmt.Fprintf(stdout, "Workflow %s is valid in %s mode with %d diagnostic(s).\n", selector.String(), *mode, len(resp.Errors))
			for _, validationErr := range resp.Errors {
				writeWorkflowValidationError(stdout, validationErr)
			}
			return 0
		}
		fmt.Fprintf(stdout, "Workflow %s is invalid in %s mode: %d error(s).\n", selector.String(), *mode, len(resp.Errors))
		for _, validationErr := range resp.Errors {
			writeWorkflowValidationError(stdout, validationErr)
		}
		return 1
	})
}

func writeWorkflowValidationError(stdout io.Writer, err serverapi.WorkflowValidationError) {
	location := workflowValidationErrorLocation(err)
	if location != "" {
		fmt.Fprintf(stdout, "- [%s] %s (%s)\n", err.Code, err.Message, location)
		return
	}
	fmt.Fprintf(stdout, "- [%s] %s\n", err.Code, err.Message)
}

// workflowValidationErrorLocation names the graph element a validation error
// points at, preferring the most specific id present.
func workflowValidationErrorLocation(err serverapi.WorkflowValidationError) string {
	switch {
	case strings.TrimSpace(err.EdgeID) != "":
		return "edge " + strings.TrimSpace(err.EdgeID)
	case strings.TrimSpace(err.TransitionGroupID) != "":
		return "transition group " + strings.TrimSpace(err.TransitionGroupID)
	case strings.TrimSpace(err.NodeID) != "":
		return "node " + strings.TrimSpace(err.NodeID)
	default:
		return ""
	}
}

func workflowInspectSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow inspect", stderr, workflowInspectUsage)
	jsonOut := fs.Bool("json", false, "write the complete workflow definition as JSON")
	summary := fs.Bool("summary", false, "write workflow metadata without loading the graph")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow inspect requires <uuid>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote *client.Remote) int {
		if *summary {
			limit := 1
			persistedWorkflowID := selector
			response, listErr := listWorkflowPage(context.Background(), remote, serverapi.WorkflowListRequest{
				Limit:      &limit,
				WorkflowID: &persistedWorkflowID,
			})
			if listErr != nil {
				fmt.Fprintln(stderr, listErr)
				return 1
			}
			if response.NextOffset != nil {
				fmt.Fprintln(stderr, "workflow summary response contains a continuation offset")
				return 1
			}
			workflows, projectionErr := workflowRecordsForCLI(response.Workflows)
			if projectionErr != nil {
				fmt.Fprintln(stderr, projectionErr)
				return 1
			}
			if scopeErr := validateWorkflowListProjectMetadata(workflowListExpectedScope{}, response.ProjectID, workflows); scopeErr != nil {
				fmt.Fprintln(stderr, scopeErr)
				return 1
			}
			if len(workflows) == 0 {
				fmt.Fprintf(stderr, "workflow %s not found\n", selector.String())
				return 1
			}
			if len(workflows) != 1 || workflows[0].ID != selector {
				fmt.Fprintln(stderr, "workflow summary response does not match the requested workflow")
				return 1
			}
			projected := workflows[0]
			if *jsonOut {
				return writeCommandJSON(stdout, stderr, projected)
			}
			writeWorkflowSummary(stdout, projected)
			return 0
		}
		def, err := resolveWorkflowDefinition(context.Background(), remote, selector)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			projected, projectionErr := workflowDefinitionForCLI(def)
			if projectionErr != nil {
				fmt.Fprintln(stderr, projectionErr)
				return 1
			}
			return writeCommandJSON(stdout, stderr, projected)
		}
		projected, err := workflowDefinitionForCLI(def)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeWorkflowDefinition(stdout, projected)
		return 0
	})
}

func writeWorkflowDefinition(stdout io.Writer, def serverapi.WorkflowDefinition) {
	writeWorkflowSummary(stdout, def.Workflow)
	writeWorkflowDefinitionNodes(stdout, def.Nodes)
	writeWorkflowDefinitionTransitions(stdout, def)
}

func writeWorkflowSummary(stdout io.Writer, workflow serverapi.WorkflowRecord) {
	fmt.Fprintf(stdout, "Workflow %q (%s), version %d.\n", workflow.Name, workflow.ID, workflow.Version)
	if description := strings.TrimSpace(workflow.Description); description != "" {
		fmt.Fprintf(stdout, "Description: %s\n", description)
	}
	fmt.Fprintf(stdout, "Execution target: %s\n", workflowExecutionTargetPolicySelector(workflow.ExecutionTargetPolicy))
}

func writeWorkflowDefinitionNodes(stdout io.Writer, nodes []serverapi.WorkflowNode) {
	fmt.Fprintf(stdout, "\nNodes (%d):\n", len(nodes))
	for _, node := range nodes {
		line := fmt.Sprintf("- %s (%s): %s", node.Key, node.Kind, node.DisplayName)
		if attrs := workflowNodeAttrs(node); attrs != "" {
			line += "  [" + attrs + "]"
		}
		fmt.Fprintln(stdout, line)
	}
}

// workflowNodeAttrs renders node-kind-specific execution attributes worth
// surfacing in the compact node listing.
func workflowNodeAttrs(node serverapi.WorkflowNode) string {
	attrs := make([]string, 0, 2)
	if role := strings.TrimSpace(node.SubagentRole); role != "" {
		attrs = append(attrs, "role: "+role)
	}
	if mode := strings.TrimSpace(node.CompletionMode); mode != "" {
		attrs = append(attrs, "completion: "+mode)
	}
	if node.Kind == "script" && node.ScriptPath != nil {
		if path := strings.TrimSpace(*node.ScriptPath); path != "" {
			attrs = append(attrs, "script: "+path)
		}
	}
	return strings.Join(attrs, ", ")
}

func writeWorkflowDefinitionTransitions(stdout io.Writer, def serverapi.WorkflowDefinition) {
	nodeKeyByID := workflowNodeKeyByID(def)
	edgesByGroup := make(map[string][]serverapi.WorkflowEdge, len(def.TransitionGroups))
	for _, edge := range def.Edges {
		edgesByGroup[edge.TransitionGroupID] = append(edgesByGroup[edge.TransitionGroupID], edge)
	}
	fmt.Fprintf(stdout, "\nTransitions (%d):\n", len(def.TransitionGroups))
	for _, group := range def.TransitionGroups {
		sourceKey := workflowNodeKeyOrID(nodeKeyByID, group.SourceNodeID)
		edges := edgesByGroup[group.ID]
		if len(edges) == 1 {
			writeWorkflowEdgeLine(stdout, fmt.Sprintf("- %s `%s` → ", sourceKey, group.TransitionID), edges[0], nodeKeyByID)
		} else {
			fmt.Fprintf(stdout, "- %s `%s` fans out (%s):\n", sourceKey, group.TransitionID, group.ID)
			for _, edge := range edges {
				writeWorkflowEdgeLine(stdout, "    → ", edge, nodeKeyByID)
			}
		}
		if description := strings.TrimSpace(group.Description); description != "" {
			fmt.Fprintf(stdout, "    when: %s\n", description)
		}
	}
}

func writeWorkflowEdgeLine(stdout io.Writer, prefix string, edge serverapi.WorkflowEdge, nodeKeyByID map[string]string) {
	targetKey := workflowNodeKeyOrID(nodeKeyByID, edge.TargetNodeID)
	detail := workflowEdgeContextDetail(edge.ContextMode, edge.RequiresApproval, edge.ContextSource)
	fmt.Fprintf(stdout, "%s%s  (edge `%s` %s, %s)\n", prefix, targetKey, edge.Key, edge.ID, detail)
	fmt.Fprintf(stdout, "    assignee selection: %s; thinking selection: %s\n", edge.AssigneeSelection, edge.ThinkingSelection)
	if len(edge.Parameters) > 0 {
		parameters := make([]string, 0, len(edge.Parameters))
		for _, param := range edge.Parameters {
			purpose := workflowParameterPurpose(param)
			parameters = append(parameters, fmt.Sprintf("%s (%s)", param.Key, purpose))
		}
		fmt.Fprintf(stdout, "    params: %s\n", strings.Join(parameters, ", "))
	}
}

func workflowEdgeContextDetail(contextMode string, requiresApproval bool, contextSource serverapi.WorkflowContextSource) string {
	detail := contextMode
	if requiresApproval {
		detail += ", requires approval"
	}
	if source := canonicalAPIContextSource(contextSource); source.Kind == "selected_node" && strings.TrimSpace(source.NodeKey) != "" {
		detail += ", context from " + strings.TrimSpace(source.NodeKey)
	} else if source.Kind == "previous_target" {
		detail += ", context from previous target"
	} else if source.Kind == "previous_target_or_new" {
		detail += ", context from previous target or new session"
	}
	return detail
}

func workflowNodeKeyByID(def serverapi.WorkflowDefinition) map[string]string {
	nodeKeyByID := make(map[string]string, len(def.Nodes))
	for _, node := range def.Nodes {
		nodeKeyByID[node.ID] = node.Key
	}
	return nodeKeyByID
}

func workflowNodeKeyOrID(nodeKeyByID map[string]string, nodeID string) string {
	if key := strings.TrimSpace(nodeKeyByID[nodeID]); key != "" {
		return key
	}
	return strings.TrimSpace(nodeID)
}

func parseWorkflowContextSourceSelector(raw string) (serverapi.WorkflowContextSource, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "immediate_source" {
		return serverapi.WorkflowContextSource{Kind: "immediate_source"}, nil
	}
	if trimmed == "previous_target" || trimmed == "previous_target_or_new" {
		return serverapi.WorkflowContextSource{Kind: trimmed}, nil
	}
	prefix := "node:"
	if strings.HasPrefix(trimmed, prefix) {
		nodeKey := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		if nodeKey == "" {
			return serverapi.WorkflowContextSource{}, errors.New("context source selector node key is required")
		}
		return serverapi.WorkflowContextSource{Kind: "selected_node", NodeKey: nodeKey}, nil
	}
	return serverapi.WorkflowContextSource{}, fmt.Errorf("context source selector must be immediate_source, previous_target, previous_target_or_new, or node:<node-key>")
}

// repeatedStringFlag collects a flag that may be supplied multiple times.
type repeatedStringFlag []string

func (f *repeatedStringFlag) String() string { return strings.Join(*f, ",") }

func (f *repeatedStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

// parseWorkflowParameters converts repeated key=description entries into transition
// parameters. The source agent of the transition must produce a value for each declared
// key; transition prompts reference them as {{.Params.<key>}}, and downstream prompts
// reference guaranteed-prior transitions as {{.Params.<transition_id>.<key>}}.
func parseWorkflowParameters(raw []string) ([]serverapi.WorkflowParameter, error) {
	parameters := make([]serverapi.WorkflowParameter, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, entry := range raw {
		key, description, found := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		description = strings.TrimSpace(description)
		if !found || key == "" || description == "" {
			return nil, fmt.Errorf("parameter %q must be key=description with a non-empty key and description", entry)
		}
		if !workflowkey.Valid(key) {
			return nil, fmt.Errorf("parameter key %q is invalid; it must %s", key, workflowkey.Description)
		}
		if workflowkey.ReservedParameter(key) {
			return nil, fmt.Errorf("parameter key %q is reserved and cannot be declared", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("parameter key %q is declared more than once", key)
		}
		seen[key] = true
		parameters = append(parameters, serverapi.WorkflowParameter{Key: key, Description: description, Purpose: "ordinary"})
	}
	return parameters, nil
}

func parseWorkflowSelectionMode(field, raw string) (string, error) {
	mode := strings.TrimSpace(raw)
	switch mode {
	case "configured", "previous_node":
		return mode, nil
	default:
		return "", fmt.Errorf("--%s must be configured or previous_node", field)
	}
}

func parseWorkflowProtectedParameterFlag(fs *flag.FlagSet, name, raw, purpose string) (*serverapi.WorkflowParameter, error) {
	if !flagExplicit(fs, name) {
		return nil, nil
	}
	key, description, found := strings.Cut(raw, "=")
	key = strings.TrimSpace(key)
	description = strings.TrimSpace(description)
	if !found || key == "" {
		return nil, fmt.Errorf("--%s must be key=description with a non-empty key", name)
	}
	if !workflowkey.Valid(key) {
		return nil, fmt.Errorf("parameter key %q is invalid; it must %s", key, workflowkey.Description)
	}
	if workflowkey.ReservedParameter(key) {
		return nil, fmt.Errorf("parameter key %q is reserved and cannot be declared", key)
	}
	return &serverapi.WorkflowParameter{Key: key, Description: description, Purpose: purpose}, nil
}

func cloneWorkflowParameters(parameters []serverapi.WorkflowParameter) []serverapi.WorkflowParameter {
	if len(parameters) == 0 {
		return nil
	}
	return append([]serverapi.WorkflowParameter(nil), parameters...)
}

func workflowParameterPurpose(parameter serverapi.WorkflowParameter) string {
	return strings.TrimSpace(parameter.Purpose)
}

func workflowParameterIsProtected(parameter serverapi.WorkflowParameter) bool {
	switch workflowParameterPurpose(parameter) {
	case "target_assignee", "target_thinking":
		return true
	default:
		return false
	}
}

func workflowParameterForPurpose(parameters []serverapi.WorkflowParameter, purpose string) (int, bool) {
	for index, parameter := range parameters {
		if workflowParameterPurpose(parameter) == purpose {
			return index, true
		}
	}
	return 0, false
}

func workflowParametersWithProtectedDefaults(parameters []serverapi.WorkflowParameter, assigneeSelection, thinkingSelection string, assignee, thinking *serverapi.WorkflowParameter) ([]serverapi.WorkflowParameter, error) {
	result := cloneWorkflowParameters(parameters)
	for _, item := range []struct {
		selection string
		purpose   string
		key       string
		custom    *serverapi.WorkflowParameter
	}{
		{selection: assigneeSelection, purpose: "target_assignee", key: "agent_role", custom: assignee},
		{selection: thinkingSelection, purpose: "target_thinking", key: "thinking_level", custom: thinking},
	} {
		if item.selection != "previous_node" {
			if item.custom != nil {
				label := "assignee"
				if item.purpose == "target_thinking" {
					label = "thinking"
				}
				return nil, fmt.Errorf("target-%s-param requires %s selection previous_node", label, label)
			}
			continue
		}
		if index, exists := workflowParameterForPurpose(result, item.purpose); exists {
			if item.custom != nil {
				result[index] = *item.custom
			}
			continue
		}
		parameter := serverapi.WorkflowParameter{Key: item.key, Purpose: item.purpose}
		if item.custom != nil {
			parameter = *item.custom
		}
		for _, existing := range result {
			if existing.Key == parameter.Key {
				return nil, fmt.Errorf("parameter key %q is declared more than once", parameter.Key)
			}
		}
		result = append(result, parameter)
	}
	return result, nil
}

func workflowEdgeParametersForAdd(parameters []serverapi.WorkflowParameter, assigneeSelection, thinkingSelection string, assignee, thinking *serverapi.WorkflowParameter) ([]serverapi.WorkflowParameter, error) {
	return workflowParametersWithProtectedDefaults(parameters, assigneeSelection, thinkingSelection, assignee, thinking)
}

func workflowReplaceOrdinaryParameters(existing, replacement []serverapi.WorkflowParameter) []serverapi.WorkflowParameter {
	result := make([]serverapi.WorkflowParameter, 0, len(existing)+len(replacement))
	replacementIndex := 0
	for _, parameter := range existing {
		if workflowParameterIsProtected(parameter) {
			result = append(result, parameter)
			continue
		}
		if replacementIndex < len(replacement) {
			result = append(result, replacement[replacementIndex])
			replacementIndex++
		}
	}
	result = append(result, replacement[replacementIndex:]...)
	return result
}

func workflowClearOrdinaryParameters(existing []serverapi.WorkflowParameter) []serverapi.WorkflowParameter {
	result := make([]serverapi.WorkflowParameter, 0, len(existing))
	for _, parameter := range existing {
		if workflowParameterIsProtected(parameter) {
			result = append(result, parameter)
		}
	}
	return result
}

func workflowEdgeParametersForUpdate(existing []serverapi.WorkflowParameter, assigneeSelection, thinkingSelection string, assignee, thinking *serverapi.WorkflowParameter, ordinary []serverapi.WorkflowParameter, clearOrdinary bool) ([]serverapi.WorkflowParameter, error) {
	parameters := cloneWorkflowParameters(existing)
	if clearOrdinary {
		parameters = workflowClearOrdinaryParameters(parameters)
	} else if ordinary != nil {
		parameters = workflowReplaceOrdinaryParameters(parameters, ordinary)
	}
	return workflowParametersWithProtectedDefaults(parameters, assigneeSelection, thinkingSelection, assignee, thinking)
}

func canonicalAPIContextSource(source serverapi.WorkflowContextSource) serverapi.WorkflowContextSource {
	if strings.TrimSpace(source.Kind) == "" {
		return serverapi.WorkflowContextSource{Kind: "immediate_source"}
	}
	return source
}

func resolveWorkflowDefinition(ctx context.Context, remote apicontract.WorkflowService, selector runtimeids.WorkflowID) (serverapi.WorkflowDefinition, error) {
	getCtx, getCancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer getCancel()
	resp, err := remote.GetWorkflow(getCtx, serverapi.WorkflowGetRequest{WorkflowID: selector})
	if err != nil {
		return serverapi.WorkflowDefinition{}, err
	}
	if resp.Definition.Workflow.ID != selector {
		return serverapi.WorkflowDefinition{}, fmt.Errorf(
			"workflow response identity %q does not match requested workflow %q",
			resp.Definition.Workflow.ID,
			selector.String(),
		)
	}
	return resp.Definition, nil
}

func listWorkflowPage(ctx context.Context, remote apicontract.WorkflowService, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	resp, err := remote.ListWorkflows(rpcCtx, req)
	if err != nil {
		return serverapi.WorkflowListResponse{}, err
	}
	return resp, nil
}

func resolveWorkflowProjectID(ctx context.Context, cfg config.App, remote apicontract.ProjectViewService, ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", errors.New("project is required")
	}
	if trimmed == "." || strings.Contains(trimmed, string(os.PathSeparator)) || pathExists(trimmed) {
		path := trimmed
		if trimmed == "." {
			path = cfg.WorkspaceRoot
		}
		abs, err := normalizeBindingCommandPath(path)
		if err != nil {
			return "", err
		}
		rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
		defer cancel()
		resp, err := remote.ResolveProjectPath(rpcCtx, serverapi.ProjectResolvePathRequest{Path: abs})
		if err != nil {
			return "", err
		}
		if resp.Binding == nil {
			return "", errWorkspaceNotRegistered
		}
		return strings.TrimSpace(resp.Binding.ProjectID), nil
	}
	return trimmed, nil
}

// resolveWorkflowSourceWorkspaceID resolves a --source-workspace reference to a
// workspace id. A path-like reference (".", a path separator, or an existing
// path) is resolved through its project binding; any other value is treated as
// an explicit workspace id.
func resolveWorkflowSourceWorkspaceID(ctx context.Context, cfg config.App, remote apicontract.ProjectViewService, ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", errors.New("source workspace is required")
	}
	if trimmed == "." || strings.Contains(trimmed, string(os.PathSeparator)) || pathExists(trimmed) {
		path := trimmed
		if trimmed == "." {
			path = cfg.WorkspaceRoot
		}
		abs, err := normalizeBindingCommandPath(path)
		if err != nil {
			return "", err
		}
		rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
		defer cancel()
		resp, err := remote.ResolveProjectPath(rpcCtx, serverapi.ProjectResolvePathRequest{Path: abs})
		if err != nil {
			return "", err
		}
		if resp.Binding == nil || strings.TrimSpace(resp.Binding.WorkspaceID) == "" {
			return "", errWorkspaceNotRegistered
		}
		return strings.TrimSpace(resp.Binding.WorkspaceID), nil
	}
	return trimmed, nil
}

func resolveWorkflowProjectLink(
	ctx context.Context,
	cfg config.App,
	projects apicontract.ProjectViewService,
	workflows apicontract.WorkflowService,
	projectRef string,
	selector runtimeids.WorkflowID,
) (serverapi.ProjectWorkflowLink, error) {
	projectID, err := resolveWorkflowProjectID(ctx, cfg, projects, projectRef)
	if err != nil {
		return serverapi.ProjectWorkflowLink{}, err
	}
	workflowID := selector
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	resp, err := workflows.ListProjectWorkflowLinks(rpcCtx, serverapi.WorkflowListProjectLinksRequest{ProjectID: projectID})
	if err != nil {
		return serverapi.ProjectWorkflowLink{}, err
	}
	for _, link := range resp.Links {
		if link.WorkflowID == workflowID {
			return link, nil
		}
	}
	return serverapi.ProjectWorkflowLink{}, fmt.Errorf("project %s has no active link to workflow %s", projectID, workflowID)
}

func writeWorkflowUnlinkBlockers(stderr io.Writer, blockers []serverapi.WorkflowUnlinkProjectBlocker) {
	if len(blockers) == 0 {
		fmt.Fprintln(stderr, "Workflow link was not removed.")
		return
	}
	fmt.Fprintln(stderr, "Cannot unlink; resolve these blockers first:")
	for _, blocker := range blockers {
		writeWorkflowBlockerLine(stderr, blocker.Code, blocker.Message, workflowBlockerCount(int64(blocker.Count)))
		for _, task := range blocker.Tasks {
			fmt.Fprintf(stderr, "    %s: %s\n", task.ShortID, task.Title)
		}
	}
}

func writeWorkflowBlockerLine(w io.Writer, code string, message string, count *int64) {
	if count != nil {
		fmt.Fprintf(w, "- [%s] %s (%d)\n", code, message, *count)
		return
	}
	fmt.Fprintf(w, "- [%s] %s\n", code, message)
}

func workflowBlockerCount(count int64) *int64 {
	if count <= 0 {
		return nil
	}
	return &count
}

func workflowDisplayNameFromKey(key string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(key), func(r rune) bool { return r == '_' || r == '-' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	display := strings.TrimSpace(strings.Join(parts, " "))
	if display == "" {
		return strings.TrimSpace(key)
	}
	return display
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(filepath.Clean(path))
	return err == nil
}

// parseInterspersedPositionals parses fs while allowing positional arguments to appear before,
// between, or after flags. Go's flag package stops at the first non-flag argument, so this
// reparses the remainder after each positional, honoring flags that surround a positional in any
// order (e.g. both `--description "x" "Name"` and a trailing `--json`). It returns the collected
// positionals, or false with an exit code when flag parsing fails.
func parseInterspersedPositionals(fs *flag.FlagSet, args []string) ([]string, bool, int) {
	var positionals []string
	rest := args
	for {
		if ok, code := parseCommandFlags(fs, rest); !ok {
			return nil, false, code
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return positionals, true, 0
		}
		positionals = append(positionals, rest[0])
		rest = rest[1:]
	}
}

// parseWorkflowPositionals parses fs while allowing flags (notably a leading --json) to surround
// the positionals in any order, then enforces the expected positional count, printing usage on a
// mismatch. It returns the positionals, or false with an exit code when parsing or the count check
// fails.
func parseWorkflowPositionals(fs *flag.FlagSet, args []string, count int, stderr io.Writer, usage string) ([]string, bool, int) {
	positionals, ok, code := parseInterspersedPositionals(fs, args)
	if !ok {
		return nil, false, code
	}
	if len(positionals) != count {
		fmt.Fprintln(stderr, usage)
		return nil, false, 2
	}
	return positionals, true, 0
}

func takeLeadingPositionals(args []string, count int) ([]string, []string) {
	if count <= 0 {
		return nil, args
	}
	positionals := make([]string, 0, count)
	index := 0
	for index < len(args) && len(positionals) < count {
		arg := strings.TrimSpace(args[index])
		if arg == "" || strings.HasPrefix(arg, "-") {
			break
		}
		positionals = append(positionals, args[index])
		index++
	}
	return positionals, args[index:]
}
