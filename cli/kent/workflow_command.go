package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/workflowkey"

	"github.com/google/uuid"
)

const (
	workflowCommandTimeout              = 5 * time.Second
	workflowCommandWorkflowListPageSize = serverapi.WorkflowListMaxPageSize
)

// workflowListOutput is the machine-readable shape of `workflow list --json`.
type workflowListOutput struct {
	Workflows     []serverapi.WorkflowRecord `json:"workflows"`
	NextPageToken string                     `json:"next_page_token,omitempty"`
}

// workflowNodeOutput is the machine-readable shape of `workflow node add/update --json`.
type workflowNodeOutput struct {
	WorkflowID string  `json:"workflow_id"`
	NodeID     string  `json:"node_id"`
	Key        string  `json:"key"`
	Kind       string  `json:"kind,omitempty"`
	ScriptPath *string `json:"script_path,omitempty"`
	Version    int64   `json:"version"`
}

// workflowEdgeOutput is the machine-readable shape of `workflow edge add/update --json`.
type workflowEdgeOutput struct {
	WorkflowID        string `json:"workflow_id"`
	EdgeID            string `json:"edge_id"`
	TransitionGroupID string `json:"transition_group_id"`
	Key               string `json:"key,omitempty"`
	TransitionID      string `json:"transition_id,omitempty"`
	Version           int64  `json:"version"`
}

// writeWorkflowJSON encodes v as a single JSON line to stdout, reporting any
// encode failure to stderr. It returns the process exit code to use.
func writeWorkflowJSON(stdout io.Writer, stderr io.Writer, v any) int {
	if err := json.NewEncoder(stdout).Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type workflowCommandRemote interface {
	apicontract.WorkflowService
	ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error)
	Close() error
}

var workflowCommandRemoteOpener func(context.Context, string) (config.App, workflowCommandRemote, error) = func(ctx context.Context, path string) (config.App, workflowCommandRemote, error) {
	cfg, remote, err := bindingCommandRemoteOpener(ctx, path)
	return cfg, remote, err
}

func workflowSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
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
	case "update":
		return workflowUpdateSubcommand(args[1:], stdout, stderr)
	case "list":
		return workflowListSubcommand(args[1:], stdout, stderr)
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

func workflowUpdateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow update", stderr, workflowUsage)
	name := fs.String("name", "", "replace the workflow name")
	description := fs.String("description", "", "replace the workflow description; pass an empty value to clear")
	executionTargetRaw := fs.String("execution-target", "", "workflow execution target: ask-on-first-execution, "+executionTargetSelectorHelp)
	jsonOut := fs.Bool("json", false, "write the updated workflow as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow update requires <workflow>")
	if !ok {
		return exitCode
	}
	if !flagWasProvided(fs, "name") && !flagWasProvided(fs, "description") && !flagWasProvided(fs, "execution-target") {
		fmt.Fprintln(stderr, "workflow update requires at least one of --name, --description, or --execution-target")
		return 2
	}
	var targetPolicy *serverapi.WorkflowExecutionTargetConfiguration
	if flagWasProvided(fs, "execution-target") {
		parsed, err := parseWorkflowExecutionTargetPolicySelector(*executionTargetRaw)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		targetPolicy = &parsed
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	def, err := resolveWorkflowDefinition(context.Background(), remote, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	metadata := serverapi.WorkflowGraphMetadata{
		Name:                  def.Workflow.Name,
		Description:           def.Workflow.Description,
		ExecutionTargetPolicy: targetPolicy,
	}
	if flagWasProvided(fs, "name") {
		metadata.Name = strings.TrimSpace(*name)
	}
	if flagWasProvided(fs, "description") {
		metadata.Description = strings.TrimSpace(*description)
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      def.Workflow.ID,
		ExpectedVersion: def.Workflow.Version,
		Metadata:        &metadata,
		Graph:           workflowGraphDraftFromDefinition(def),
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
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, resp.Definition.Workflow)
	}
	fmt.Fprintf(stdout, "Updated workflow %q (%s) to version %d.\n", resp.Definition.Workflow.Name, resp.Definition.Workflow.ID, resp.Definition.Workflow.Version)
	return 0
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
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_ = cfg
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: name, Description: *description})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, resp.Workflow)
	}
	fmt.Fprintf(stdout, "Created workflow %q (%s).\n", resp.Workflow.Name, resp.Workflow.ID)
	return 0
}

func workflowListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow list", stderr, workflowListUsage)
	pageSize := fs.Int("page-size", workflowCommandWorkflowListPageSize, "maximum number of workflows to return")
	pageToken := fs.String("page-token", "", "continue from a previous list response")
	jsonOut := fs.Bool("json", false, "write workflows and the next page token as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "workflow list does not accept positional arguments")
		return 2
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	workflows, nextPageToken, err := listWorkflowPage(context.Background(), remote, serverapi.WorkflowListRequest{PageSize: *pageSize, PageToken: *pageToken})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, workflowListOutput{Workflows: workflows, NextPageToken: nextPageToken})
	}
	for _, workflow := range workflows {
		fmt.Fprintf(stdout, "%s: %s (v%d)\n", workflow.ID, workflow.Name, workflow.Version)
	}
	if strings.TrimSpace(nextPageToken) != "" {
		fmt.Fprintf(stderr, "Next page token: `%s`\n", nextPageToken)
	}
	return 0
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
	prompt := fs.String("prompt", "", "prompt template for an agent node")
	agent := fs.String("agent", "", "subagent role assigned to an agent node")
	completionMode := fs.String("completion-mode", "", "agent completion contract: auto|structured_output|tool|shell_command|unstructured_output")
	scriptPath := fs.String("script-path", "", "server-side executable for a script node")
	jsonOut := fs.Bool("json", false, "write the added node as JSON")
	workflowRef, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow node add requires <workflow>")
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
	nodeID := "node-" + uuid.NewString()
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	workflowID, err := resolveWorkflowID(context.Background(), remote, workflowRef[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	req := serverapi.WorkflowNodeAddRequest{WorkflowID: workflowID, NodeID: nodeID, Key: *key, Kind: *kind, DisplayName: *displayName, SubagentRole: *agent, PromptTemplate: *prompt, CompletionMode: *completionMode, ScriptPath: workflowScriptPathFlagValue(fs, "script-path", *scriptPath)}
	resp, err := remote.AddWorkflowNode(ctx, req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, workflowNodeOutput{WorkflowID: workflowID, NodeID: nodeID, Key: *key, Kind: *kind, ScriptPath: req.ScriptPath, Version: resp.Version})
	}
	fmt.Fprintf(stdout, "Added %s node `%s` (%s).\n", *kind, *key, nodeID)
	return 0
}

func workflowNodeUpdateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow node update", stderr, workflowNodeUpdateUsage)
	key := fs.String("key", "", "new stable node key")
	kind := fs.String("kind", "", "node kind: start|agent|script|join|terminal")
	displayName := fs.String("display-name", "", "new name shown in task and workflow views")
	prompt := fs.String("prompt", "", "replace the agent prompt template; pass an empty value to clear")
	agent := fs.String("agent", "", "replace the assigned subagent role; pass an empty value to clear")
	completionMode := fs.String("completion-mode", "", "replace the agent completion contract: auto|structured_output|tool|shell_command|unstructured_output")
	scriptPath := fs.String("script-path", "", "replace the server-side executable; pass an empty value to clear")
	jsonOut := fs.Bool("json", false, "write the updated node as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow node update requires <workflow> <node-key>")
	if !ok {
		return exitCode
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	def, err := resolveWorkflowDefinition(context.Background(), remote, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	node, err := workflowNodeByKey(def, positionals[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	updated := node
	if strings.TrimSpace(*key) != "" {
		updated.Key = strings.TrimSpace(*key)
	}
	if strings.TrimSpace(*kind) != "" {
		updated.Kind = strings.TrimSpace(*kind)
	}
	if strings.TrimSpace(*displayName) != "" {
		updated.DisplayName = strings.TrimSpace(*displayName)
	}
	if fs.Lookup("prompt") != nil && flagWasProvided(fs, "prompt") {
		updated.PromptTemplate = *prompt
	}
	if fs.Lookup("agent") != nil && flagWasProvided(fs, "agent") {
		updated.SubagentRole = *agent
	}
	if flagWasProvided(fs, "completion-mode") {
		updated.CompletionMode = strings.TrimSpace(*completionMode)
	}
	if flagWasProvided(fs, "script-path") {
		updated.ScriptPath = workflowScriptPathFlagValue(fs, "script-path", *scriptPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.UpdateWorkflowNode(ctx, serverapi.WorkflowNodeUpdateRequest{
		WorkflowID:         def.Workflow.ID,
		NodeID:             updated.ID,
		Key:                updated.Key,
		Kind:               updated.Kind,
		DisplayName:        updated.DisplayName,
		GroupKey:           updated.GroupKey,
		SubagentRole:       updated.SubagentRole,
		PromptTemplate:     updated.PromptTemplate,
		CompletionMode:     updated.CompletionMode,
		ScriptPath:         updated.ScriptPath,
		InputFields:        updated.InputFields,
		JoinInputProviders: updated.JoinInputProviders,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, workflowNodeOutput{WorkflowID: def.Workflow.ID, NodeID: updated.ID, Key: updated.Key, Kind: updated.Kind, ScriptPath: updated.ScriptPath, Version: resp.Version})
	}
	fmt.Fprintf(stdout, "Updated node `%s`.\n", updated.Key)
	return 0
}

func workflowScriptPathFlagValue(fs *flag.FlagSet, name string, value string) *string {
	if !flagWasProvided(fs, name) {
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
	var params repeatedStringFlag
	fs.Var(&params, "param", "required transition value as key=description; repeatable")
	jsonOut := fs.Bool("json", false, "write the added branch as JSON")
	workflowRef, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow edge add requires <workflow>")
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
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	def, err := resolveWorkflowDefinition(context.Background(), remote, workflowRef[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	source, err := workflowNodeByKey(def, *fromKey)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	target, err := workflowNodeByKey(def, *toKey)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	groupID := ""
	var existingGroup *serverapi.WorkflowTransitionGroup
	for i := range def.TransitionGroups {
		group := def.TransitionGroups[i]
		if group.SourceNodeID == source.ID && group.TransitionID == strings.TrimSpace(*transitionID) {
			groupID = group.ID
			existingGroup = &group
			break
		}
	}
	trimmedDescription := strings.TrimSpace(*transitionDescription)
	if groupID == "" {
		groupID = "group-" + uuid.NewString()
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		resp, addErr := remote.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: def.Workflow.ID, GroupID: groupID, SourceNodeID: source.ID, TransitionID: *transitionID, DisplayName: workflowDisplayNameFromKey(*transitionID), Description: trimmedDescription})
		cancel()
		if addErr != nil {
			fmt.Fprintln(stderr, addErr)
			return 1
		}
		_ = resp
	}
	descriptionRollback := func() {}
	if groupID != "" && flagWasProvided(fs, "transition-description") && existingGroup != nil {
		previousGroup := *existingGroup
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		resp, updateErr := remote.UpdateWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupUpdateRequest{WorkflowID: def.Workflow.ID, GroupID: existingGroup.ID, SourceNodeID: existingGroup.SourceNodeID, TransitionID: existingGroup.TransitionID, DisplayName: existingGroup.DisplayName, Description: trimmedDescription})
		cancel()
		if updateErr != nil {
			fmt.Fprintln(stderr, updateErr)
			return 1
		}
		_ = resp
		// Restore the prior description if a later step fails so a non-zero exit
		// never leaves the transition group description partially mutated.
		descriptionRollback = func() {
			rbCtx, rbCancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
			_, rbErr := remote.UpdateWorkflowTransitionGroup(rbCtx, serverapi.WorkflowTransitionGroupUpdateRequest{WorkflowID: def.Workflow.ID, GroupID: previousGroup.ID, SourceNodeID: previousGroup.SourceNodeID, TransitionID: previousGroup.TransitionID, DisplayName: previousGroup.DisplayName, Description: previousGroup.Description})
			rbCancel()
			if rbErr != nil {
				fmt.Fprintf(stderr, "rollback transition group %s description failed: %v\n", previousGroup.ID, rbErr)
			}
		}
	}
	edgeID := "edge-" + uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	resp, err := remote.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: def.Workflow.ID, EdgeID: edgeID, TransitionGroupID: groupID, Key: *edgeKey, TargetNodeID: target.ID, ContextMode: *contextMode, ContextSource: parsedContextSource, RequiresApproval: *requiresApproval, PromptTemplate: *prompt, Parameters: parsedParameters})
	cancel()
	if err != nil {
		descriptionRollback()
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, workflowEdgeOutput{WorkflowID: def.Workflow.ID, EdgeID: edgeID, TransitionGroupID: groupID, Key: *edgeKey, TransitionID: *transitionID, Version: resp.Version})
	}
	fmt.Fprintf(stdout, "Added edge `%s` (%s) on transition `%s`: `%s` → `%s` (%s).\n", *edgeKey, edgeID, *transitionID, *fromKey, *toKey, workflowEdgeContextDetail(*contextMode, *requiresApproval, parsedContextSource))
	return 0
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
	var params repeatedStringFlag
	fs.Var(&params, "param", "required transition value as key=description; repeatable and replaces all existing values")
	clearParams := fs.Bool("clear-params", false, "remove all transition parameters")
	jsonOut := fs.Bool("json", false, "write the updated branch as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow edge update requires <workflow> <edge-id>")
	if !ok {
		return exitCode
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	def, err := resolveWorkflowDefinition(context.Background(), remote, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	edge, err := workflowEdgeByID(def, positionals[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	group, err := workflowTransitionGroupByID(def, edge.TransitionGroupID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	updatedGroup := group
	if strings.TrimSpace(*transitionID) != "" {
		updatedGroup.TransitionID = strings.TrimSpace(*transitionID)
	}
	if strings.TrimSpace(*transitionDisplayName) != "" {
		updatedGroup.DisplayName = strings.TrimSpace(*transitionDisplayName)
	} else if strings.TrimSpace(*transitionID) != "" {
		updatedGroup.DisplayName = workflowDisplayNameFromKey(*transitionID)
	}
	if flagWasProvided(fs, "transition-description") {
		updatedGroup.Description = strings.TrimSpace(*transitionDescription)
	}
	updatedEdge := edge
	if strings.TrimSpace(*edgeKey) != "" {
		updatedEdge.Key = strings.TrimSpace(*edgeKey)
	}
	if strings.TrimSpace(*toKey) != "" {
		target, targetErr := workflowNodeByKey(def, *toKey)
		if targetErr != nil {
			fmt.Fprintln(stderr, targetErr)
			return 1
		}
		updatedEdge.TargetNodeID = target.ID
	}
	if strings.TrimSpace(*contextMode) != "" {
		updatedEdge.ContextMode = strings.TrimSpace(*contextMode)
	}
	if strings.TrimSpace(*contextSource) != "" {
		parsedContextSource, parseErr := parseWorkflowContextSourceSelector(*contextSource)
		if parseErr != nil {
			fmt.Fprintln(stderr, parseErr)
			return 2
		}
		updatedEdge.ContextSource = parsedContextSource
	}
	if flagWasProvided(fs, "requires-approval") {
		updatedEdge.RequiresApproval = *requiresApproval
	}
	if flagWasProvided(fs, "prompt") {
		updatedEdge.PromptTemplate = *prompt
	}
	if *clearParams && flagWasProvided(fs, "param") {
		fmt.Fprintln(stderr, "use either --param or --clear-params, not both")
		return 2
	}
	if *clearParams {
		updatedEdge.Parameters = nil
	} else if flagWasProvided(fs, "param") {
		parsedParameters, parseErr := parseWorkflowParameters(params)
		if parseErr != nil {
			fmt.Fprintln(stderr, parseErr)
			return 2
		}
		updatedEdge.Parameters = parsedParameters
	}
	// Commit the transition group change only after every edge flag has parsed, so
	// a malformed --param or --context-source can never leave the group mutated
	// while the command exits non-zero before touching the edge.
	if updatedGroup != group {
		groupCtx, groupCancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		groupResp, updateErr := remote.UpdateWorkflowTransitionGroup(groupCtx, serverapi.WorkflowTransitionGroupUpdateRequest{WorkflowID: def.Workflow.ID, GroupID: updatedGroup.ID, SourceNodeID: updatedGroup.SourceNodeID, TransitionID: updatedGroup.TransitionID, DisplayName: updatedGroup.DisplayName, Description: updatedGroup.Description})
		groupCancel()
		if updateErr != nil {
			fmt.Fprintln(stderr, updateErr)
			return 1
		}
		_ = groupResp
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.UpdateWorkflowEdge(ctx, serverapi.WorkflowEdgeUpdateRequest{WorkflowID: def.Workflow.ID, EdgeID: updatedEdge.ID, TransitionGroupID: updatedEdge.TransitionGroupID, Key: updatedEdge.Key, TargetNodeID: updatedEdge.TargetNodeID, ContextMode: updatedEdge.ContextMode, ContextSource: updatedEdge.ContextSource, RequiresApproval: updatedEdge.RequiresApproval, PromptTemplate: updatedEdge.PromptTemplate, Parameters: updatedEdge.Parameters})
	if err != nil {
		if updatedGroup != group {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
			_, rollbackErr := remote.UpdateWorkflowTransitionGroup(rollbackCtx, serverapi.WorkflowTransitionGroupUpdateRequest{WorkflowID: def.Workflow.ID, GroupID: group.ID, SourceNodeID: group.SourceNodeID, TransitionID: group.TransitionID, DisplayName: group.DisplayName, Description: group.Description})
			rollbackCancel()
			if rollbackErr != nil {
				fmt.Fprintf(stderr, "%v; rollback transition group %s failed: %v\n", err, group.ID, rollbackErr)
				return 1
			}
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, workflowEdgeOutput{WorkflowID: def.Workflow.ID, EdgeID: updatedEdge.ID, TransitionGroupID: updatedEdge.TransitionGroupID, Key: updatedEdge.Key, TransitionID: updatedGroup.TransitionID, Version: resp.Version})
	}
	fmt.Fprintf(stdout, "Updated edge `%s`: `%s` → `%s` (%s).\n", updatedEdge.Key, updatedGroup.TransitionID, workflowNodeKeyOrID(workflowNodeKeyByID(def), updatedEdge.TargetNodeID), workflowEdgeContextDetail(updatedEdge.ContextMode, updatedEdge.RequiresApproval, updatedEdge.ContextSource))
	return 0
}

func workflowLinkSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow link", stderr, workflowLinkUsage)
	defaultLink := fs.Bool("default", false, "use this workflow when a project task omits --workflow")
	jsonOut := fs.Bool("json", false, "write the project-workflow link as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow link requires <project> and <workflow>")
	if !ok {
		return exitCode
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	workflowID, err := resolveWorkflowID(context.Background(), remote, positionals[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
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
		return writeWorkflowJSON(stdout, stderr, resp.Link)
	}
	suffix := ""
	if resp.Link.Default {
		suffix = " as the default workflow"
	}
	fmt.Fprintf(stdout, "Linked workflow %s to project %s%s.\n", positionals[1], positionals[0], suffix)
	return 0
}

func workflowUnlinkSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow unlink", stderr, workflowUnlinkUsage)
	jsonOut := fs.Bool("json", false, "write the unlink result and blockers as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow unlink requires <project> and <workflow>")
	if !ok {
		return exitCode
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	link, err := resolveWorkflowProjectLink(context.Background(), cfg, remote, positionals[0], positionals[1])
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
			writeWorkflowJSON(stdout, stderr, resp)
			return 1
		}
		writeWorkflowUnlinkBlockers(stderr, resp.Blockers)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, resp)
	}
	fmt.Fprintf(stdout, "Unlinked workflow %s from project %s.\n", positionals[1], positionals[0])
	return 0
}

func workflowDefaultSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow default", stderr, workflowDefaultUsage)
	jsonOut := fs.Bool("json", false, "write the updated project-workflow link as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 2, stderr, "workflow default requires <project> and <workflow>")
	if !ok {
		return exitCode
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	workflowID, err := resolveWorkflowID(context.Background(), remote, positionals[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.SetDefaultProjectWorkflowLink(ctx, serverapi.WorkflowSetDefaultProjectLinkRequest{ProjectID: projectID, WorkflowID: workflowID})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, resp.Link)
	}
	fmt.Fprintf(stdout, "Set workflow %s as the default for project %s.\n", positionals[1], positionals[0])
	return 0
}

func workflowValidateSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow validate", stderr, workflowValidateUsage)
	mode := fs.String("mode", string(serverapi.WorkflowValidationModeExecution), "validation context: draft|task_creation|execution")
	jsonOut := fs.Bool("json", false, "write validation diagnostics as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow validate requires <workflow>")
	if !ok {
		return exitCode
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	workflowID, err := resolveWorkflowID(context.Background(), remote, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{WorkflowID: workflowID, Mode: serverapi.WorkflowValidationMode(*mode)})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		exit := writeWorkflowJSON(stdout, stderr, resp)
		if exit == 0 && !resp.Valid {
			return 1
		}
		return exit
	}
	if resp.Valid {
		if len(resp.Errors) == 0 {
			fmt.Fprintf(stdout, "Workflow %s is valid in %s mode.\n", workflowID, *mode)
			return 0
		}
		fmt.Fprintf(stdout, "Workflow %s is valid in %s mode with %d diagnostic(s).\n", workflowID, *mode, len(resp.Errors))
		for _, validationErr := range resp.Errors {
			writeWorkflowValidationError(stdout, validationErr)
		}
		return 0
	}
	fmt.Fprintf(stdout, "Workflow %s is invalid in %s mode: %d error(s).\n", workflowID, *mode, len(resp.Errors))
	for _, validationErr := range resp.Errors {
		writeWorkflowValidationError(stdout, validationErr)
	}
	return 1
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
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow inspect requires <workflow>")
	if !ok {
		return exitCode
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	def, err := resolveWorkflowDefinition(context.Background(), remote, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeWorkflowJSON(stdout, stderr, def)
	}
	writeWorkflowDefinition(stdout, def)
	return 0
}

func writeWorkflowDefinition(stdout io.Writer, def serverapi.WorkflowDefinition) {
	fmt.Fprintf(stdout, "Workflow %q (%s), version %d.\n", def.Workflow.Name, def.Workflow.ID, def.Workflow.Version)
	if description := strings.TrimSpace(def.Workflow.Description); description != "" {
		fmt.Fprintf(stdout, "Description: %s\n", description)
	}
	fmt.Fprintf(stdout, "Execution target: %s\n", workflowExecutionTargetPolicySelector(def.Workflow.ExecutionTargetPolicy))
	writeWorkflowDefinitionNodes(stdout, def.Nodes)
	writeWorkflowDefinitionTransitions(stdout, def)
}

func workflowGraphDraftFromDefinition(def serverapi.WorkflowDefinition) serverapi.WorkflowGraphDraft {
	graph := serverapi.WorkflowGraphDraft{
		NodeGroups:       make([]serverapi.WorkflowGraphDraftNodeGroup, 0, len(def.NodeGroups)),
		Nodes:            make([]serverapi.WorkflowGraphDraftNode, 0, len(def.Nodes)),
		TransitionGroups: make([]serverapi.WorkflowGraphDraftTransitionGroup, 0, len(def.TransitionGroups)),
		Edges:            make([]serverapi.WorkflowGraphDraftEdge, 0, len(def.Edges)),
	}
	for _, group := range def.NodeGroups {
		graph.NodeGroups = append(graph.NodeGroups, serverapi.WorkflowGraphDraftNodeGroup{
			ID:          group.GroupID,
			Key:         group.GroupKey,
			DisplayName: group.DisplayName,
		})
	}
	for _, node := range def.Nodes {
		graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
			ID:                 node.ID,
			Key:                node.Key,
			Kind:               node.Kind,
			DisplayName:        node.DisplayName,
			GroupID:            node.GroupID,
			GroupKey:           node.GroupKey,
			SubagentRole:       node.SubagentRole,
			PromptTemplate:     node.PromptTemplate,
			CompletionMode:     node.CompletionMode,
			ScriptPath:         node.ScriptPath,
			InputFields:        node.InputFields,
			JoinInputProviders: node.JoinInputProviders,
		})
	}
	for _, group := range def.TransitionGroups {
		graph.TransitionGroups = append(graph.TransitionGroups, serverapi.WorkflowGraphDraftTransitionGroup{
			ID:           group.ID,
			SourceNodeID: group.SourceNodeID,
			TransitionID: group.TransitionID,
			DisplayName:  group.DisplayName,
			Description:  group.Description,
		})
	}
	for _, edge := range def.Edges {
		graph.Edges = append(graph.Edges, serverapi.WorkflowGraphDraftEdge{
			ID:                edge.ID,
			TransitionGroupID: edge.TransitionGroupID,
			Key:               edge.Key,
			TargetNodeID:      edge.TargetNodeID,
			RequiresApproval:  edge.RequiresApproval,
			ContextMode:       edge.ContextMode,
			ContextSource:     edge.ContextSource,
			PromptTemplate:    edge.PromptTemplate,
			Parameters:        edge.Parameters,
		})
	}
	return graph
}

func writeWorkflowDefinitionNodes(stdout io.Writer, nodes []serverapi.WorkflowNode) {
	fmt.Fprintf(stdout, "\nNodes (%d):\n", len(nodes))
	for _, node := range nodes {
		line := fmt.Sprintf("- %s (%s): %s", node.Key, node.Kind, node.DisplayName)
		if attrs := workflowNodeAttrs(node); attrs != "" {
			line += "  [" + attrs + "]"
		}
		fmt.Fprintln(stdout, line)
		for _, field := range node.OutputFields {
			fmt.Fprintf(stdout, "    output `%s` — %s\n", field.Name, field.Description)
		}
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
	if len(edge.Parameters) > 0 {
		keys := make([]string, 0, len(edge.Parameters))
		for _, param := range edge.Parameters {
			keys = append(keys, param.Key)
		}
		fmt.Fprintf(stdout, "    params: %s\n", strings.Join(keys, ", "))
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
		parameters = append(parameters, serverapi.WorkflowParameter{Key: key, Description: description})
	}
	return parameters, nil
}

func canonicalAPIContextSource(source serverapi.WorkflowContextSource) serverapi.WorkflowContextSource {
	if strings.TrimSpace(source.Kind) == "" {
		return serverapi.WorkflowContextSource{Kind: "immediate_source"}
	}
	return source
}

func resolveWorkflowID(ctx context.Context, remote workflowCommandRemote, ref string) (string, error) {
	def, err := resolveWorkflowDefinition(ctx, remote, ref)
	if err != nil {
		return "", err
	}
	return def.Workflow.ID, nil
}

func resolveWorkflowDefinition(ctx context.Context, remote workflowCommandRemote, ref string) (serverapi.WorkflowDefinition, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return serverapi.WorkflowDefinition{}, errors.New("workflow is required")
	}
	getCtx, getCancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer getCancel()
	resp, err := remote.GetWorkflow(getCtx, serverapi.WorkflowGetRequest{WorkflowID: trimmed})
	if err == nil {
		return resp.Definition, nil
	}
	nameMatches, _, listErr := listWorkflowPage(ctx, remote, serverapi.WorkflowListRequest{PageSize: 2, ExactName: trimmed})
	if listErr != nil {
		return serverapi.WorkflowDefinition{}, listErr
	}
	if len(nameMatches) == 0 {
		return serverapi.WorkflowDefinition{}, fmt.Errorf("workflow %q not found", trimmed)
	}
	if len(nameMatches) > 1 {
		return serverapi.WorkflowDefinition{}, fmt.Errorf("workflow %q is ambiguous; use workflow id", trimmed)
	}
	nameGetCtx, nameGetCancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer nameGetCancel()
	nameResp, err := remote.GetWorkflow(nameGetCtx, serverapi.WorkflowGetRequest{WorkflowID: nameMatches[0].ID})
	if err != nil {
		return serverapi.WorkflowDefinition{}, err
	}
	return nameResp.Definition, nil
}

func listWorkflowPage(ctx context.Context, remote workflowCommandRemote, req serverapi.WorkflowListRequest) ([]serverapi.WorkflowRecord, string, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	resp, err := remote.ListWorkflows(rpcCtx, req)
	if err != nil {
		return nil, "", err
	}
	return resp.Workflows, strings.TrimSpace(resp.NextPageToken), nil
}

func workflowNodeByKey(def serverapi.WorkflowDefinition, key string) (serverapi.WorkflowNode, error) {
	trimmed := strings.TrimSpace(key)
	for _, node := range def.Nodes {
		if node.Key == trimmed {
			return node, nil
		}
	}
	return serverapi.WorkflowNode{}, fmt.Errorf("workflow node key %q not found", trimmed)
}

func workflowEdgeByID(def serverapi.WorkflowDefinition, edgeID string) (serverapi.WorkflowEdge, error) {
	trimmed := strings.TrimSpace(edgeID)
	for _, edge := range def.Edges {
		if edge.ID == trimmed {
			return edge, nil
		}
	}
	return serverapi.WorkflowEdge{}, fmt.Errorf("workflow edge id %q not found", trimmed)
}

func workflowTransitionGroupByID(def serverapi.WorkflowDefinition, groupID string) (serverapi.WorkflowTransitionGroup, error) {
	trimmed := strings.TrimSpace(groupID)
	for _, group := range def.TransitionGroups {
		if group.ID == trimmed {
			return group, nil
		}
	}
	return serverapi.WorkflowTransitionGroup{}, fmt.Errorf("workflow transition group id %q not found", trimmed)
}

func flagWasProvided(fs *flag.FlagSet, name string) bool {
	provided := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			provided = true
		}
	})
	return provided
}

func resolveWorkflowProjectID(ctx context.Context, cfg config.App, remote workflowCommandRemote, ref string) (string, error) {
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
func resolveWorkflowSourceWorkspaceID(ctx context.Context, cfg config.App, remote workflowCommandRemote, ref string) (string, error) {
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

func resolveWorkflowProjectLink(ctx context.Context, cfg config.App, remote workflowCommandRemote, projectRef string, workflowRef string) (serverapi.ProjectWorkflowLink, error) {
	projectID, err := resolveWorkflowProjectID(ctx, cfg, remote, projectRef)
	if err != nil {
		return serverapi.ProjectWorkflowLink{}, err
	}
	workflowID, err := resolveWorkflowID(ctx, remote, workflowRef)
	if err != nil {
		return serverapi.ProjectWorkflowLink{}, err
	}
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	resp, err := remote.ListProjectWorkflowLinks(rpcCtx, serverapi.WorkflowListProjectLinksRequest{ProjectID: projectID})
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
		if blocker.Count > 0 {
			fmt.Fprintf(stderr, "- [%s] %s (%d)\n", blocker.Code, blocker.Message, blocker.Count)
		} else {
			fmt.Fprintf(stderr, "- [%s] %s\n", blocker.Code, blocker.Message)
		}
		for _, task := range blocker.Tasks {
			fmt.Fprintf(stderr, "    %s: %s\n", task.ShortID, task.Title)
		}
	}
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

func workflowTaskSummaryFromCard(projectID string, card serverapi.WorkflowBoardTaskCard) serverapi.WorkflowTaskSummary {
	return serverapi.WorkflowTaskSummary{
		ID:                card.TaskID,
		ProjectID:         projectID,
		WorkflowID:        card.WorkflowID,
		ShortID:           card.ShortID,
		Title:             card.Title,
		BodyPreview:       card.Preview.Markdown,
		SourceWorkspaceID: card.SourceWorkspace.WorkspaceID,
		UpdatedAtUnixMs:   card.UpdatedAtUnixMs,
		Done:              card.Status.Kind == "done",
		ActiveNodeIDs:     append([]string(nil), card.ActiveNodeIDs...),
	}
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
